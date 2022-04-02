package webtransport_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/marten-seemann/webtransport-go"

	"github.com/lucas-clemente/quic-go/http3"
	"github.com/lucas-clemente/quic-go/quicvarint"

	"github.com/stretchr/testify/require"
)

func TestUpgradeFailures(t *testing.T) {
	var s webtransport.Server

	t.Run("wrong request method", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/webtransport", nil)
		_, err := s.Upgrade(httptest.NewRecorder(), req)
		require.EqualError(t, err, "expected CONNECT request, got GET")
	})

	t.Run("wrong protocol", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodConnect, "/webtransport", nil)
		_, err := s.Upgrade(httptest.NewRecorder(), req)
		require.EqualError(t, err, "unexpected protocol: HTTP/1.1")
	})

	t.Run("missing WebTransport header", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodConnect, "/webtransport", nil)
		req.Proto = "webtransport"
		_, err := s.Upgrade(httptest.NewRecorder(), req)
		require.EqualError(t, err, "missing or invalid Sec-Webtransport-Http3-Draft02 header")
	})
}

func newWebTransportRequest(t *testing.T, addr string) *http.Request {
	t.Helper()
	u, err := url.Parse(addr)
	require.NoError(t, err)
	hdr := make(http.Header)
	hdr.Add("Sec-Webtransport-Http3-Draft02", "1")
	hdr.Add("Origin", "webtransport-go test")
	return &http.Request{
		Method: http.MethodConnect,
		Header: hdr,
		Proto:  "webtransport",
		Host:   u.Host,
		URL:    u,
	}
}

func TestReorderedUpgradeRequest(t *testing.T) {
	tlsConf, _ := getTLSConf(t)
	s := webtransport.Server{
		H3: http3.Server{
			Server: &http.Server{TLSConfig: tlsConf},
		},
	}
	connChan := make(chan *webtransport.Conn)
	http.HandleFunc("/webtransport", func(w http.ResponseWriter, r *http.Request) {
		conn, err := s.Upgrade(w, r)
		if err != nil {
			t.Logf("upgrading failed: %s", err)
			w.WriteHeader(500)
			return
		}
		connChan <- conn
	})

	udpConn, err := net.ListenUDP("udp", nil)
	require.NoError(t, err)
	port := udpConn.LocalAddr().(*net.UDPAddr).Port
	go func() {
		require.NoError(t, s.Serve(udpConn))
	}()

	var rt http3.RoundTripper
	rt.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	// This sends a a request, so that we can hijack the connection. Stream ID: 0.
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("https://localhost:%d/", port), nil)
	require.NoError(t, err)
	rsp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	qconn := rsp.Body.(http3.Hijacker).StreamCreator()
	// Open a new stream for a WebTransport session we'll establish later. Stream ID: 4.
	str, err := qconn.OpenStream()
	require.NoError(t, err)
	buf := &bytes.Buffer{}
	quicvarint.Write(buf, 0x41)
	quicvarint.Write(buf, 8) // stream ID of the stream used to establish the WebTransport session.
	buf.Write([]byte("foobar"))
	_, err = str.Write(buf.Bytes())
	require.NoError(t, err)
	require.NoError(t, str.Close())

	rsp, err = rt.RoundTrip(newWebTransportRequest(t, fmt.Sprintf("https://localhost:%d/webtransport", port)))
	require.NoError(t, err)
	require.Equal(t, 200, rsp.StatusCode)
	sconn := <-connChan
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	sstr, err := sconn.AcceptStream(ctx)
	require.NoError(t, err)
	data, err := io.ReadAll(sstr)
	require.NoError(t, err)
	require.Equal(t, []byte("foobar"), data)
}
