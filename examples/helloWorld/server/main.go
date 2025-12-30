package server

import (
	"context"
	"io"
	"log"
	"net/http"

	"github.com/quic-go/webtransport-go/examples/helloWorld/common"

	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

func main() {
	tlsConfig, err := common.GenerateTLSConfig()
	if err != nil {
		log.Fatalf("TLS config error: %v", err)
	}

	server := &webtransport.Server{
		H3: http3.Server{
			Addr:      common.ServerAddr(),
			TLSConfig: tlsConfig,
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc(common.ServerPath, func(w http.ResponseWriter, r *http.Request) {
		session, err := server.Upgrade(w, r)
		if err != nil {
			log.Printf("Upgrade failed: %v", err)
			http.Error(w, "Upgrade failed", http.StatusInternalServerError)
			return
		}

		log.Printf("Client connected: %s", session.RemoteAddr())
		go handleSession(session)
	})

	server.H3.Handler = mux

	log.Printf("WebTransport server running at %s", common.ServerURL())

	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

func handleSession(session *webtransport.Session) {
	defer session.CloseWithError(0, "goodbye")

	for {
		stream, err := session.AcceptStream(context.Background())
		if err != nil {
			log.Printf("AcceptStream error: %v", err)
			return
		}
		go handleStream(stream)
	}
}

func handleStream(stream *webtransport.Stream) {
	defer stream.Close()

	data, err := io.ReadAll(stream)
	if err != nil {
		log.Printf("Read error: %v", err)
		return
	}

	msg := string(data)
	log.Printf("Received: %s", msg)

	response := "I only respond to 'hello' with 'world!'"
	if msg == "hello" {
		response = "world!"
	}

	if _, err := stream.Write([]byte(response)); err != nil {
		log.Printf("Write error: %v", err)
		return
	}

	log.Printf("Sent: %s", response)
}
