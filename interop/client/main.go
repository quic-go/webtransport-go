package main

import (
	"fmt"
	"os"

	"github.com/quic-go/webtransport-go/interop"
)

func main() {
	if err := interop.RunInteropClient(); err != nil {
		fmt.Printf("failed to run interop client: %v\n", err)
		os.Exit(1)
	}
}
