package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/quic-go/webtransport-go"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3/qlog"
)

func main() {
	if err := runClient(); err != nil {
		log.Fatalf("failed to run client: %v", err)
	}
}

func runClient() error {
	url := flag.String("url", "https://localhost:6121/webtransport", "WebTransport URL")
	protocolsFlag := flag.String("protocols", "webtransport-test,webtransport-test-2", "comma-separated application protocols")
	certHash := flag.String("cert-hash", "", "base64-encoded SHA-256 server certificate hash")
	insecure := flag.Bool("insecure", false, "skip certificate verification")
	flag.Parse()

	var protocols []string
	for p := range strings.SplitSeq(*protocolsFlag, ",") {
		if p = strings.TrimSpace(p); p != "" {
			protocols = append(protocols, p)
		}
	}

	tlsConf := &tls.Config{InsecureSkipVerify: *insecure}
	if hash := strings.TrimSpace(*certHash); hash != "" {
		tlsConf.InsecureSkipVerify = true
		tlsConf.VerifyConnection = func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return fmt.Errorf("server didn't send a certificate")
			}
			actualHash := sha256.Sum256(state.PeerCertificates[0].Raw)
			if got := base64.RawStdEncoding.EncodeToString(actualHash[:]); got != hash {
				return fmt.Errorf("server certificate hash mismatch: got %s", got)
			}
			return nil
		}
	}

	cl := &webtransport.Dialer{
		ApplicationProtocols: protocols,
		TLSClientConfig:      tlsConf,
		QUICConfig: &quic.Config{
			Tracer:                           qlog.DefaultConnectionTracer,
			EnableDatagrams:                  true,
			EnableStreamResetPartialDelivery: true,
		},
	}
	defer cl.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rsp, sess, err := cl.Dial(ctx, *url, nil)
	if err != nil {
		return err
	}
	fmt.Println("HTTP status code:", rsp.StatusCode)
	if rsp.StatusCode < 200 || rsp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status: %d", rsp.StatusCode)
	}
	fmt.Printf("negotiated protocol: %s\n", sess.SessionState().ApplicationProtocol)
	return nil
}
