package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	_ "embed"
	"encoding/base64"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/quic-go/webtransport-go"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/http3/qlog"
)

//go:embed index.html
var browserIndex string

func main() {
	addr := flag.String("addr", ":6121", "WebTransport listen address")
	browserAddr := flag.String("browser-addr", ":6120", "browser client listen address")
	protocolsFlag := flag.String("protocols", "webtransport-test,webtransport-test-2", "comma-separated application protocols")
	flag.Parse()

	var protocols []string
	for p := range strings.SplitSeq(*protocolsFlag, ",") {
		if p = strings.TrimSpace(p); p != "" {
			protocols = append(protocols, p)
		}
	}

	privateKeyBytes := []byte("webtransport-example-cert-key-01")
	certKey, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), privateKeyBytes)
	if err != nil {
		log.Fatalf("failed to create certificate key: %v", err)
	}

	// The W3C serverCertificateHashes API requires the certificate to be valid now,
	// but for less than two weeks. A weekly window keeps the hash stable across restarts.
	now := time.Now().UTC()
	validFrom := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	validFrom = validFrom.AddDate(0, 0, -int((validFrom.Weekday()+6)%7))
	validUntil := validFrom.Add(13 * 24 * time.Hour)

	certTemplate := x509.Certificate{
		SerialNumber:          big.NewInt(validFrom.Unix()),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             validFrom,
		NotAfter:              validUntil,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.ParseIP("::1")},
	}
	// Passing nil makes ECDSA signing deterministic according to RFC 6979.
	certDER, err := x509.CreateCertificate(nil, &certTemplate, &certTemplate, &certKey.PublicKey, certKey)
	if err != nil {
		log.Fatalf("failed to create certificate: %v", err)
	}

	hash := sha256.Sum256(certDER)
	certHash := base64.RawStdEncoding.EncodeToString(hash[:])
	fmt.Printf("Server Certificate Hash: %s\n", certHash)

	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{certDER},
			PrivateKey:  certKey,
		}},
	}
	if err := runServer(tlsConf, certHash, *addr, *browserAddr, protocols); err != nil {
		log.Fatalf("failed to run server: %v", err)
	}
}

func runServer(tlsConf *tls.Config, certHash, addr, browserAddr string, protocols []string) error {
	webTransportAddr := addr
	if host, port, err := net.SplitHostPort(addr); err == nil && host == "" {
		webTransportAddr = net.JoinHostPort("localhost", port)
	}
	webTransportURL := "https://" + webTransportAddr + "/webtransport"

	browserURL := browserAddr
	if host, port, err := net.SplitHostPort(browserAddr); err == nil && host == "" {
		browserURL = net.JoinHostPort("localhost", port)
	}

	browserHTML := strings.ReplaceAll(browserIndex, "{{SERVER_CERTIFICATE_HASH}}", certHash)
	browserHTML = strings.ReplaceAll(browserHTML, "{{WEBTRANSPORT_URL}}", webTransportURL)

	go func() {
		log.Printf("serving browser client on http://%s", browserURL)
		if err := http.ListenAndServe(browserAddr, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, browserHTML)
		})); err != nil {
			log.Printf("serving browser client failed: %s", err)
		}
	}()

	h3Server := &http3.Server{
		Addr:      addr,
		TLSConfig: http3.ConfigureTLSConfig(tlsConf),
		QUICConfig: &quic.Config{
			Tracer:                           qlog.DefaultConnectionTracer,
			EnableDatagrams:                  true,
			EnableStreamResetPartialDelivery: true,
		},
	}
	webtransport.ConfigureHTTP3Server(h3Server)
	mux := http.NewServeMux()
	h3Server.Handler = mux

	s := webtransport.Server{
		ApplicationProtocols: protocols,
		H3:                   h3Server,
		CheckOrigin:          func(*http.Request) bool { return true },
	}

	// Create a new HTTP endpoint /webtransport.
	mux.HandleFunc("/webtransport", func(w http.ResponseWriter, r *http.Request) {
		sess, err := s.Upgrade(w, r)
		if err != nil {
			log.Printf("upgrading failed: %s", err)
			w.WriteHeader(500)
			return
		}
		fmt.Printf("negotiated protocol: %s\n", sess.SessionState().ApplicationProtocol)
	})

	log.Printf("listening on %s", webTransportURL)
	return s.ListenAndServe()
}
