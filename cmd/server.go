package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"time"

	"github.com/marten-seemann/webtransport-go"

	"github.com/lucas-clemente/quic-go/http3"
)

func main() {
	s := webtransport.Server{
		H3:          http3.Server{Server: &http.Server{Addr: ":4433"}},
		CheckOrigin: func(*http.Request) bool { return true },
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/webtransport", func(w http.ResponseWriter, r *http.Request) {
		done := make(chan struct{})
		go func() {
			defer func() {
				if r := recover(); r != nil {
					fmt.Println("Recovered in f", r)
				}
			}()
			defer close(done)
			conn, err := s.Upgrade(w, r)
			if err != nil {
				w.WriteHeader(404)
				log.Println("webtransport upgrade failed:", err)
				return
			}
			log.Println("Accepted WebTransport connection from", r.RemoteAddr)
			go handleConn(conn)
		}()
		time.Sleep(time.Hour)
		<-done
	})
	s.H3.Handler = mux
	tlsConf, err := getTLSConf()
	if err != nil {
		log.Fatal(err)
	}
	s.H3.TLSConfig = tlsConf
	fmt.Printf("Server Certificate Hash: %#v\n", sha256.Sum256(tlsConf.Certificates[0].Certificate[0]))
	s.ListenAndServe()
}

func handleConn(c *webtransport.Conn) {
	for {
		str, err := c.AcceptStream(context.Background())
		if err != nil {
			log.Fatal(err)
		}

		go func(str webtransport.Stream) {
			data, err := io.ReadAll(str)
			if err != nil {
				log.Fatal(err)
			}
			log.Printf("Received message: %s", string(data))
			str.Close()
		}(str)
	}
}

func getTLSConf() (*tls.Config, error) {
	ca, caPrivateKey, err := generateCA()
	if err != nil {
		return nil, err
	}
	leafCert, leafPrivateKey, err := generateLeafCert(ca, caPrivateKey)
	if err != nil {
		return nil, err
	}
	certPool := x509.NewCertPool()
	certPool.AddCert(ca)
	return &tls.Config{
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{leafCert.Raw},
			PrivateKey:  leafPrivateKey,
		}},
	}, nil
}

func generateCA() (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certTempl := &x509.Certificate{
		SerialNumber:          big.NewInt(2019),
		Subject:               pkix.Name{},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
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

func generateLeafCert(ca *x509.Certificate, caPrivateKey *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certTempl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		DNSNames:     []string{"localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	certBytes, err := x509.CreateCertificate(rand.Reader, certTempl, ca, &privKey.PublicKey, caPrivateKey)
	if err != nil {
		return nil, nil, err
	}
	hash := sha256.New()
	hash.Write(certBytes)
	fmt.Printf("%#v\n", hash.Sum(nil))
	cert, err := x509.ParseCertificate(certBytes)
	if err != nil {
		return nil, nil, err
	}
	return cert, privKey, nil
}
