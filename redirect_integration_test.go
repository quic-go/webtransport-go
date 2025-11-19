package webtransport_test

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/qlog"
	"github.com/quic-go/webtransport-go"
	"github.com/stretchr/testify/require"
)

// runRedirectServer starts a test server that returns redirect responses
func runRedirectServer(t *testing.T, handler http.Handler) (addr *net.UDPAddr, close func()) {
	t.Helper()

	s := &webtransport.Server{
		H3: http3.Server{
			TLSConfig:  webtransport.TLSConf,
			QUICConfig: &quic.Config{Tracer: qlog.DefaultConnectionTracer, EnableDatagrams: true},
			Handler:    handler,
		},
	}

	laddr, err := net.ResolveUDPAddr("udp", "localhost:0")
	require.NoError(t, err)
	udpConn, err := net.ListenUDP("udp", laddr)
	require.NoError(t, err)

	servErr := make(chan error, 1)
	go func() {
		servErr <- s.Serve(udpConn)
	}()

	return udpConn.LocalAddr().(*net.UDPAddr), func() {
		require.NoError(t, s.Close())
		<-servErr
		udpConn.Close()
	}
}

// TestDialer_RedirectWithoutCheckRedirect tests that Dial() with nil CheckRedirect
// returns redirect response without following
func TestDialer_RedirectWithoutCheckRedirect(t *testing.T) {
	// Create handler that returns a redirect
	mux := http.NewServeMux()
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/final")
		w.WriteHeader(http.StatusFound) // 302
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	addr, closeServer := runRedirectServer(t, mux)
	defer closeServer()

	// Create dialer with nil CheckRedirect (default behavior)
	d := &webtransport.Dialer{
		TLSClientConfig: &tls.Config{RootCAs: webtransport.CertPool},
		QUICConfig:      &quic.Config{Tracer: qlog.DefaultConnectionTracer, EnableDatagrams: true},
		// CheckRedirect is nil (default)
	}
	defer d.Close()

	// Dial the redirect URL
	url := fmt.Sprintf("https://localhost:%d/redirect", addr.Port)
	rsp, sess, err := d.Dial(context.Background(), url, nil)

	// Verify we got the redirect response without following
	require.NoError(t, err)
	require.Nil(t, sess) // No session for redirect responses
	require.Equal(t, http.StatusFound, rsp.StatusCode)
	require.Equal(t, "/final", rsp.Header.Get("Location"))
}

// TestDialer_RedirectWithMissingLocation tests that redirects without Location header return error
func TestDialer_RedirectWithMissingLocation(t *testing.T) {
	// Create handler that returns a redirect without Location header
	mux := http.NewServeMux()
	mux.HandleFunc("/bad-redirect", func(w http.ResponseWriter, r *http.Request) {
		// Don't set Location header
		w.WriteHeader(http.StatusFound) // 302
	})

	addr, closeServer := runRedirectServer(t, mux)
	defer closeServer()

	d := &webtransport.Dialer{
		TLSClientConfig: &tls.Config{RootCAs: webtransport.CertPool},
		QUICConfig:      &quic.Config{Tracer: qlog.DefaultConnectionTracer, EnableDatagrams: true},
	}
	defer d.Close()

	url := fmt.Sprintf("https://localhost:%d/bad-redirect", addr.Port)
	rsp, sess, err := d.Dial(context.Background(), url, nil)

	// Verify we get an error for missing Location header
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing Location header")
	require.NotNil(t, rsp) // Response should still be returned
	require.Nil(t, sess)   // No session
}

// TestDialer_RedirectAllStatusCodes tests all redirect status codes (301, 302, 303, 307, 308)
func TestDialer_RedirectAllStatusCodes(t *testing.T) {
	testCases := []struct {
		name       string
		statusCode int
	}{
		{"301 Moved Permanently", http.StatusMovedPermanently},
		{"302 Found", http.StatusFound},
		{"303 See Other", http.StatusSeeOther},
		{"307 Temporary Redirect", http.StatusTemporaryRedirect},
		{"308 Permanent Redirect", http.StatusPermanentRedirect},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create handler that returns the specific redirect status
			mux := http.NewServeMux()
			mux.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "/target")
				w.WriteHeader(tc.statusCode)
			})

			addr, closeServer := runRedirectServer(t, mux)
			defer closeServer()

			d := &webtransport.Dialer{
				TLSClientConfig: &tls.Config{RootCAs: webtransport.CertPool},
				QUICConfig:      &quic.Config{Tracer: qlog.DefaultConnectionTracer, EnableDatagrams: true},
			}
			defer d.Close()

			url := fmt.Sprintf("https://localhost:%d/test", addr.Port)
			rsp, sess, err := d.Dial(context.Background(), url, nil)

			require.NoError(t, err)
			require.Nil(t, sess)
			require.Equal(t, tc.statusCode, rsp.StatusCode)
			require.Equal(t, "/target", rsp.Header.Get("Location"))
		})
	}
}

// TestDialer_AutomaticRedirectFollowing tests FollowRedirects(10) automatically follows redirects
func TestDialer_AutomaticRedirectFollowing(t *testing.T) {
	// Track redirect chain (protected by mutex for race safety)
	var (
		redirectsMu   sync.Mutex
		redirectsSeen []string
	)

	// Create handler with redirect chain: /start -> /redirect1 -> /redirect2 -> /final
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		redirectsMu.Lock()
		redirectsSeen = append(redirectsSeen, "/start")
		redirectsMu.Unlock()
		w.Header().Set("Location", "/redirect1")
		w.WriteHeader(http.StatusFound) // 302
	})
	mux.HandleFunc("/redirect1", func(w http.ResponseWriter, r *http.Request) {
		redirectsMu.Lock()
		redirectsSeen = append(redirectsSeen, "/redirect1")
		redirectsMu.Unlock()
		w.Header().Set("Location", "/redirect2")
		w.WriteHeader(http.StatusTemporaryRedirect) // 307
	})
	mux.HandleFunc("/redirect2", func(w http.ResponseWriter, r *http.Request) {
		redirectsMu.Lock()
		redirectsSeen = append(redirectsSeen, "/redirect2")
		redirectsMu.Unlock()
		w.Header().Set("Location", "/final")
		w.WriteHeader(http.StatusMovedPermanently) // 301
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
		redirectsMu.Lock()
		redirectsSeen = append(redirectsSeen, "/final")
		redirectsMu.Unlock()
		// Final endpoint returns error (no WebTransport setup)
		// We just want to verify redirects were followed
		w.WriteHeader(http.StatusBadRequest)
	})

	addr, closeServer := runRedirectServer(t, mux)
	defer closeServer()

	// Create dialer with automatic redirect following
	d := &webtransport.Dialer{
		TLSClientConfig: &tls.Config{RootCAs: webtransport.CertPool},
		QUICConfig:      &quic.Config{Tracer: qlog.DefaultConnectionTracer, EnableDatagrams: true},
		CheckRedirect:   webtransport.FollowRedirects(10),
	}
	defer d.Close()

	// Dial the initial URL that starts redirect chain
	url := fmt.Sprintf("https://localhost:%d/start", addr.Port)
	rsp, sess, err := d.Dial(context.Background(), url, nil)

	// Verify redirects were followed (we should see all 4 paths visited)
	require.Error(t, err) // Final endpoint returns 400, so we get an error
	require.Nil(t, sess)
	require.Equal(t, http.StatusBadRequest, rsp.StatusCode)

	redirectsMu.Lock()
	expected := []string{"/start", "/redirect1", "/redirect2", "/final"}
	require.Equal(t, expected, redirectsSeen)
	redirectsMu.Unlock()
}

// TestDialer_RedirectLimitExceeded tests that redirect limit is enforced
func TestDialer_RedirectLimitExceeded(t *testing.T) {
	// Create handler with 5 sequential redirects
	mux := http.NewServeMux()
	mux.HandleFunc("/redirect0", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/redirect1")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/redirect1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/redirect2")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/redirect2", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/redirect3")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/redirect3", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/redirect4")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/redirect4", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/final")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	addr, closeServer := runRedirectServer(t, mux)
	defer closeServer()

	// Create dialer with redirect limit of 3
	d := &webtransport.Dialer{
		TLSClientConfig: &tls.Config{RootCAs: webtransport.CertPool},
		QUICConfig:      &quic.Config{Tracer: qlog.DefaultConnectionTracer, EnableDatagrams: true},
		CheckRedirect:   webtransport.FollowRedirects(3),
	}
	defer d.Close()

	url := fmt.Sprintf("https://localhost:%d/redirect0", addr.Port)
	rsp, sess, err := d.Dial(context.Background(), url, nil)

	// Verify we got TooManyRedirectsError
	require.Error(t, err)
	var tooMany *webtransport.TooManyRedirectsError
	require.ErrorAs(t, err, &tooMany)
	require.Equal(t, 3, tooMany.Limit)
	require.NotNil(t, rsp) // Response should be returned with error
	require.Nil(t, sess)   // No session
}

// TestDialer_UnlimitedRedirects tests FollowRedirects(0) follows many redirects
func TestDialer_UnlimitedRedirects(t *testing.T) {
	// Create handler with 20 redirects
	const numRedirects = 20
	redirectCount := 0
	mux := http.NewServeMux()

	for i := 0; i < numRedirects; i++ {
		path := fmt.Sprintf("/redirect%d", i)
		nextPath := fmt.Sprintf("/redirect%d", i+1)
		if i == numRedirects-1 {
			nextPath = "/final"
		}
		// Capture nextPath in closure
		nextPathCopy := nextPath
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			redirectCount++
			w.Header().Set("Location", nextPathCopy)
			w.WriteHeader(http.StatusFound)
		})
	}
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
		redirectCount++
		w.WriteHeader(http.StatusBadRequest) // Return error since we can't do full WebTransport
	})

	addr, closeServer := runRedirectServer(t, mux)
	defer closeServer()

	// Create dialer with unlimited redirects
	d := &webtransport.Dialer{
		TLSClientConfig: &tls.Config{RootCAs: webtransport.CertPool},
		QUICConfig:      &quic.Config{Tracer: qlog.DefaultConnectionTracer, EnableDatagrams: true},
		CheckRedirect:   webtransport.FollowRedirects(0), // unlimited
	}
	defer d.Close()

	url := fmt.Sprintf("https://localhost:%d/redirect0", addr.Port)
	rsp, sess, err := d.Dial(context.Background(), url, nil)

	// Verify we successfully followed all redirects (21 = 20 redirects + 1 final)
	require.Error(t, err) // Final returns 400
	require.Nil(t, sess)
	require.Equal(t, http.StatusBadRequest, rsp.StatusCode)
	require.Equal(t, 21, redirectCount)
}

// TestDialer_ErrUseLastResponse tests CheckRedirect returning ErrUseLastResponse
func TestDialer_ErrUseLastResponse(t *testing.T) {
	// Create handler with redirect chain
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/final")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	addr, closeServer := runRedirectServer(t, mux)
	defer closeServer()

	// Create dialer with CheckRedirect that returns ErrUseLastResponse
	d := &webtransport.Dialer{
		TLSClientConfig: &tls.Config{RootCAs: webtransport.CertPool},
		QUICConfig:      &quic.Config{Tracer: qlog.DefaultConnectionTracer, EnableDatagrams: true},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Stop at first redirect and return the redirect response
			return webtransport.ErrUseLastResponse
		},
	}
	defer d.Close()

	url := fmt.Sprintf("https://localhost:%d/start", addr.Port)
	rsp, sess, err := d.Dial(context.Background(), url, nil)

	// Verify we got the redirect response without error
	require.NoError(t, err)
	require.Nil(t, sess) // No session for redirect
	require.Equal(t, http.StatusFound, rsp.StatusCode)
	require.Equal(t, "/final", rsp.Header.Get("Location"))
}

// TestDialer_CustomCheckRedirect_Block tests custom callback blocking redirects
func TestDialer_CustomCheckRedirect_Block(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/blocked")
		w.WriteHeader(http.StatusFound)
	})

	addr, closeServer := runRedirectServer(t, mux)
	defer closeServer()

	d := &webtransport.Dialer{
		TLSClientConfig: &tls.Config{RootCAs: webtransport.CertPool},
		QUICConfig:      &quic.Config{Tracer: qlog.DefaultConnectionTracer, EnableDatagrams: true},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Block all redirects
			return fmt.Errorf("custom policy: redirects not allowed")
		},
	}
	defer d.Close()

	url := fmt.Sprintf("https://localhost:%d/start", addr.Port)
	rsp, sess, err := d.Dial(context.Background(), url, nil)

	// Verify redirect was blocked
	require.Error(t, err)
	require.Contains(t, err.Error(), "redirects not allowed")
	require.NotNil(t, rsp)
	require.Nil(t, sess)
	require.Equal(t, http.StatusFound, rsp.StatusCode)
}

// TestDialer_CustomCheckRedirect_Allow tests custom callback allowing redirects
func TestDialer_CustomCheckRedirect_Allow(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/allowed")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/allowed", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest) // Final response
	})

	addr, closeServer := runRedirectServer(t, mux)
	defer closeServer()

	redirectCount := 0
	d := &webtransport.Dialer{
		TLSClientConfig: &tls.Config{RootCAs: webtransport.CertPool},
		QUICConfig:      &quic.Config{Tracer: qlog.DefaultConnectionTracer, EnableDatagrams: true},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			redirectCount++
			// Allow redirects with limit
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
	defer d.Close()

	url := fmt.Sprintf("https://localhost:%d/start", addr.Port)
	rsp, sess, err := d.Dial(context.Background(), url, nil)

	// Verify redirect was followed
	require.Error(t, err) // Final endpoint returns 400
	require.Equal(t, 1, redirectCount)
	require.NotNil(t, rsp)
	require.Nil(t, sess)
	require.Equal(t, http.StatusBadRequest, rsp.StatusCode)
}

// TestDialer_CustomCheckRedirect_ViaChain tests via chain contents and ordering
func TestDialer_CustomCheckRedirect_ViaChain(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/step1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/step2")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/step2", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/step3")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/step3", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})

	addr, closeServer := runRedirectServer(t, mux)
	defer closeServer()

	var viaChains [][]*http.Request
	d := &webtransport.Dialer{
		TLSClientConfig: &tls.Config{RootCAs: webtransport.CertPool},
		QUICConfig:      &quic.Config{Tracer: qlog.DefaultConnectionTracer, EnableDatagrams: true},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Capture via chain at each redirect
			viaCopy := make([]*http.Request, len(via))
			copy(viaCopy, via)
			viaChains = append(viaChains, viaCopy)
			return nil // Allow redirect
		},
	}
	defer d.Close()

	url := fmt.Sprintf("https://localhost:%d/step1", addr.Port)
	_, _, _ = d.Dial(context.Background(), url, nil)

	// Verify via chain grew correctly
	require.Equal(t, 2, len(viaChains))                     // Called twice (step1->step2, step2->step3)
	require.Equal(t, 0, len(viaChains[0]))                  // First redirect: empty via
	require.Equal(t, 1, len(viaChains[1]))                  // Second redirect: 1 request in via
	require.Contains(t, viaChains[1][0].URL.Path, "/step1") // Via contains step1
}

// TestDialer_CustomCheckRedirect_SameOrigin tests same-origin policy example
func TestDialer_CustomCheckRedirect_SameOrigin(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/internal", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/other")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/other", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})

	addr, closeServer := runRedirectServer(t, mux)
	defer closeServer()

	originalHost := fmt.Sprintf("localhost:%d", addr.Port)
	d := &webtransport.Dialer{
		TLSClientConfig: &tls.Config{RootCAs: webtransport.CertPool},
		QUICConfig:      &quic.Config{Tracer: qlog.DefaultConnectionTracer, EnableDatagrams: true},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Same-origin policy
			if req.URL.Host != originalHost {
				return fmt.Errorf("redirect to different host not allowed: %s", req.URL.Host)
			}
			if len(via) >= 10 {
				return &webtransport.TooManyRedirectsError{Limit: 10, Via: via}
			}
			return nil
		},
	}
	defer d.Close()

	// This should work (same origin)
	url := fmt.Sprintf("https://localhost:%d/internal", addr.Port)
	rsp, sess, err := d.Dial(context.Background(), url, nil)

	require.Error(t, err) // Final returns 400
	require.NotNil(t, rsp)
	require.Nil(t, sess)
	require.Equal(t, http.StatusBadRequest, rsp.StatusCode)
}

// TestDialer_ContextCancellation tests context timeout during redirect chain
func TestDialer_ContextCancellation(t *testing.T) {
	mux := http.NewServeMux()
	redirectCount := 0
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		redirectCount++
		w.Header().Set("Location", "/slow")
		w.WriteHeader(http.StatusFound)
	})

	addr, closeServer := runRedirectServer(t, mux)
	defer closeServer()

	d := &webtransport.Dialer{
		TLSClientConfig: &tls.Config{RootCAs: webtransport.CertPool},
		QUICConfig:      &quic.Config{Tracer: qlog.DefaultConnectionTracer, EnableDatagrams: true},
		CheckRedirect:   webtransport.FollowRedirects(100), // High limit
	}
	defer d.Close()

	// Use very short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	url := fmt.Sprintf("https://localhost:%d/slow", addr.Port)
	rsp, sess, err := d.Dial(ctx, url, nil)

	// Verify context cancellation stopped redirect following
	require.Error(t, err)
	require.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled),
		"expected context error, got: %v", err)
	require.True(t, redirectCount < 100, "should have stopped before reaching redirect limit")
	if rsp != nil {
		// May have response if cancellation happened during redirect
		require.Nil(t, sess)
	}
}
