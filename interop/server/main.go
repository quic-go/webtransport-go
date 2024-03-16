package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/quic-go/webtransport-go"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/interop/utils"
)

var tlsConf *tls.Config

func main() {
	logFile, err := os.Create("/logs/log.txt")
	if err != nil {
		fmt.Printf("Could not create log file: %s\n", err.Error())
		os.Exit(1)
	}
	defer logFile.Close()
	log.SetOutput(logFile)

	keyLog, err := utils.GetSSLKeyLog()
	if err != nil {
		fmt.Printf("Could not create key log: %s\n", err.Error())
		os.Exit(1)
	}
	if keyLog != nil {
		defer keyLog.Close()
	}

	testcase := os.Getenv("TESTCASE")

	quicConf := &quic.Config{
		Tracer:          utils.NewQLOGConnectionTracer,
		EnableDatagrams: true,
	}
	cert, err := tls.LoadX509KeyPair("/certs/cert.pem", "/certs/priv.key")
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	hash := sha256.Sum256(cert.Certificate[0])
	fmt.Printf("Server Certificate Hash: %#v (base64: %s)\n", hash, base64.RawStdEncoding.EncodeToString(hash[:]))
	tlsConf = &tls.Config{
		Certificates: []tls.Certificate{cert},
		KeyLogWriter: keyLog,
	}

	switch testcase {
	case "handshake", "transfer":
		err = runServer(quicConf)
	default:
		fmt.Printf("unsupported test case: %s\n", testcase)
		os.Exit(127)
	}

	if err != nil {
		fmt.Printf("Error running server: %s\n", err.Error())
		os.Exit(1)
	}
}

func runServer(quicConf *quic.Config) error {
	// create a new webtransport.Server, listening on (UDP) port 443
	s := webtransport.Server{
		H3: http3.Server{
			Addr:       ":443",
			TLSConfig:  tlsConf,
			QUICConfig: quicConf,
		},
		CheckOrigin: func(*http.Request) bool { return true },
	}

	// Create a new HTTP endpoint /webtransport.
	http.HandleFunc("/webtransport", func(w http.ResponseWriter, r *http.Request) {
		c, err := s.Upgrade(w, r)
		if err != nil {
			log.Printf("upgrading failed: %s", err)
			w.WriteHeader(500)
			return
		}
		for {
			str, err := c.AcceptStream(context.Background())
			if err != nil {
				log.Printf("Error accepting stream: %s\n", err.Error())
				return
			}
			if err := handleStream(str); err != nil {
				log.Printf("Error handling stream: %s\n", err.Error())
				return
			}
		}
		// Handle the connection. Here goes the application logic.
	})

	return s.ListenAndServe()
}

func handleStream(str *webtransport.Stream) error {
	r := bufio.NewReader(str)
	line, err := r.ReadString('\n')
	if err != nil {
		return err
	}
	if strings.HasSuffix(line, "\r\n") {
		line = line[:len(line)-2]
	} else {
		line = strings.TrimRight(line, "\n")
	}
	method, params, cut := strings.Cut(line, " ")
	if !cut {
		return fmt.Errorf("invalid request: %s", line)
	}
	switch method {
	case "GET":
		_, err = str.Write([]byte(params + "\n"))
		return err
	case "PUSH":
	default:
		return fmt.Errorf("unknown method: %s", method)
	}
	parts := strings.Split(params, " ")
	if len(parts) < 1 {
		return fmt.Errorf("invalid request: %s", line)
	}
	f, err := os.Open("/www/" + strings.TrimLeft(parts[0], "/"))
	if err != nil {
		return err
	}
	defer f.Close()
	defer str.Close()
	_, err = io.Copy(str, f)
	return err
}
