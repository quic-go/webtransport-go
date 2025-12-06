package webtransport

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/quicvarint"

	"github.com/stretchr/testify/require"
)

func scaleDuration(d time.Duration) time.Duration {
	if os.Getenv("CI") != "" {
		return 5 * d
	}
	return d
}

func streamHeaderBidirectional(sessionID uint64) []byte {
	hdr := quicvarint.Append(nil, webTransportFrameType)
	return quicvarint.Append(hdr, sessionID)
}

func streamHeaderUnidirectional(sessionID uint64) []byte {
	hdr := quicvarint.Append(nil, webTransportUniStreamType)
	return quicvarint.Append(hdr, sessionID)
}

func newUDPConnLocalhost(t testing.TB) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	return conn
}

func newConnPair(t *testing.T) (client, server *quic.Conn) {
	t.Helper()

	ln, err := quic.ListenEarly(
		newUDPConnLocalhost(t),
		TLSConf,
		&quic.Config{
			InitialStreamReceiveWindow:     1 << 60,
			InitialConnectionReceiveWindow: 1 << 60,
			EnableDatagrams:                true,
		},
	)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cl, err := quic.DialEarly(
		ctx,
		newUDPConnLocalhost(t),
		ln.Addr(),
		&tls.Config{
			ServerName: "localhost",
			NextProtos: []string{http3.NextProtoH3},
			RootCAs:    CertPool,
		},
		&quic.Config{EnableDatagrams: true},
	)
	require.NoError(t, err)
	require.True(t, cl.ConnectionState().SupportsDatagrams)
	t.Cleanup(func() { cl.CloseWithError(0, "") })

	conn, err := ln.Accept(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { conn.CloseWithError(0, "") })
	select {
	case <-conn.HandshakeComplete():
		require.True(t, conn.ConnectionState().SupportsDatagrams)
	case <-ctx.Done():
		t.Fatal("timeout")
	}
	return cl, conn
}

func setupSession(t *testing.T, clientConn, serverConn *quic.Conn, sessionID sessionID) *Session {
	var sess *Session
	tr := &http3.Transport{
		UniStreamHijacker: func(_ http3.StreamType, _ quic.ConnectionTracingID, str *quic.ReceiveStream, _ error) (hijacked bool) {
			sess.addIncomingUniStream(str)
			return true
		},
		StreamHijacker: func(_ http3.FrameType, _ quic.ConnectionTracingID, str *quic.Stream, _ error) (hijacked bool, err error) {
			sess.addIncomingStream(str)
			return true, nil
		},
	}

	serverAddr := startSimpleWebTransportServer(t, serverConn, &http3.Server{})
	reqStr, conn := setupRequestStr(t, tr, clientConn, serverAddr)
	sess = newSession(context.Background(), sessionID, conn.Conn(), reqStr, "")
	return sess
}

func startSimpleWebTransportServer(t *testing.T, serverConn *quic.Conn, server *http3.Server) (serverAddr string) {
	// TODO: use t.Context once we switch to Go 1.24
	testCtx, testCtxCancel := context.WithCancel(context.Background())

	mux := http.NewServeMux()
	mux.HandleFunc("/webtransport", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-testCtx.Done() // block until the test is done
	})
	server.Handler = mux
	t.Cleanup(func() {
		testCtxCancel()
		server.Close()
	})
	go server.ServeQUICConn(serverConn)
	return fmt.Sprintf("https://localhost:%d/webtransport", serverConn.LocalAddr().(*net.UDPAddr).Port)
}

func setupRequestStr(t *testing.T, tr *http3.Transport, clientConn *quic.Conn, serverAddr string) (*http3.RequestStream, *http3.ClientConn) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn := tr.NewClientConn(clientConn)
	reqStr, err := conn.OpenRequestStream(ctx)
	require.NoError(t, err)
	require.NoError(t, reqStr.SendRequestHeader(NewWebTransportRequest(t, serverAddr)))
	rsp, err := reqStr.ReadResponse()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rsp.StatusCode)

	return reqStr, conn
}

func TestAddStreamAfterSessionClose(t *testing.T) {
	const sessionID = 42
	clientConn, serverConn := newConnPair(t)
	sess := setupSession(t, clientConn, serverConn, sessionID)

	str1, err := serverConn.OpenStream()
	require.NoError(t, err)
	_, err = str1.Write(streamHeaderBidirectional(sessionID))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err = sess.AcceptStream(ctx)
	require.NoError(t, err)

	// now close the session
	require.NoError(t, sess.CloseWithError(0, ""))

	// we expect further streams to be reset
	str2, err := serverConn.OpenStream()
	require.NoError(t, err)
	_, err = str2.Write(streamHeaderBidirectional(sessionID))
	require.NoError(t, err)
	select {
	case <-str2.Context().Done():
		require.ErrorIs(t,
			context.Cause(str2.Context()),
			&quic.StreamError{Remote: true, StreamID: str2.StreamID(), ErrorCode: sessionCloseErrorCode},
		)
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}

	ustr, err := serverConn.OpenUniStream()
	require.NoError(t, err)
	_, err = ustr.Write(streamHeaderUnidirectional(sessionID))
	require.NoError(t, err)
	select {
	case <-ustr.Context().Done():
		require.ErrorIs(t,
			context.Cause(ustr.Context()),
			&quic.StreamError{Remote: true, StreamID: ustr.StreamID(), ErrorCode: sessionCloseErrorCode},
		)
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestOpenStreamSyncSessionClose(t *testing.T) {
	t.Run("bidirectional", func(t *testing.T) {
		testOpenStreamSyncSessionClose(
			t,
			func(s *Session) error { _, err := s.OpenStream(); return err },
			func(s *Session) error { _, err := s.OpenStreamSync(context.Background()); return err },
		)
	})
	t.Run("unidirectional", func(t *testing.T) {
		testOpenStreamSyncSessionClose(
			t,
			func(s *Session) error { _, err := s.OpenUniStream(); return err },
			func(s *Session) error { _, err := s.OpenUniStreamSync(context.Background()); return err },
		)
	})
}

func testOpenStreamSyncSessionClose(t *testing.T, openStream func(*Session) error, openStreamSync func(*Session) error) {
	const sessionID = 42
	clientConn, serverConn := newConnPair(t)
	sess := setupSession(t, clientConn, serverConn, sessionID)

	for {
		if err := openStream(sess); err != nil {
			break
		}
	}

	errChan := make(chan error, 1)
	go func() {
		err := openStreamSync(sess)
		errChan <- err
	}()

	select {
	case <-errChan:
		t.Fatal("should not have opened stream")
	case <-time.After(scaleDuration(10 * time.Millisecond)):
	}
	require.NoError(t, sess.CloseWithError(1337, "closing"))

	select {
	case err := <-errChan:
		var serr *SessionError
		require.ErrorAs(t, err, &serr)
		require.False(t, serr.Remote)
		require.Equal(t, SessionErrorCode(1337), serr.ErrorCode)
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

type mockConn struct {
	http3Conn
	hijackMagicNumber   uint64
	blockOpenStreamSync chan struct{}
}

var _ http3Conn = &mockConn{}

func (c *mockConn) OpenStreamSync(ctx context.Context) (*quic.Stream, error) {
	str, err := c.http3Conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	b := quicvarint.Append(nil, c.hijackMagicNumber)
	if _, err := str.Write(b); err != nil {
		panic(err)
	}
	<-c.blockOpenStreamSync
	return str, nil
}

func (c *mockConn) OpenUniStreamSync(ctx context.Context) (*quic.SendStream, error) {
	str, err := c.http3Conn.OpenUniStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	b := quicvarint.Append(nil, c.hijackMagicNumber)
	if _, err := str.Write(b); err != nil {
		panic(err)
	}
	<-c.blockOpenStreamSync
	return str, nil
}

func TestOpenStreamSyncAfterSessionClose(t *testing.T) {
	t.Run("bidirectional", func(t *testing.T) {
		testOpenStreamSyncAfterSessionClose(t, true)
	})
	t.Run("unidirectional", func(t *testing.T) {
		testOpenStreamSyncAfterSessionClose(t, false)
	})
}

func testOpenStreamSyncAfterSessionClose(t *testing.T, bidirectional bool) {
	type testStream interface {
		StreamID() quic.StreamID
		io.Reader
		SetReadDeadline(time.Time) error
	}

	const magicNumber = 42
	clientConn, serverConn := newConnPair(t)
	serverStrChan := make(chan testStream, 1)
	server := &http3.Server{}
	switch bidirectional {
	case true:
		server.StreamHijacker = func(ft http3.FrameType, connTracingID quic.ConnectionTracingID, str *quic.Stream, err error) (bool /* hijacked */, error) {
			if ft == magicNumber {
				serverStrChan <- str
				return true, nil
			}
			return false, nil
		}
	case false:
		server.UniStreamHijacker = func(ft http3.StreamType, connTracingID quic.ConnectionTracingID, str *quic.ReceiveStream, err error) bool /* hijacked */ {
			if ft == magicNumber {
				serverStrChan <- str
				return true
			}
			return false
		}
	}

	serverAddr := startSimpleWebTransportServer(t, serverConn, server)
	reqStr, conn := setupRequestStr(t, &http3.Transport{}, clientConn, serverAddr)
	unblockOpenStreamSync := make(chan struct{})
	mockConn := &mockConn{
		http3Conn:           conn.Conn(),
		hijackMagicNumber:   magicNumber,
		blockOpenStreamSync: unblockOpenStreamSync,
	}
	sess := newSession(context.Background(), magicNumber, mockConn, reqStr, "")

	errChan := make(chan error, 1)
	switch bidirectional {
	case true:
		go func() {
			_, err := sess.OpenStreamSync(context.Background())
			errChan <- err
		}()
	case false:
		go func() {
			_, err := sess.OpenUniStreamSync(context.Background())
			errChan <- err
		}()
	}

	var str testStream
	select {
	case str = <-serverStrChan:
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}

	// test that the stream was not yet reset
	str.SetReadDeadline(time.Now().Add(scaleDuration(10 * time.Millisecond)))
	_, err := str.Read([]byte{0})
	require.ErrorIs(t, err, os.ErrDeadlineExceeded)

	select {
	case <-errChan:
		t.Fatal("should not have opened stream on the client side")
	case <-time.After(scaleDuration(10 * time.Millisecond)):
	}
	require.NoError(t, sess.CloseWithError(1337, "closing"))
	close(unblockOpenStreamSync)

	select {
	case err := <-errChan:
		var serr *SessionError
		require.ErrorAs(t, err, &serr)
		require.False(t, serr.Remote)
		require.Equal(t, SessionErrorCode(1337), serr.ErrorCode)
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}

	str.SetReadDeadline(time.Now().Add(time.Second))
	_, err = str.Read([]byte{0})
	require.ErrorIs(t, err, &quic.StreamError{Remote: true, StreamID: str.StreamID(), ErrorCode: sessionCloseErrorCode})
}
