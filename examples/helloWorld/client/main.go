package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/quic-go/webtransport-go/examples/helloWorld/common"

	"github.com/quic-go/webtransport-go"
)

func main() {
	dialer := webtransport.Dialer{
		TLSClientConfig: common.InsecureClientTLSConfig(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, session, err := dialer.Dial(ctx, common.ServerURL(), nil)
	if err != nil {
		log.Fatalf("Dial failed: %v", err)
	}
	defer session.CloseWithError(0, "done")

	log.Println("Connected to server")
	fmt.Println("\n===========================================")
	fmt.Println("WebTransport Interactive Client")
	fmt.Println("===========================================")
	fmt.Println("Type 'hello' to get a 'world!' response")
	fmt.Println("Type any other message to see what happens")
	fmt.Println("Type 'quit' or 'exit' to close the connection")
	fmt.Println("===========================================")
	fmt.Println()

	reader := bufio.NewReader(os.Stdin)

	for {
		fmt.Print("Enter message: ")
		message, err := reader.ReadString('\n')
		if err != nil {
			log.Printf("Read input error: %v", err)
			break
		}

		message = strings.TrimSpace(message)

		// Check for exit commands
		if message == "quit" || message == "exit" {
			fmt.Println("Closing connection...")
			break
		}

		if message == "" {
			continue
		}

		// Open a new stream for each message
		stream, err := session.OpenStreamSync(context.Background())
		if err != nil {
			log.Printf("OpenStream failed: %v", err)
			break
		}

		// Send the message
		if _, err := stream.Write([]byte(message)); err != nil {
			log.Printf("Write failed: %v", err)
			stream.Close()
			break
		}
		log.Printf("Sent: %s", message)

		// Close the write side to signal we're done sending
		if err := stream.Close(); err != nil {
			log.Printf("Close failed: %v", err)
			break
		}

		// Read the response
		data, err := io.ReadAll(stream)
		if err != nil {
			log.Printf("Read failed: %v", err)
			break
		}

		log.Printf("Received: %s\n", string(data))
	}

	fmt.Println("Connection closed.")
}
