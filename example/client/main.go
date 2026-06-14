package main

import (
	"context"
	"crypto/tls"
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
	flag.Parse()

	var protocols []string
	for p := range strings.SplitSeq(*protocolsFlag, ",") {
		if p = strings.TrimSpace(p); p != "" {
			protocols = append(protocols, p)
		}
	}

	cl := &webtransport.Dialer{
		ApplicationProtocols: protocols,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
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
	fmt.Println(rsp.StatusCode)
	if rsp.StatusCode < 200 || rsp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status: %d", rsp.StatusCode)
	}
	fmt.Printf("negotiated protocol: %s\n", sess.SessionState().ApplicationProtocol)
	return nil
}
