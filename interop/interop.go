package interop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/quic-go/webtransport-go"
)

const (
	getPrefix  = "GET "
	pushPrefix = "PUSH "
)

func runTransfer(endpoint string, sess *webtransport.Session, log *slog.Logger) {
	var wg sync.WaitGroup
	// handle bidirectional streams
	wg.Go(func() {
		for {
			str, err := sess.AcceptStream(context.Background())
			if err != nil {
				return
			}
			wg.Go(func() {
				data, err := io.ReadAll(str)
				if err != nil {
					str.CancelRead(1234)
					log.Error("failed to read request", "err", err)
					return
				}
				filename, err := parseRequest(data)
				if err != nil {
					str.CancelRead(1234)
					log.Error("failed to parse request", "err", err)
					return
				}
				defer str.Close()
				if err := sendFile(filepath.Join(endpoint, filename), str, log); err != nil {
					log.Error("failed to send file", "err", err)
					return
				}
			})
		}
	})
	// handle unidirectional streams
	wg.Go(func() {
		for {
			str, err := sess.AcceptUniStream(context.Background())
			if err != nil {
				return
			}
			wg.Go(func() {
				data, err := io.ReadAll(str)
				if err != nil {
					log.Error("failed to read request", "err", err)
					return
				}
				filename, err := parseRequest(data)
				if err != nil {
					log.Error("failed to parse request", "err", err)
					return
				}
				rstr, err := sess.OpenUniStreamSync(context.Background())
				if err != nil {
					log.Error("failed to open unidirectional stream", "err", err)
					return
				}
				defer rstr.Close()
				if err := pushFile(filepath.Join(endpoint, filename), rstr, log); err != nil {
					log.Error("failed to send file", "err", err)
					return
				}
			})
		}
	})
	// handle datagrams
	wg.Go(func() {
		for {
			data, err := sess.ReceiveDatagram(context.Background())
			if err != nil {
				return
			}
			filename, err := parseRequest(data)
			if err != nil {
				log.Error("failed to parse datagram request", "err", err)
				continue
			}
			payload, err := readFile(filepath.Join(endpoint, filename))
			if err != nil {
				log.Error("failed to read file for datagram response", "filename", filename, "err", err)
				continue
			}
			pushPayload := append([]byte(pushPrefix+filepath.Base(filename)+"\n"), payload...)
			log.Info("sending datagram response", "filename", filename, "bytes", len(pushPayload))
			if err := sess.SendDatagram(pushPayload); err != nil {
				log.Error("failed to send datagram response", "err", err)
				return
			}
		}
	})
	wg.Wait()
}

func runTransferUniReceive(sess *webtransport.Session, endpoint string, requests []string, log *slog.Logger) error {
	var eg errgroup.Group
	for _, req := range requests {
		log.Info("requesting file", "file", req)
		eg.Go(func() error {
			str, err := sess.OpenUniStreamSync(context.Background())
			if err != nil {
				return err
			}
			if err := requestFile(str, req); err != nil {
				return fmt.Errorf("failed to request file %s: %w", req, err)
			}
			return nil
		})
	}
	for range len(requests) {
		eg.Go(func() error {
			str, err := sess.AcceptUniStream(context.Background())
			if err != nil {
				return err
			}
			return storePush(str, endpoint, log)
		})
	}
	return eg.Wait()
}

func runTransferBidiReceive(sess *webtransport.Session, endpoint string, requests []string, log *slog.Logger) error {
	var eg errgroup.Group
	for _, req := range requests {
		log.Info("requesting file", "file", req)
		eg.Go(func() error {
			str, err := sess.OpenStreamSync(context.Background())
			if err != nil {
				return err
			}
			if err := requestFile(str, req); err != nil {
				return fmt.Errorf("failed to request file %s: %w", req, err)
			}
			data, err := io.ReadAll(str)
			if err != nil {
				return fmt.Errorf("failed to read response: %w", err)
			}
			return saveFile(filepath.Join(endpoint, filepath.Base(req)), data)
		})
	}
	return eg.Wait()
}

func runTransferDatagramReceive(sess *webtransport.Session, endpoint string, requests []string, log *slog.Logger) error {
	var eg errgroup.Group
	eg.Go(func() error {
		for _, req := range requests {
			log.Info("requesting file (datagram)", "file", req)
			if err := sess.SendDatagram([]byte(getPrefix + req)); err != nil {
				return fmt.Errorf("failed to send GET datagram for %s: %w", req, err)
			}
			time.Sleep(20 * time.Millisecond)
		}
		return nil
	})
	eg.Go(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		for i := range len(requests) {
			data, err := sess.ReceiveDatagram(ctx)
			if err != nil {
				return fmt.Errorf("receiving PUSH datagram %d/%d: %w", i+1, len(requests), err)
			}
			if err := storePushFromDatagram(data, endpoint, log); err != nil {
				return err
			}
		}
		return nil
	})
	return eg.Wait()
}

func storePushFromDatagram(data []byte, endpoint string, log *slog.Logger) error {
	if !bytes.HasPrefix(data, []byte(pushPrefix)) {
		return fmt.Errorf("unexpected datagram, missing PUSH prefix")
	}
	rest := bytes.TrimPrefix(data, []byte(pushPrefix))
	name, payload, ok := bytes.Cut(rest, []byte("\n"))
	if !ok {
		return fmt.Errorf("missing newline in PUSH datagram")
	}
	if len(name) == 0 {
		return fmt.Errorf("missing filename in PUSH datagram")
	}
	filename := string(bytes.TrimSpace(name))
	log.Info("received PUSH datagram", "filename", filename, "bytes", len(payload))
	rel := filepath.Join(endpoint, filepath.Base(filename))
	return saveFile(rel, payload)
}

func readFile(path string) ([]byte, error) {
	path = filepath.Join("/www", strings.TrimPrefix(path, "/"))
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file for %s: %v", path, err)
	}
	return data, nil
}

func saveFile(name string, data []byte) error {
	full := filepath.Join("/downloads", name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, data, 0o644)
}

func requestFile(w io.WriteCloser, filename string) error {
	defer w.Close()
	_, err := w.Write([]byte(getPrefix + filename))
	return err
}

func storePush(r io.Reader, endpoint string, log *slog.Logger) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if !bytes.HasPrefix(data, []byte(pushPrefix)) {
		return fmt.Errorf("unexpected data, missing PUSH prefix")
	}
	rest := bytes.TrimPrefix(data, []byte(pushPrefix))
	name, payload, ok := bytes.Cut(rest, []byte("\n"))
	if !ok {
		return fmt.Errorf("missing newline in PUSH")
	}
	if len(name) == 0 {
		return fmt.Errorf("missing filename in PUSH")
	}
	filename := string(bytes.TrimSpace(name))
	log.Info("received PUSH", "filename", filename, "bytes", len(payload))
	rel := filepath.Join(endpoint, filepath.Base(filename))
	return saveFile(rel, payload)
}

func parseRequest(data []byte) (string, error) {
	if !bytes.HasPrefix(data, []byte(getPrefix)) {
		return "", errors.New("unexpected data, missing GET prefix")
	}
	filename := strings.TrimSpace(string(bytes.TrimPrefix(data, []byte(getPrefix))))
	if filename == "" {
		return "", errors.New("missing filename in GET")
	}
	return filename, nil
}

func sendFile(filename string, w io.Writer, log *slog.Logger) error {
	payload, err := readFile(filename)
	if err != nil {
		return fmt.Errorf("failed to read file for %s: %v", filename, err)
	}
	log.Info("sending file", "filename", filename, "bytes", len(payload))
	_, err = w.Write(payload)
	return err
}

func pushFile(filename string, w io.Writer, log *slog.Logger) error {
	payload, err := readFile(filename)
	if err != nil {
		return fmt.Errorf("failed to read file for %s: %v", filename, err)
	}
	log.Info("sending file", "filename", filename, "bytes", len(payload))
	if _, err := w.Write([]byte(pushPrefix + filepath.Base(filename) + "\n")); err != nil {
		return err
	}
	_, err = w.Write(payload)
	return err
}
