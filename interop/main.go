package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	_ "embed"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/quic-go/quic-go/http3"

	"github.com/quic-go/webtransport-go"
)

//go:embed index.html
var indexHTML string

var data []byte

func init() {
	data = make([]byte, 1<<20)
	rand.Read(data)
}

func main() {
	tlsConf, err := getTLSConf(time.Now(), time.Now().Add(10*24*time.Hour))
	if err != nil {
		log.Fatal(err)
	}
	hash := sha256.Sum256(tlsConf.Certificates[0].Leaf.Raw)

	go runHTTPServer(hash)

	wmux := http.NewServeMux()
	s := webtransport.Server{
		H3: http3.Server{
			TLSConfig: tlsConf,
			Addr:      "localhost:12345",
			Handler:   wmux,
		},
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	defer s.Close()

	wmux.HandleFunc("/unidirectional", func(w http.ResponseWriter, r *http.Request) {
		conn, err := s.Upgrade(w, r)
		if err != nil {
			log.Printf("upgrading failed: %s", err)
			w.WriteHeader(500)
			return
		}
		runUnidirectionalTest(conn)
	})
	if err := s.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func runHTTPServer(certHash [32]byte) {
	mux := http.NewServeMux()
	mux.HandleFunc("/webtransport", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Println("handler hit")
		content := strings.ReplaceAll(indexHTML, "%%CERTHASH%%", formatByteSlice(certHash[:]))
		content = strings.ReplaceAll(content, "%%DATA%%", formatByteSlice(data))
		content = strings.ReplaceAll(content, "%%TEST%%", "unidirectional")
		w.Write([]byte(content))
	})
	http.ListenAndServe("localhost:8080", mux)
}

func runUnidirectionalTest(sess *webtransport.Session) {
	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		str, err := sess.AcceptUniStream(ctx)
		if err != nil {
			log.Fatalf("failed to accept unidirectional stream: %v", err)
		}
		rvcd, err := io.ReadAll(str)
		if err != nil {
			log.Fatalf("failed to read all data: %v", err)
		}
		if !bytes.Equal(rvcd, data) {
			log.Fatal("data doesn't match")
		}
	}
	select {
	case <-sess.Context().Done():
		fmt.Println("done")
	case <-time.After(5 * time.Second):
		log.Fatal("timed out waiting for the session to be closed")
	}
}

func getTLSConf(start, end time.Time) (*tls.Config, error) {
	cert, priv, err := generateCert(start, end)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{cert.Raw},
			PrivateKey:  priv,
			Leaf:        cert,
		}},
	}, nil
}

func generateCert(start, end time.Time) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return nil, nil, err
	}
	serial := int64(binary.BigEndian.Uint64(b))
	if serial < 0 {
		serial = -serial
	}
	certTempl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{},
		NotBefore:             start,
		NotAfter:              end,
		IsCA:                  true,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	caPrivateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	caBytes, err := x509.CreateCertificate(rand.Reader, certTempl, certTempl, &caPrivateKey.PublicKey, caPrivateKey)
	if err != nil {
		return nil, nil, err
	}
	ca, err := x509.ParseCertificate(caBytes)
	if err != nil {
		return nil, nil, err
	}
	return ca, caPrivateKey, nil
}

func formatByteSlice(b []byte) string {
	s := strings.ReplaceAll(fmt.Sprintf("%#v", b[:]), "[]byte{", "[")
	s = strings.ReplaceAll(s, "}", "]")
	return s
}
