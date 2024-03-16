package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"time"

	"github.com/quic-go/webtransport-go"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/interop/utils"
)

var errUnsupported = errors.New("unsupported test case")

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

	tlsConf = &tls.Config{
		InsecureSkipVerify: true,
		KeyLogWriter:       keyLog,
	}
	testcase := os.Getenv("TESTCASE")
	if err := runTestcase(testcase); err != nil {
		if errors.Is(err, errUnsupported) {
			fmt.Printf("unsupported test case: %s\n", testcase)
			os.Exit(127)
		}
		fmt.Printf("Downloading files failed: %s\n", err.Error())
		os.Exit(1)
	}
}

func runTestcase(testcase string) error {
	switch testcase {
	case "handshake", "transfer":
	default:
		return errUnsupported
	}

	flag.Parse()
	urls := flag.Args()

	quicConf := &quic.Config{
		EnableDatagrams: true,
		Tracer:          utils.NewQLOGConnectionTracer,
	}

	cl := &webtransport.Dialer{
		TLSClientConfig: tlsConf,
		QUICConfig:      quicConf,
	}
	defer cl.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	u, err := url.Parse(urls[0])
	if err != nil {
		return err
	}

	rsp, sess, err := cl.Dial(ctx, fmt.Sprintf("https://%s/webtransport", u.Host), nil)
	if err != nil {
		return err
	}
	if rsp.StatusCode < 200 || rsp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status: %d", rsp.StatusCode)
	}
	str, err := sess.OpenStream()
	if err != nil {
		return err
	}
	return downloadFile(str, urls[0])
}

func downloadFile(str io.ReadWriteCloser, url string) error {
	cmd := "GET " + url + "\r\n"
	if _, err := str.Write([]byte(cmd)); err != nil {
		return err
	}
	if err := str.Close(); err != nil {
		return err
	}

	file, err := os.Create("/downloads" + url)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(file, str)
	return err
}
