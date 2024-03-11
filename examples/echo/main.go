package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

func main() {
	domain := flag.String("domain", "example.com", "Domain")
	certPath := flag.String("cert-path", "", "Cert path")
	keyPath := flag.String("key-path", "", "Key path")
	flag.Parse()

	go runServer(*certPath, *keyPath)
	runClient(*domain)
}

func runServer(certPath, keyPath string) {

	ctx := context.Background()

	wtServer := webtransport.Server{
		H3: http3.Server{
			Addr:       ":443",
			QuicConfig: &quic.Config{},
		},
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {

		wtSession, err := wtServer.Upgrade(w, r)
		checkErr(err)

		stream, err := wtSession.AcceptStream(ctx)
		checkErr(err)

		bytes, err := io.ReadAll(stream)
		checkErr(err)

		fmt.Println("From client: " + string(bytes))

		_, err = stream.Write(bytes)
		checkErr(err)

		// Note that stream.Close() only closes the send side, so the
		// stream could continue receiving after this if desired.
		err = stream.Close()
		checkErr(err)
	})

	err := wtServer.ListenAndServeTLS(certPath, keyPath)
	checkErr(err)
}

func runClient(domain string) {
	ctx := context.Background()

	var d webtransport.Dialer

	_, wtSession, err := d.Dial(ctx, domain, nil)
	checkErr(err)

	stream, err := wtSession.OpenStreamSync(ctx)
	checkErr(err)

	_, err = stream.Write([]byte("Hi there"))
	checkErr(err)

	err = stream.Close()
	checkErr(err)

	bytes, err := io.ReadAll(stream)
	checkErr(err)

	fmt.Println("From server: " + string(bytes))
}

func checkErr(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, err.Error())
		os.Exit(1)
	}
}
