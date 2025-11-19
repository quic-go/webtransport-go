package webtransport_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"testing"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/qlog"
	"github.com/quic-go/webtransport-go"
)

// BenchmarkRedirectChain benchmarks following a chain of N redirects.
// This measures the performance impact of redirect handling in WebTransport Dial().
// Tests realistic redirect scenarios with 1, 3, 5, and 10 redirects.
func BenchmarkRedirectChain(b *testing.B) {
	redirectChains := []struct {
		name         string
		numRedirects int
	}{
		{"1 redirect", 1},
		{"3 redirects", 3},
		{"5 redirects", 5},
		{"10 redirects", 10},
	}

	for _, chain := range redirectChains {
		b.Run(chain.name, func(b *testing.B) {
			// Create a handler that produces a redirect chain
			// The chain is: /start -> /redirect1 -> /redirect2 -> ... -> /final
			mux := http.NewServeMux()

			// Create redirect handlers for each step in the chain
			for i := 0; i < chain.numRedirects; i++ {
				path := fmt.Sprintf("/redirect%d", i)
				nextPath := fmt.Sprintf("/redirect%d", i+1)
				if i == chain.numRedirects-1 {
					nextPath = "/final"
				}
				// Capture nextPath in closure
				nextPathCopy := nextPath
				mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Location", nextPathCopy)
					w.WriteHeader(http.StatusFound) // 302
				})
			}

			// Create initial redirect handler
			mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "/redirect0")
				w.WriteHeader(http.StatusFound) // 302
			})

			// Final endpoint (after all redirects)
			mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
				// Return bad request since we don't have full WebTransport setup
				w.WriteHeader(http.StatusBadRequest)
			})

			// Start test server
			s := &webtransport.Server{
				H3: http3.Server{
					TLSConfig:  webtransport.TLSConf,
					QUICConfig: &quic.Config{Tracer: qlog.DefaultConnectionTracer, EnableDatagrams: true},
					Handler:    mux,
				},
			}

			laddr, err := net.ResolveUDPAddr("udp", "localhost:0")
			if err != nil {
				b.Fatalf("ResolveUDPAddr failed: %v", err)
			}
			udpConn, err := net.ListenUDP("udp", laddr)
			if err != nil {
				b.Fatalf("ListenUDP failed: %v", err)
			}
			defer udpConn.Close()

			servErr := make(chan error, 1)
			go func() {
				servErr <- s.Serve(udpConn)
			}()
			defer func() {
				s.Close()
				<-servErr
			}()

			serverAddr := fmt.Sprintf("https://localhost:%d/start", udpConn.LocalAddr().(*net.UDPAddr).Port)

			// Create dialer with redirect following enabled
			d := &webtransport.Dialer{
				TLSClientConfig: &tls.Config{RootCAs: webtransport.CertPool},
				QUICConfig:      &quic.Config{Tracer: qlog.DefaultConnectionTracer, EnableDatagrams: true},
				CheckRedirect:   webtransport.FollowRedirects(100), // Allow up to 100 redirects
			}
			defer d.Close()

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				rsp, sess, err := d.Dial(context.Background(), serverAddr, nil)
				if err != nil {
					// We expect an error because /final returns 400
					// Just verify it's expected
					if rsp == nil {
						b.Fatalf("Dial failed unexpectedly: %v", err)
					}
				}
				if sess != nil {
					sess.CloseWithError(0, "")
				}
			}
		})
	}
}

// BenchmarkViaChainAllocation benchmarks the memory allocations involved in
// managing via chains during redirect handling. The via chain is copied before
// passing to CheckRedirect to prevent modification by the callback.
//
// This measures the allocation overhead of:
// - Creating copies of the via slice
// - Growing the via slice as redirects are followed
// Tests with 1, 5, 10, and 20 redirects to show allocation scaling.
func BenchmarkViaChainAllocation(b *testing.B) {
	allocChains := []struct {
		name         string
		numRedirects int
	}{
		{"1 redirect", 1},
		{"5 redirects", 5},
		{"10 redirects", 10},
		{"20 redirects", 20},
	}

	for _, chain := range allocChains {
		b.Run(chain.name, func(b *testing.B) {
			// Create a handler that produces a redirect chain
			mux := http.NewServeMux()

			// Create redirect handlers
			for i := 0; i < chain.numRedirects; i++ {
				path := fmt.Sprintf("/step%d", i)
				nextPath := fmt.Sprintf("/step%d", i+1)
				if i == chain.numRedirects-1 {
					nextPath = "/final"
				}
				nextPathCopy := nextPath
				mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Location", nextPathCopy)
					w.WriteHeader(http.StatusFound) // 302
				})
			}

			// Initial redirect
			mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "/step0")
				w.WriteHeader(http.StatusFound)
			})

			// Final endpoint
			mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
			})

			// Start test server
			s := &webtransport.Server{
				H3: http3.Server{
					TLSConfig:  webtransport.TLSConf,
					QUICConfig: &quic.Config{Tracer: qlog.DefaultConnectionTracer, EnableDatagrams: true},
					Handler:    mux,
				},
			}

			laddr, err := net.ResolveUDPAddr("udp", "localhost:0")
			if err != nil {
				b.Fatalf("ResolveUDPAddr failed: %v", err)
			}
			udpConn, err := net.ListenUDP("udp", laddr)
			if err != nil {
				b.Fatalf("ListenUDP failed: %v", err)
			}
			defer udpConn.Close()

			servErr := make(chan error, 1)
			go func() {
				servErr <- s.Serve(udpConn)
			}()
			defer func() {
				s.Close()
				<-servErr
			}()

			serverAddr := fmt.Sprintf("https://localhost:%d/start", udpConn.LocalAddr().(*net.UDPAddr).Port)

			// Create dialer with a custom CheckRedirect that records via chain details
			// This allows us to measure the allocation impact of via chain operations
			viaChainLengths := []int{}
			d := &webtransport.Dialer{
				TLSClientConfig: &tls.Config{RootCAs: webtransport.CertPool},
				QUICConfig:      &quic.Config{Tracer: qlog.DefaultConnectionTracer, EnableDatagrams: true},
				CheckRedirect: func(req *http.Request, via []*http.Request) error {
					// Record via chain length to verify behavior
					viaChainLengths = append(viaChainLengths, len(via))
					// Allow redirects with a limit
					if len(via) >= 100 {
						return fmt.Errorf("too many redirects")
					}
					return nil // Follow redirect
				},
			}
			defer d.Close()

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				viaChainLengths = viaChainLengths[:0] // Reset for each iteration
				rsp, sess, err := d.Dial(context.Background(), serverAddr, nil)
				if err != nil {
					// Expected error from final endpoint
					if rsp == nil {
						b.Fatalf("Dial failed unexpectedly: %v", err)
					}
				}
				if sess != nil {
					sess.CloseWithError(0, "")
				}
				// Verify we got the expected number of via chain calls
				// Note: we expect numRedirects + 1 because /start redirects to /step0,
				// then /step0 to /step1, etc., so we get called at each redirect boundary
				if len(viaChainLengths) != chain.numRedirects+1 {
					b.Fatalf("expected %d CheckRedirect calls, got %d", chain.numRedirects+1, len(viaChainLengths))
				}
			}
		})
	}
}

// BenchmarkCheckRedirectCallback benchmarks the overhead of invoking the CheckRedirect
// callback itself, measuring just the callback invocation cost without network overhead.
// This is a micro-benchmark that isolates the cost of via chain copying and callback invocation.
func BenchmarkCheckRedirectCallback(b *testing.B) {
	callbackTests := []struct {
		name      string
		viaSize   int
		operation string // what the callback does
	}{
		{"empty via", 0, "minimal"},
		{"1 request via", 1, "minimal"},
		{"5 requests via", 5, "minimal"},
		{"10 requests via", 10, "minimal"},
		{"with length check", 5, "check_length"},
		{"with host check", 5, "check_host"},
	}

	for _, test := range callbackTests {
		b.Run(test.name, func(b *testing.B) {
			// Create test requests for via chain
			via := make([]*http.Request, test.viaSize)
			for i := range via {
				req, _ := http.NewRequest("GET", fmt.Sprintf("https://example.com/redirect%d", i), nil)
				via[i] = req
			}

			req, _ := http.NewRequest("GET", "https://example.com/final", nil)

			// Define the callback based on operation type
			var callback func(req *http.Request, via []*http.Request) error

			switch test.operation {
			case "minimal":
				// Just return nil
				callback = func(req *http.Request, via []*http.Request) error {
					return nil
				}
			case "check_length":
				// Check via chain length (like FollowRedirects)
				callback = func(req *http.Request, via []*http.Request) error {
					if len(via) >= 10 {
						return fmt.Errorf("too many redirects")
					}
					return nil
				}
			case "check_host":
				// Check host in first request (like same-origin policy)
				callback = func(req *http.Request, via []*http.Request) error {
					if len(via) > 0 && via[0].URL.Host != req.URL.Host {
						return fmt.Errorf("cross-origin redirect")
					}
					return nil
				}
			}

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				// Simulate what the Dial code does: copy the via chain before calling CheckRedirect
				viaCopy := make([]*http.Request, len(via))
				copy(viaCopy, via)
				_ = callback(req, viaCopy)
			}
		})
	}
}
