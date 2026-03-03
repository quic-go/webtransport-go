package main

import (
	"fmt"
	"os"

	"github.com/quic-go/webtransport-go/interop"
)

func main() {
	if err := interop.RunInteropServer(); err != nil {
		fmt.Printf("failed to run interop server: %v\n", err)
		os.Exit(1)
	}
}
