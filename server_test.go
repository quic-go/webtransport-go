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

	"github.com/lucas-clemente/quic-go"
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

func createStreamAndWrite(t *testing.T, qconn http3.StreamCreator, sessionID uint64, data []byte) quic.Stream {
	t.Helper()
	str, err := qconn.OpenStream()
	require.NoError(t, err)
	buf := &bytes.Buffer{}
	quicvarint.Write(buf, 0x41)
	quicvarint.Write(buf, sessionID) // stream ID of the stream used to establish the WebTransport session.
	buf.Write(data)
	_, err = str.Write(buf.Bytes())
	require.NoError(t, err)
	require.NoError(t, str.Close())
	return str
}

func TestReorderedUpgradeRequest(t *testing.T) {
	tlsConf, _ := getTLSConf(t)
	s := webtransport.Server{
		H3: http3.Server{
			Server: &http.Server{TLSConfig: tlsConf},
		},
	}
	defer s.Close()
	connChan := make(chan *webtransport.Conn)
	addHandler(t, &s, func(c *webtransport.Conn) {
		connChan <- c
	})

	udpConn, err := net.ListenUDP("udp", nil)
	require.NoError(t, err)
	port := udpConn.LocalAddr().(*net.UDPAddr).Port
	go s.Serve(udpConn)

	var rt http3.RoundTripper
	rt.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	// This sends a request, so that we can hijack the connection. Stream ID: 0.
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("https://localhost:%d/", port), nil)
	require.NoError(t, err)
	rsp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	qconn := rsp.Body.(http3.Hijacker).StreamCreator()
	// Open a new stream for a WebTransport session we'll establish later. Stream ID: 4.
	createStreamAndWrite(t, qconn, 8, []byte("foobar"))

	// make sure this request actually arrives first
	time.Sleep(100 * time.Millisecond)

	rsp, err = rt.RoundTrip(newWebTransportRequest(t, fmt.Sprintf("https://localhost:%d/webtransport", port)))
	require.NoError(t, err)
	require.Equal(t, 200, rsp.StatusCode)
	sconn := <-connChan
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	sstr, err := sconn.AcceptStream(ctx)
	require.NoError(t, err)
	data, err := io.ReadAll(sstr)
	require.NoError(t, err)
	require.Equal(t, []byte("foobar"), data)

	// Establish another stream and make sure it's accepted now.
	createStreamAndWrite(t, qconn, 8, []byte("raboof"))
	ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	sstr, err = sconn.AcceptStream(ctx)
	require.NoError(t, err)
	data, err = io.ReadAll(sstr)
	require.NoError(t, err)
	require.Equal(t, []byte("raboof"), data)
}

func TestReorderedUpgradeRequestTimeout(t *testing.T) {
	const timeout = 100 * time.Millisecond
	tlsConf, _ := getTLSConf(t)
	s := webtransport.Server{
		H3: http3.Server{
			Server: &http.Server{TLSConfig: tlsConf},
		},
		StreamReorderingTimeout: timeout,
	}
	defer s.Close()
	connChan := make(chan *webtransport.Conn)
	addHandler(t, &s, func(c *webtransport.Conn) {
		connChan <- c
	})

	udpConn, err := net.ListenUDP("udp", nil)
	require.NoError(t, err)
	port := udpConn.LocalAddr().(*net.UDPAddr).Port
	go s.Serve(udpConn)

	var rt http3.RoundTripper
	rt.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	// This sends a request, so that we can hijack the connection. Stream ID: 0.
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("https://localhost:%d/", port), nil)
	require.NoError(t, err)
	rsp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	qconn := rsp.Body.(http3.Hijacker).StreamCreator()
	// Open a new stream for a WebTransport session we'll establish later. Stream ID: 4.
	str := createStreamAndWrite(t, qconn, 8, []byte("foobar"))

	time.Sleep(2 * timeout)

	// Reordering was too long. The stream should now have been reset by the server.
	_, err = str.Read([]byte{0})
	var streamErr *quic.StreamError
	require.ErrorAs(t, err, &streamErr)
	require.Equal(t, webtransport.WebTransportBufferedStreamRejectedErrorCode, streamErr.ErrorCode)

	// Now establish the session. Make sure we don't accept the stream.
	rsp, err = rt.RoundTrip(newWebTransportRequest(t, fmt.Sprintf("https://localhost:%d/webtransport", port)))
	require.NoError(t, err)
	require.Equal(t, 200, rsp.StatusCode)
	sconn := <-connChan
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err = sconn.AcceptStream(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	// Establish another stream and make sure it's accepted now.
	createStreamAndWrite(t, qconn, 8, []byte("raboof"))
	ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	sstr, err := sconn.AcceptStream(ctx)
	require.NoError(t, err)
	data, err := io.ReadAll(sstr)
	require.NoError(t, err)
	require.Equal(t, []byte("raboof"), data)
}

func TestReorderedMultipleStreams(t *testing.T) {
	const timeout = 300 * time.Millisecond
	tlsConf, _ := getTLSConf(t)
	s := webtransport.Server{
		H3: http3.Server{
			Server: &http.Server{TLSConfig: tlsConf},
		},
		StreamReorderingTimeout: timeout,
	}
	defer s.Close()
	connChan := make(chan *webtransport.Conn)
	addHandler(t, &s, func(c *webtransport.Conn) {
		connChan <- c
	})

	udpConn, err := net.ListenUDP("udp", nil)
	require.NoError(t, err)
	port := udpConn.LocalAddr().(*net.UDPAddr).Port
	go s.Serve(udpConn)

	var rt http3.RoundTripper
	rt.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	// This sends a request, so that we can hijack the connection. Stream ID: 0.
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("https://localhost:%d/", port), nil)
	require.NoError(t, err)
	rsp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	qconn := rsp.Body.(http3.Hijacker).StreamCreator()
	start := time.Now()
	// Open a new stream for a WebTransport session we'll establish later. Stream ID: 4.
	str1 := createStreamAndWrite(t, qconn, 12, []byte("foobar"))

	// After a while, open another stream.
	time.Sleep(timeout / 2)
	// Open a new stream for a WebTransport session we'll establish later. Stream ID: 8.
	str2 := createStreamAndWrite(t, qconn, 12, []byte("foobar"))

	// Reordering was too long. The stream should now have been reset by the server.
	_, err = str1.Read([]byte{0})
	var streamErr *quic.StreamError
	require.ErrorAs(t, err, &streamErr)
	require.Equal(t, webtransport.WebTransportBufferedStreamRejectedErrorCode, streamErr.ErrorCode)
	_, err = str2.Read([]byte{0})
	require.ErrorAs(t, err, &streamErr)
	require.Equal(t, webtransport.WebTransportBufferedStreamRejectedErrorCode, streamErr.ErrorCode)

	took := time.Since(start)
	require.GreaterOrEqual(t, took, timeout)
	require.Less(t, took, timeout*5/4)
}
