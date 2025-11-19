package webtransport_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"testing"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/qlog"
	"github.com/quic-go/webtransport-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProtocolNegotiation_ClientOffersServerSelects tests the basic protocol negotiation
// where client offers protocols and server selects one.
// This verifies RFC Section 3.3 acceptance scenario 1.
func TestProtocolNegotiation_ClientOffersServerSelects(t *testing.T) {
	serverProtocol := ""
	clientProtocol := ""

	// Setup server with protocol selection callback
	s := &webtransport.Server{
		SelectProtocol: func(available []string) string {
			// Server prefers v1 over v2
			for _, proto := range []string{"v1", "v2"} {
				for _, avail := range available {
					if proto == avail {
						return proto
					}
				}
			}
			return ""
		},
	}
	s.H3.TLSConfig = webtransport.TLSConf
	s.H3.QUICConfig = &quic.Config{Tracer: qlog.DefaultConnectionTracer, EnableDatagrams: true}

	mux := http.NewServeMux()
	mux.HandleFunc("/webtransport", func(w http.ResponseWriter, r *http.Request) {
		sess, err := s.Upgrade(w, r)
		if err != nil {
			t.Logf("upgrading failed: %s", err)
			w.WriteHeader(500)
			return
		}
		serverProtocol = sess.Protocol()
		sess.CloseWithError(0, "")
	})
	s.H3.Handler = mux

	addr, closeServer := runServer(t, s)
	defer closeServer()

	// Setup client with available protocols
	d := webtransport.Dialer{
		TLSClientConfig:    &tls.Config{RootCAs: webtransport.CertPool},
		QUICConfig:         &quic.Config{Tracer: qlog.DefaultConnectionTracer, EnableDatagrams: true},
		AvailableProtocols: []string{"v2", "v1"}, // Client prefers v2
	}
	defer d.Close()

	url := fmt.Sprintf("https://localhost:%d/webtransport", addr.Port)
	rsp, sess, err := d.Dial(context.Background(), url, nil)
	require.NoError(t, err)
	require.Equal(t, 200, rsp.StatusCode)
	defer sess.CloseWithError(0, "")

	clientProtocol = sess.Protocol()

	// Server should select v1 (its preference) even though client prefers v2
	assert.Equal(t, "v1", serverProtocol, "server should see negotiated protocol")
	assert.Equal(t, "v1", clientProtocol, "client should see negotiated protocol")
}

// TestProtocolNegotiation_NoMatch tests that the server rejects when no protocol matches.
// This verifies RFC Section 3.3 acceptance scenario 2.
func TestProtocolNegotiation_NoMatch(t *testing.T) {
	// Setup server with protocol selection callback that requires v1
	s := &webtransport.Server{
		SelectProtocol: func(available []string) string {
			for _, proto := range available {
				if proto == "v1" {
					return "v1"
				}
			}
			return "" // Reject if v1 not available
		},
	}
	s.H3.TLSConfig = webtransport.TLSConf
	s.H3.QUICConfig = &quic.Config{Tracer: qlog.DefaultConnectionTracer, EnableDatagrams: true}

	mux := http.NewServeMux()
	mux.HandleFunc("/webtransport", func(w http.ResponseWriter, r *http.Request) {
		_, err := s.Upgrade(w, r)
		if err != nil {
			// Expected error
			w.WriteHeader(400)
			return
		}
		t.Fatal("upgrade should have failed")
	})
	s.H3.Handler = mux

	addr, closeServer := runServer(t, s)
	defer closeServer()

	// Setup client with only v2 (no match with server's v1 requirement)
	d := webtransport.Dialer{
		TLSClientConfig:    &tls.Config{RootCAs: webtransport.CertPool},
		QUICConfig:         &quic.Config{Tracer: qlog.DefaultConnectionTracer, EnableDatagrams: true},
		AvailableProtocols: []string{"v2"},
	}
	defer d.Close()

	url := fmt.Sprintf("https://localhost:%d/webtransport", addr.Port)
	rsp, sess, err := d.Dial(context.Background(), url, nil)

	// Should get error or non-200 status
	if err == nil {
		defer sess.CloseWithError(0, "")
		assert.NotEqual(t, 200, rsp.StatusCode, "should not get 200 status when protocol doesn't match")
	} else {
		// Error is also acceptable
		t.Logf("dial failed as expected: %v", err)
	}
}

// TestProtocolNegotiation_NoProtocols tests session establishment without protocol negotiation.
func TestProtocolNegotiation_NoProtocols(t *testing.T) {
	serverProtocol := ""
	clientProtocol := ""

	// Setup server without protocol selection callback
	s := &webtransport.Server{
		// No SelectProtocol callback
	}
	s.H3.TLSConfig = webtransport.TLSConf
	s.H3.QUICConfig = &quic.Config{Tracer: qlog.DefaultConnectionTracer, EnableDatagrams: true}

	mux := http.NewServeMux()
	mux.HandleFunc("/webtransport", func(w http.ResponseWriter, r *http.Request) {
		sess, err := s.Upgrade(w, r)
		if err != nil {
			t.Logf("upgrading failed: %s", err)
			w.WriteHeader(500)
			return
		}
		serverProtocol = sess.Protocol()
		sess.CloseWithError(0, "")
	})
	s.H3.Handler = mux

	addr, closeServer := runServer(t, s)
	defer closeServer()

	// Setup client without available protocols
	d := webtransport.Dialer{
		TLSClientConfig: &tls.Config{RootCAs: webtransport.CertPool},
		QUICConfig:      &quic.Config{Tracer: qlog.DefaultConnectionTracer, EnableDatagrams: true},
		// No AvailableProtocols
	}
	defer d.Close()

	url := fmt.Sprintf("https://localhost:%d/webtransport", addr.Port)
	rsp, sess, err := d.Dial(context.Background(), url, nil)
	require.NoError(t, err)
	require.Equal(t, 200, rsp.StatusCode)
	defer sess.CloseWithError(0, "")

	clientProtocol = sess.Protocol()

	// No protocol should be negotiated
	assert.Equal(t, "", serverProtocol, "server should have empty protocol when none negotiated")
	assert.Equal(t, "", clientProtocol, "client should have empty protocol when none negotiated")
}

// TestProtocolNegotiation_ClientOffersServerIgnores tests that session works
// when client offers protocols but server doesn't have SelectProtocol callback.
func TestProtocolNegotiation_ClientOffersServerIgnores(t *testing.T) {
	serverProtocol := ""
	clientProtocol := ""

	// Setup server without protocol selection callback
	s := &webtransport.Server{
		// No SelectProtocol callback - server ignores client's protocols
	}
	s.H3.TLSConfig = webtransport.TLSConf
	s.H3.QUICConfig = &quic.Config{Tracer: qlog.DefaultConnectionTracer, EnableDatagrams: true}

	mux := http.NewServeMux()
	mux.HandleFunc("/webtransport", func(w http.ResponseWriter, r *http.Request) {
		sess, err := s.Upgrade(w, r)
		if err != nil {
			t.Logf("upgrading failed: %s", err)
			w.WriteHeader(500)
			return
		}
		serverProtocol = sess.Protocol()
		sess.CloseWithError(0, "")
	})
	s.H3.Handler = mux

	addr, closeServer := runServer(t, s)
	defer closeServer()

	// Setup client with available protocols
	d := webtransport.Dialer{
		TLSClientConfig:    &tls.Config{RootCAs: webtransport.CertPool},
		QUICConfig:         &quic.Config{Tracer: qlog.DefaultConnectionTracer, EnableDatagrams: true},
		AvailableProtocols: []string{"v2", "v1"},
	}
	defer d.Close()

	url := fmt.Sprintf("https://localhost:%d/webtransport", addr.Port)
	rsp, sess, err := d.Dial(context.Background(), url, nil)
	require.NoError(t, err)
	require.Equal(t, 200, rsp.StatusCode)
	defer sess.CloseWithError(0, "")

	clientProtocol = sess.Protocol()

	// No protocol should be negotiated (server didn't respond with WT-Protocol header)
	assert.Equal(t, "", serverProtocol, "server should have empty protocol when not negotiating")
	assert.Equal(t, "", clientProtocol, "client should have empty protocol when server doesn't negotiate")
}

// TestProtocolNegotiation_MultipleProtocols tests negotiation with several protocols.
func TestProtocolNegotiation_MultipleProtocols(t *testing.T) {
	serverProtocol := ""
	clientProtocol := ""

	// Setup server with protocol selection callback
	s := &webtransport.Server{
		SelectProtocol: func(available []string) string {
			// Server supports v3, v2, v1 in that preference order
			for _, proto := range []string{"v3", "v2", "v1"} {
				for _, avail := range available {
					if proto == avail {
						return proto
					}
				}
			}
			return ""
		},
	}
	s.H3.TLSConfig = webtransport.TLSConf
	s.H3.QUICConfig = &quic.Config{Tracer: qlog.DefaultConnectionTracer, EnableDatagrams: true}

	mux := http.NewServeMux()
	mux.HandleFunc("/webtransport", func(w http.ResponseWriter, r *http.Request) {
		sess, err := s.Upgrade(w, r)
		if err != nil {
			t.Logf("upgrading failed: %s", err)
			w.WriteHeader(500)
			return
		}
		serverProtocol = sess.Protocol()
		sess.CloseWithError(0, "")
	})
	s.H3.Handler = mux

	addr, closeServer := runServer(t, s)
	defer closeServer()

	// Setup client - offers v4, v2, v1 (doesn't offer v3)
	d := webtransport.Dialer{
		TLSClientConfig:    &tls.Config{RootCAs: webtransport.CertPool},
		QUICConfig:         &quic.Config{Tracer: qlog.DefaultConnectionTracer, EnableDatagrams: true},
		AvailableProtocols: []string{"v4", "v2", "v1"},
	}
	defer d.Close()

	url := fmt.Sprintf("https://localhost:%d/webtransport", addr.Port)
	rsp, sess, err := d.Dial(context.Background(), url, nil)
	require.NoError(t, err)
	require.Equal(t, 200, rsp.StatusCode)
	defer sess.CloseWithError(0, "")

	clientProtocol = sess.Protocol()

	// Server should select v2 (first common protocol in server's preference order)
	assert.Equal(t, "v2", serverProtocol, "server should see negotiated protocol v2")
	assert.Equal(t, "v2", clientProtocol, "client should see negotiated protocol v2")
}
