package webtransport_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/quic-go/webtransport-go"

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
}

//nolint:unparam
func createStreamAndWrite(t *testing.T, conn *quic.Conn, sessionID uint64, data []byte) *quic.Stream {
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
		H3: &http3.Server{TLSConfig: webtransport.TLSConf},
	}
	defer s.Close()
	connChan := make(chan *webtransport.Session)
	addHandler(t, &s, func(c *webtransport.Session) {
		connChan <- c
	})

	udpConn, err := net.ListenUDP("udp", nil)
	require.NoError(t, err)
	port := udpConn.LocalAddr().(*net.UDPAddr).Port
	webtransport.ConfigureHTTP3Server(s.H3)
	go s.Serve(udpConn)

	cconn, err := quic.DialAddr(
		context.Background(),
		fmt.Sprintf("localhost:%d", port),
		&tls.Config{RootCAs: webtransport.CertPool, NextProtos: []string{http3.NextProtoH3}},
		&quic.Config{EnableDatagrams: true, EnableStreamResetPartialDelivery: true},
	)
	require.NoError(t, err)
	// Open a new stream for a WebTransport session we'll establish later. Stream ID: 0.
	createStreamAndWrite(t, cconn, 4, []byte("foobar"))
	tr := &http3.Transport{EnableDatagrams: true}
	conn := tr.NewClientConn(cconn)

	// make sure this request actually arrives first
	time.Sleep(scaleDuration(50 * time.Millisecond))

	// Create a new WebTransport session. Stream ID: 4.
	str, err := conn.OpenRequestStream(context.Background())
	require.NoError(t, err)
	require.NoError(t, str.SendRequestHeader(
		webtransport.NewWebTransportRequest(t, fmt.Sprintf("https://localhost:%d/webtransport", port)),
	))
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
		H3:                &http3.Server{TLSConfig: webtransport.TLSConf, EnableDatagrams: true},
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
	webtransport.ConfigureHTTP3Server(s.H3)
	go s.Serve(udpConn)

	cconn, err := quic.DialAddr(
		context.Background(),
		fmt.Sprintf("localhost:%d", port),
		&tls.Config{RootCAs: webtransport.CertPool, NextProtos: []string{http3.NextProtoH3}},
		&quic.Config{EnableDatagrams: true},
	)
	require.NoError(t, err)

	// Open a new stream for a WebTransport session we'll establish later. Stream ID: 0.
	str := createStreamAndWrite(t, cconn, 4, []byte("foobar"))

	time.Sleep(2 * timeout)

	tr := &http3.Transport{EnableDatagrams: true}
	conn := tr.NewClientConn(cconn)

	// Reordering was too long. The stream should now have been reset by the server.
	_, err = str.Read([]byte{0})
	var streamErr *quic.StreamError
	require.ErrorAs(t, err, &streamErr)
	require.Equal(t, webtransport.WTBufferedStreamRejectedErrorCode, streamErr.ErrorCode)

	// Now establish the session. Make sure we don't accept the stream.
	requestStr, err := conn.OpenRequestStream(context.Background())
	require.NoError(t, err)
	require.NoError(t, requestStr.SendRequestHeader(
		webtransport.NewWebTransportRequest(t, fmt.Sprintf("https://localhost:%d/webtransport", port)),
	))
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

func TestServerSettingsCheck(t *testing.T) {
	timeout := scaleDuration(150 * time.Millisecond)
	s := webtransport.Server{
		H3:                &http3.Server{TLSConfig: webtransport.TLSConf, EnableDatagrams: true},
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
	webtransport.ConfigureHTTP3Server(s.H3)
	go s.Serve(udpConn)

	cconn, err := quic.DialAddr(
		context.Background(),
		fmt.Sprintf("localhost:%d", port),
		&tls.Config{RootCAs: webtransport.CertPool, NextProtos: []string{http3.NextProtoH3}},
		&quic.Config{EnableDatagrams: true},
	)
	require.NoError(t, err)
	tr := &http3.Transport{EnableDatagrams: false}
	conn := tr.NewClientConn(cconn)
	requestStr, err := conn.OpenRequestStream(context.Background())
	require.NoError(t, err)
	require.NoError(t, requestStr.SendRequestHeader(
		webtransport.NewWebTransportRequest(t, fmt.Sprintf("https://localhost:%d/webtransport", port)),
	))
	rsp, err := requestStr.ReadResponse()
	require.NoError(t, err)
	require.Equal(t, http.StatusNotImplemented, rsp.StatusCode)

	require.ErrorContains(t, <-errChan, "webtransport: missing datagram support")
}

func TestImmediateClose(t *testing.T) {
	s := webtransport.Server{H3: &http3.Server{}}
	require.NoError(t, s.Close())
}

func TestServerConnectionStateChecks(t *testing.T) {
	tests := []struct {
		name                     string
		enableDatagrams          bool
		enableStreamResetPartial bool
		wantErr                  string
	}{
		{
			name:                     "missing datagram support",
			enableDatagrams:          false,
			enableStreamResetPartial: true,
			wantErr:                  "webtransport: QUIC DATAGRAM support required",
		},
		{
			name:                     "missing stream reset partial delivery support",
			enableDatagrams:          true,
			enableStreamResetPartial: false,
			wantErr:                  "webtransport: QUIC Stream Resets with Partial Delivery required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := webtransport.Server{H3: &http3.Server{TLSConfig: webtransport.TLSConf}}
			defer s.Close()

			serverConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
			require.NoError(t, err)
			defer serverConn.Close()

			clientConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
			require.NoError(t, err)
			defer clientConn.Close()

			ln, err := quic.ListenEarly(serverConn, webtransport.TLSConf, &quic.Config{
				EnableDatagrams:                  tt.enableDatagrams,
				EnableStreamResetPartialDelivery: tt.enableStreamResetPartial,
			})
			require.NoError(t, err)
			defer ln.Close()

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err = quic.DialEarly(ctx, clientConn, ln.Addr(), &tls.Config{
				ServerName: "localhost",
				NextProtos: []string{http3.NextProtoH3},
				RootCAs:    webtransport.CertPool,
			}, &quic.Config{
				EnableDatagrams:                  tt.enableDatagrams,
				EnableStreamResetPartialDelivery: tt.enableStreamResetPartial,
			})
			require.NoError(t, err)

			qconn, err := ln.Accept(ctx)
			require.NoError(t, err)
			defer qconn.CloseWithError(0, "")

			require.ErrorContains(t, s.ServeQUICConn(qconn), tt.wantErr)
		})
	}
}
