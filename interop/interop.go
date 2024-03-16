package interop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/quic-go/webtransport-go"
)

const (
	getPrefix  = "GET "
	pushPrefix = "PUSH "
)

func runTransfer(endpoint string, sess *webtransport.Session) {
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
					log.Printf("failed to read request: %v", err)
					return
				}
				filename, err := parseRequest(data)
				if err != nil {
					str.CancelRead(1234)
					log.Printf("failed to parse request: %v", err)
					return
				}
				defer str.Close()
				if err := sendFile(filepath.Join(endpoint, filename), str); err != nil {
					log.Printf("failed to send file: %v", err)
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
					log.Printf("failed to read request: %v", err)
					return
				}
				filename, err := parseRequest(data)
				if err != nil {
					log.Printf("failed to parse request: %v", err)
					return
				}
				rstr, err := sess.OpenUniStreamSync(context.Background())
				if err != nil {
					log.Printf("failed to open unidirectional stream: %v", err)
					return
				}
				defer rstr.Close()
				if err := sendFile(filepath.Join(endpoint, filename), rstr); err != nil {
					log.Printf("failed to send file: %v", err)
					return
				}
			})
		}
	})
	// handle datagrams
	wg.Go(func() {
		// TODO: datagrams
	})
	wg.Wait()
}

func runTransferUniReceive(sess *webtransport.Session, endpoint string, requests []string) error {
	var eg errgroup.Group
	for _, req := range requests {
		log.Printf("requesting file: %s", req)
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
			return storePush(str, endpoint, "/downloads")
		})
	}
	return eg.Wait()
}

func readFile(path string) ([]byte, error) {
	path = filepath.Join("/www", strings.TrimPrefix(path, "/"))
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file for %s: %v", path, err)
	}
	return data, nil
}

func writeDownload(baseDir, rel string, data []byte) error {
	rel = strings.TrimPrefix(rel, "/")
	full := filepath.Join(baseDir, rel)
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

func storePush(r io.Reader, endpoint, downloadDir string) error {
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
	log.Printf("received PUSH for %s: %d bytes", filename, len(payload))
	// Save under endpoint subdir so the test framework finds SERVER_DOWNLOADS/endpoint/
	rel := filepath.Join(endpoint, filepath.Base(filename))
	return writeDownload(downloadDir, rel, payload)
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

func sendFile(filename string, w io.Writer) error {
	payload, err := readFile(filename)
	if err != nil {
		return fmt.Errorf("failed to read file for %s: %v", filename, err)
	}
	if _, err := w.Write([]byte(pushPrefix + filepath.Base(filename) + "\n")); err != nil {
		return err
	}
	_, err = w.Write(payload)
	return err
}
