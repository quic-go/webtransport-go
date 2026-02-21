package interop

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/interop/utils"

	"github.com/quic-go/webtransport-go"
)

type session struct {
	Endpoint string
	Session  *webtransport.Session
}

func parseServerRequests(s string) map[string][]string {
	if s == "" {
		return nil
	}
	requests := make(map[string][]string)
	for pathStr := range strings.FieldsSeq(s) {
		pathStr = strings.TrimPrefix(strings.TrimSpace(pathStr), "/")
		if pathStr == "" {
			continue
		}
		idx := strings.Index(pathStr, "/")
		if idx < 0 {
			requests[pathStr] = []string{}
			continue
		}
		endpoint := pathStr[:idx]
		rest := pathStr[idx+1:]
		requests[endpoint] = append(requests[endpoint], rest)
	}
	return requests
}

func RunInteropServer() error {
	switch os.Getenv("TESTCASE") {
	case "handshake", "transfer", "transfer-unidirectional-send", "transfer-bidirectional-send", "transfer-datagram-send":
	default:
		os.Exit(127)
	}

	requestMap := parseServerRequests(os.Getenv("REQUESTS"))
	if len(requestMap) > 1 {
		return fmt.Errorf("webtransport-go currently doesn't support flow control")
	}

	logFile, err := os.Create("/logs/log.txt")
	if err != nil {
		return fmt.Errorf("failed to create log file: %w", err)
	}
	defer logFile.Close()
	log.SetOutput(io.MultiWriter(logFile, os.Stderr))

	keyLog, err := utils.GetSSLKeyLog()
	if err != nil {
		return fmt.Errorf("failed to create key log: %w", err)
	}
	if keyLog != nil {
		defer keyLog.Close()
	}

	protocols := strings.Split(os.Getenv("PROTOCOLS"), " ")

	quicConf := &quic.Config{
		Tracer:                           utils.NewQLOGConnectionTracer,
		EnableStreamResetPartialDelivery: true,
		EnableDatagrams:                  true,
	}
	cert, err := tls.LoadX509KeyPair("/certs/cert.pem", "/certs/priv.key")
	if err != nil {
		return fmt.Errorf("failed to load certificate: %w", err)
	}
	hash := sha256.Sum256(cert.Certificate[0])
	log.Printf("Server Certificate Hash: %s", base64.RawStdEncoding.EncodeToString(hash[:]))

	// create a new webtransport.Server, listening on (UDP) port 443
	h3Server := &http3.Server{
		Addr: ":443",
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{cert},
			KeyLogWriter: keyLog,
			NextProtos:   []string{http3.NextProtoH3},
		},
		QUICConfig: quicConf,
	}
	webtransport.ConfigureHTTP3Server(h3Server)
	s := &webtransport.Server{
		ApplicationProtocols: protocols,
		H3:                   h3Server,
		CheckOrigin:          func(*http.Request) bool { return true },
	}
	defer s.Close()

	sessChan := make(chan *session)

	endpoints, err := discoverEndpoints("/www")
	if err != nil {
		return fmt.Errorf("failed to read /www: %w", err)
	}
	if len(endpoints) == 0 {
		return errors.New("no endpoints discovered in /www")
	}
	for _, ep := range endpoints {
		handler := func(w http.ResponseWriter, r *http.Request) {
			c, err := s.Upgrade(w, r)
			if err != nil {
				log.Printf("upgrading failed on %s: %s", ep, err)
				w.WriteHeader(500)
				return
			}
			sessChan <- &session{Endpoint: ep, Session: c}
		}

		http.HandleFunc("/"+ep, handler)
		if !strings.HasSuffix(ep, "/") {
			http.HandleFunc("/"+ep+"/", handler)
		}
		log.Printf("registered WebTransport endpoint %s", ep)
	}

	go func() {
		if err := s.ListenAndServe(); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("failed to listen and serve: %s", err.Error())
			os.Exit(1)
		}
	}()

	se := <-sessChan
	sess := se.Session
	endpoint := se.Endpoint

	switch testcase := os.Getenv("TESTCASE"); testcase {
	case "handshake":
		proto := sess.SessionState().ApplicationProtocol
		if err := os.WriteFile("/downloads/negotiated_protocol.txt", []byte(proto), 0o644); err != nil {
			return fmt.Errorf("failed to write negotiated_protocol.txt: %w", err)
		}
		<-sess.Context().Done()
	case "transfer":
		runTransfer(endpoint, sess)
	case "transfer-datagram-send":
		return runTransferDatagramReceive(sess, endpoint, requestMap[endpoint])
	case "transfer-unidirectional-send":
		return runTransferUniReceive(sess, endpoint, requestMap[endpoint])
	case "transfer-bidirectional-send":
		return runTransferBidiReceive(sess, endpoint, requestMap[endpoint])
	default:
		os.Exit(127)
	}
	return nil
}

func discoverEndpoints(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	endpoints := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			endpoints = append(endpoints, e.Name())
		}
	}
	return endpoints, nil
}
