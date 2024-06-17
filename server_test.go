package webtransport_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/mincho-artesoft/webtransport-go"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/quicvarint"

	"github.com/stretchr/testify/require"
)

const webTransportFrameType = 0x41

func scaleDuration(d time.Duration) time.Duration {
	if os.Getenv("CI") != "" {
		return 5 * d
	}
	return d
}

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
	return &http.Request{
		Method: http.MethodConnect,
		Header: hdr,
		Proto:  "webtransport",
		Host:   u.Host,
		URL:    u,
	}
}

func createStreamAndWrite(t *testing.T, conn quic.Connection, sessionID uint64, data []byte) quic.Stream {
	t.Helper()
	str, err := conn.OpenStream()
	require.NoError(t, err)
	var buf []byte
	buf = quicvarint.Append(buf, webTransportFrameType)
	buf = quicvarint.Append(buf, sessionID) // stream ID of the stream used to establish the WebTransport session.
	buf = append(buf, data...)
	_, err = str.Write(buf)
	require.NoError(t, err)
	require.NoError(t, str.Close())
	return str
}

func TestServerReorderedUpgradeRequest(t *testing.T) {
	s := webtransport.Server{
		H3: http3.Server{TLSConfig: tlsConf},
	}
	defer s.Close()
	connChan := make(chan *webtransport.Session)
	addHandler(t, &s, func(c *webtransport.Session) {
		connChan <- c
	})

	udpConn, err := net.ListenUDP("udp", nil)
	require.NoError(t, err)
	port := udpConn.LocalAddr().(*net.UDPAddr).Port
	go s.Serve(udpConn)

	cconn, err := quic.DialAddr(
		context.Background(),
		fmt.Sprintf("localhost:%d", port),
		&tls.Config{RootCAs: certPool, NextProtos: []string{http3.NextProtoH3}},
		&quic.Config{EnableDatagrams: true},
	)
	require.NoError(t, err)
	// Open a new stream for a WebTransport session we'll establish later. Stream ID: 0.
	createStreamAndWrite(t, cconn, 4, []byte("foobar"))
	rt := http3.SingleDestinationRoundTripper{
		Connection:      cconn,
		EnableDatagrams: true,
	}

	// make sure this request actually arrives first
	time.Sleep(scaleDuration(50 * time.Millisecond))

	// Create a new WebTransport session. Stream ID: 4.
	str, err := rt.OpenRequestStream(context.Background())
	require.NoError(t, err)
	require.NoError(t, str.SendRequestHeader(newWebTransportRequest(t, fmt.Sprintf("https://localhost:%d/webtransport", port))))
	rsp, err := str.ReadResponse()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rsp.StatusCode)
	sconn := <-connChan
	defer sconn.CloseWithError(0, "")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	sstr, err := sconn.AcceptStream(ctx)
	require.NoError(t, err)
	data, err := io.ReadAll(sstr)
	require.NoError(t, err)
	require.Equal(t, []byte("foobar"), data)

	// Establish another stream and make sure it's accepted now.
	createStreamAndWrite(t, cconn, 4, []byte("raboof"))
	ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	sstr, err = sconn.AcceptStream(ctx)
	require.NoError(t, err)
	data, err = io.ReadAll(sstr)
	require.NoError(t, err)
	require.Equal(t, []byte("raboof"), data)
}

func TestServerReorderedUpgradeRequestTimeout(t *testing.T) {
	timeout := scaleDuration(100 * time.Millisecond)
	s := webtransport.Server{
		H3:                http3.Server{TLSConfig: tlsConf, EnableDatagrams: true},
		ReorderingTimeout: timeout,
	}
	defer s.Close()
	connChan := make(chan *webtransport.Session)
	addHandler(t, &s, func(c *webtransport.Session) {
		connChan <- c
	})

	udpConn, err := net.ListenUDP("udp", nil)
	require.NoError(t, err)
	port := udpConn.LocalAddr().(*net.UDPAddr).Port
	go s.Serve(udpConn)

	cconn, err := quic.DialAddr(
		context.Background(),
		fmt.Sprintf("localhost:%d", port),
		&tls.Config{RootCAs: certPool, NextProtos: []string{http3.NextProtoH3}},
		&quic.Config{EnableDatagrams: true},
	)
	require.NoError(t, err)

	// Open a new stream for a WebTransport session we'll establish later. Stream ID: 0.
	str := createStreamAndWrite(t, cconn, 4, []byte("foobar"))

	time.Sleep(2 * timeout)

	rt := http3.SingleDestinationRoundTripper{
		Connection:      cconn,
		EnableDatagrams: true,
	}

	// Reordering was too long. The stream should now have been reset by the server.
	_, err = str.Read([]byte{0})
	var streamErr *quic.StreamError
	require.ErrorAs(t, err, &streamErr)
	require.Equal(t, webtransport.WebTransportBufferedStreamRejectedErrorCode, streamErr.ErrorCode)

	// Now establish the session. Make sure we don't accept the stream.
	requestStr, err := rt.OpenRequestStream(context.Background())
	require.NoError(t, err)
	require.NoError(t, requestStr.SendRequestHeader(newWebTransportRequest(t, fmt.Sprintf("https://localhost:%d/webtransport", port))))
	rsp, err := requestStr.ReadResponse()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rsp.StatusCode)
	sconn := <-connChan
	defer sconn.CloseWithError(0, "")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err = sconn.AcceptStream(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	// Establish another stream and make sure it's accepted now.
	createStreamAndWrite(t, cconn, 4, []byte("raboof"))
	ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	sstr, err := sconn.AcceptStream(ctx)
	require.NoError(t, err)
	data, err := io.ReadAll(sstr)
	require.NoError(t, err)
	require.Equal(t, []byte("raboof"), data)
}

func TestServerReorderedMultipleStreams(t *testing.T) {
	timeout := scaleDuration(150 * time.Millisecond)
	s := webtransport.Server{
		H3:                http3.Server{TLSConfig: tlsConf, EnableDatagrams: true},
		ReorderingTimeout: timeout,
	}
	defer s.Close()
	connChan := make(chan *webtransport.Session)
	addHandler(t, &s, func(c *webtransport.Session) {
		connChan <- c
	})

	udpConn, err := net.ListenUDP("udp", nil)
	require.NoError(t, err)
	port := udpConn.LocalAddr().(*net.UDPAddr).Port
	go s.Serve(udpConn)

	cconn, err := quic.DialAddr(
		context.Background(),
		fmt.Sprintf("localhost:%d", port),
		&tls.Config{RootCAs: certPool, NextProtos: []string{http3.NextProtoH3}},
		&quic.Config{EnableDatagrams: true},
	)
	require.NoError(t, err)
	start := time.Now()
	// Open a new stream for a WebTransport session we'll establish later. Stream ID: 0.
	str1 := createStreamAndWrite(t, cconn, 8, []byte("foobar"))

	// After a while, open another stream.
	time.Sleep(timeout / 2)
	// Open a new stream for a WebTransport session we'll establish later. Stream ID: 4.
	createStreamAndWrite(t, cconn, 8, []byte("raboof"))

	// Reordering was too long. The stream should now have been reset by the server.
	_, err = str1.Read([]byte{0})
	var streamErr *quic.StreamError
	require.ErrorAs(t, err, &streamErr)
	require.Equal(t, webtransport.WebTransportBufferedStreamRejectedErrorCode, streamErr.ErrorCode)
	took := time.Since(start)
	require.GreaterOrEqual(t, took, timeout)
	require.Less(t, took, timeout*5/4)

	rt := http3.SingleDestinationRoundTripper{
		Connection:      cconn,
		EnableDatagrams: true,
	}
	// Now establish the session. Make sure we don't accept the stream.
	requestStr, err := rt.OpenRequestStream(context.Background())
	require.NoError(t, err)
	require.NoError(t, requestStr.SendRequestHeader(newWebTransportRequest(t, fmt.Sprintf("https://localhost:%d/webtransport", port))))
	rsp, err := requestStr.ReadResponse()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rsp.StatusCode)
	sconn := <-connChan
	defer sconn.CloseWithError(0, "")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	sstr, err := sconn.AcceptStream(ctx)
	require.NoError(t, err)
	data, err := io.ReadAll(sstr)
	require.NoError(t, err)
	require.Equal(t, []byte("raboof"), data)
}

func TestServerSettingsCheck(t *testing.T) {
	timeout := scaleDuration(150 * time.Millisecond)
	s := webtransport.Server{
		H3:                http3.Server{TLSConfig: tlsConf, EnableDatagrams: true},
		ReorderingTimeout: timeout,
	}
	errChan := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/webtransport", func(w http.ResponseWriter, r *http.Request) {
		_, err := s.Upgrade(w, r)
		w.WriteHeader(http.StatusNotImplemented)
		errChan <- err
	})
	s.H3.Handler = mux
	udpConn, err := net.ListenUDP("udp", nil)
	require.NoError(t, err)
	port := udpConn.LocalAddr().(*net.UDPAddr).Port
	go s.Serve(udpConn)

	cconn, err := quic.DialAddr(
		context.Background(),
		fmt.Sprintf("localhost:%d", port),
		&tls.Config{RootCAs: certPool, NextProtos: []string{http3.NextProtoH3}},
		&quic.Config{EnableDatagrams: true},
	)
	require.NoError(t, err)
	rt := http3.SingleDestinationRoundTripper{Connection: cconn} // datagrams disabled
	requestStr, err := rt.OpenRequestStream(context.Background())
	require.NoError(t, err)
	require.NoError(t, requestStr.SendRequestHeader(newWebTransportRequest(t, fmt.Sprintf("https://localhost:%d/webtransport", port))))
	rsp, err := requestStr.ReadResponse()
	require.NoError(t, err)
	require.Equal(t, http.StatusNotImplemented, rsp.StatusCode)

	require.ErrorContains(t, <-errChan, "webtransport: missing datagram support")
}

func TestImmediateClose(t *testing.T) {
	s := webtransport.Server{H3: http3.Server{}}
	require.NoError(t, s.Close())
}
