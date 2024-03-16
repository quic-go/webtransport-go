package interop

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/interop/utils"

	"github.com/quic-go/webtransport-go"
)

func parseClientRequests(s string) (map[string][]string, error) {
	if s == "" {
		return nil, nil
	}
	requests := make(map[string][]string)
	for urlStr := range strings.SplitSeq(s, " ") {
		urlStr = strings.TrimSpace(urlStr)
		if urlStr == "" {
			continue
		}
		u, err := url.Parse(urlStr)
		if err != nil {
			return nil, err
		}
		if strings.Count(u.Path, "/") < 2 {
			requests[u.Host+u.Path] = []string{}
			continue
		}
		splits := strings.SplitN(u.Path[1:], "/", 2)
		first, rest := splits[0], splits[1]
		k := u.Host + "/" + first
		requests[k] = append(requests[k], rest)
	}
	return requests, nil
}

func RunInteropClient() error {
	requestMap, err := parseClientRequests(os.Getenv("REQUESTS"))
	if err != nil {
		return err
	}
	if len(requestMap) > 1 {
		return fmt.Errorf("webtransport-go currently doesn't support flow control")
	}

	protocols := strings.Split(os.Getenv("PROTOCOLS"), " ")

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

	cl := &webtransport.Dialer{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
			KeyLogWriter:       keyLog,
			NextProtos:         []string{http3.NextProtoH3},
		},
		QUICConfig: &quic.Config{
			EnableDatagrams:                  true,
			EnableStreamResetPartialDelivery: true,
			Tracer:                           utils.NewQLOGConnectionTracer,
		},
		ApplicationProtocols: protocols,
	}
	defer cl.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var sess *webtransport.Session
	var requests []string
	var endpoint string
	for k, v := range requestMap {
		var rsp *http.Response
		var err error
		rsp, sess, err = cl.Dial(ctx, "https://"+k, nil)
		if err != nil {
			return fmt.Errorf("failed to dial %s: %w", k, err)
		}
		if rsp.StatusCode < 200 || rsp.StatusCode >= 300 {
			return fmt.Errorf("unexpected status: %d", rsp.StatusCode)
		}
		requests = v
		endpoint = k[strings.Index(k, "/")+1:]
		break
	}
	if sess != nil {
		defer sess.CloseWithError(0, "")
	}

	switch testcase := os.Getenv("TESTCASE"); testcase {
	case "handshake":
		proto := sess.SessionState().ApplicationProtocol
		if err := os.WriteFile("/downloads/negotiated_protocol.txt", []byte(proto), 0o644); err != nil {
			return fmt.Errorf("failed to write negotiated_protocol.txt: %w", err)
		}
	case "transfer":
		u := slices.Collect(maps.Keys(requestMap))[0]
		runTransfer(u[strings.Index(u, "/")+1:], sess)
	case "transfer-unidirectional-receive":
		return runTransferUniReceive(sess, endpoint, requests)
	default:
		os.Exit(127)
	}
	return nil
}
