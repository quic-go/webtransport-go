package redirect

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"testing"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	webtransport "github.com/quic-go/webtransport-go"
	"github.com/stretchr/testify/require"
)

// testServer represents a test WebTransport server
type testServer struct {
	*http3.Server
	URL      string
	handler  http.Handler
	closeCh  chan struct{}
	listener *quic.EarlyListener
}

// newTestServer creates a new test server with the given handler
func newTestServer(t *testing.T, handler http.Handler) *testServer {
	t.Helper()

	// Create QUIC listener on random port
	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{generateTestCert(t)},
		NextProtos:   []string{http3.NextProtoH3},
	}

	quicConf := &quic.Config{
		EnableDatagrams: true,
	}

	listener, err := quic.ListenAddrEarly("127.0.0.1:0", tlsConf, quicConf)
	require.NoError(t, err)

	server := &http3.Server{
		Handler: handler,
	}

	ts := &testServer{
		Server:   server,
		URL:      fmt.Sprintf("https://127.0.0.1:%d", listener.Addr().(*net.UDPAddr).Port),
		handler:  handler,
		closeCh:  make(chan struct{}),
		listener: listener,
	}

	go func() {
		_ = server.ServeListener(listener)
		close(ts.closeCh)
	}()

	return ts
}

// Close shuts down the test server
func (ts *testServer) Close() {
	ts.Server.Close()
	ts.listener.Close()
	<-ts.closeCh
}

// redirectHandler creates an HTTP handler that returns a redirect to the given URL
func redirectHandler(statusCode int, location string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", location)
		w.WriteHeader(statusCode)
	}
}

// redirectChainHandler creates a handler that redirects through a chain of URLs
func redirectChainHandler(statusCodes []int, locations []string, finalHandler http.Handler) http.HandlerFunc {
	index := 0
	return func(w http.ResponseWriter, r *http.Request) {
		if index < len(locations) {
			w.Header().Set("Location", locations[index])
			w.WriteHeader(statusCodes[index])
			index++
		} else {
			finalHandler.ServeHTTP(w, r)
		}
	}
}

// successHandler creates a handler that returns a successful WebTransport response
func successHandler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// For testing, we just need to return 200 OK
		// The actual WebTransport session handling is done by the server
		w.WriteHeader(http.StatusOK)
	}
}

// generateTestCert generates a self-signed certificate for testing
func generateTestCert(t *testing.T) tls.Certificate {
	t.Helper()

	// Use a simple self-signed cert for testing
	cert, err := tls.LoadX509KeyPair("testdata/cert.pem", "testdata/key.pem")
	if err != nil {
		// If files don't exist, generate in-memory cert
		cert = generateInMemoryCert(t)
	}
	return cert
}

// generateInMemoryCert generates a self-signed certificate in memory
func generateInMemoryCert(t *testing.T) tls.Certificate {
	t.Helper()

	// For now, just return an empty cert - we'll implement proper generation if needed
	// This will be populated during actual test implementation
	t.Skip("Certificate generation not yet implemented")
	return tls.Certificate{}
}

// dialWithInsecureSkipVerify creates a dialer that skips TLS verification for testing
func dialWithInsecureSkipVerify() *webtransport.Dialer {
	return &webtransport.Dialer{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
	}
}

// requireNoError is a helper that fails the test if err is not nil
func requireNoError(t *testing.T, err error, msgAndArgs ...interface{}) {
	t.Helper()
	require.NoError(t, err, msgAndArgs...)
}

// requireError is a helper that fails the test if err is nil
func requireError(t *testing.T, err error, msgAndArgs ...interface{}) {
	t.Helper()
	require.Error(t, err, msgAndArgs...)
}

// requireEqual is a helper that fails the test if expected != actual
func requireEqual(t *testing.T, expected, actual interface{}, msgAndArgs ...interface{}) {
	t.Helper()
	require.Equal(t, expected, actual, msgAndArgs...)
}
