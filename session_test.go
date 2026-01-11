package webtransport

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/quicvarint"

	"github.com/stretchr/testify/assert"
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

func newConnPair(t *testing.T, clientConn, serverConn net.PacketConn) (client, server *quic.Conn) {
	t.Helper()

	ln, err := quic.ListenEarly(
		serverConn,
		TLSConf,
		&quic.Config{
			InitialStreamReceiveWindow:       1 << 60,
			InitialConnectionReceiveWindow:   1 << 60,
			EnableDatagrams:                  true,
			EnableStreamResetPartialDelivery: true,
		},
	)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cl, err := quic.DialEarly(
		ctx,
		clientConn,
		ln.Addr(),
		&tls.Config{
			ServerName: "localhost",
			NextProtos: []string{http3.NextProtoH3},
			RootCAs:    CertPool,
		},
		&quic.Config{
			EnableDatagrams:                  true,
			EnableStreamResetPartialDelivery: true,
		},
	)
	require.NoError(t, err)
	assert.True(t, cl.ConnectionState().SupportsDatagrams.Local)
	assert.True(t, cl.ConnectionState().SupportsDatagrams.Remote)
	assert.True(t, cl.ConnectionState().SupportsStreamResetPartialDelivery.Local)
	assert.True(t, cl.ConnectionState().SupportsStreamResetPartialDelivery.Remote)
	t.Cleanup(func() { cl.CloseWithError(0, "") })

	conn, err := ln.Accept(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { conn.CloseWithError(0, "") })
	select {
	case <-conn.HandshakeComplete():
		assert.True(t, conn.ConnectionState().SupportsDatagrams.Local)
		assert.True(t, conn.ConnectionState().SupportsDatagrams.Remote)
		assert.True(t, conn.ConnectionState().SupportsStreamResetPartialDelivery.Local)
		assert.True(t, conn.ConnectionState().SupportsStreamResetPartialDelivery.Remote)
	case <-ctx.Done():
		t.Fatal("timeout")
	}
	return cl, conn
}

// mockHTTP3Stream is a minimal mock for http3Stream that blocks on Read.
type mockHTTP3Stream struct {
	readChan  chan struct{}
	closeOnce sync.Once
}

func newMockHTTP3Stream() *mockHTTP3Stream {
	return &mockHTTP3Stream{readChan: make(chan struct{})}
}

func (m *mockHTTP3Stream) Read(p []byte) (n int, err error) {
	<-m.readChan // block forever
	return 0, io.EOF
}
func (m *mockHTTP3Stream) Write(p []byte) (n int, err error) { return len(p), nil }
func (m *mockHTTP3Stream) Close() error {
	m.closeOnce.Do(func() { close(m.readChan) })
	return nil
}

func (m *mockHTTP3Stream) ReceiveDatagram(context.Context) ([]byte, error) {
	return nil, errors.New("not implemented")
}
func (m *mockHTTP3Stream) SendDatagram([]byte) error        { return nil }
func (m *mockHTTP3Stream) CancelRead(quic.StreamErrorCode)  {}
func (m *mockHTTP3Stream) CancelWrite(quic.StreamErrorCode) {}
func (m *mockHTTP3Stream) SetWriteDeadline(time.Time) error { return nil }

func setupSession(t *testing.T, clientConn *quic.Conn, sessID sessionID) *Session {
	mockStr := newMockHTTP3Stream()
	t.Cleanup(func() { mockStr.Close() })
	sess := newSession(context.Background(), sessID, clientConn, mockStr, "")
	return sess
}

// acceptAndRouteStream accepts a bidirectional stream from the connection and routes it to the session.
func acceptAndRouteStream(t *testing.T, conn *quic.Conn, sess *Session) {
	t.Helper()
	str, err := conn.AcceptStream(context.Background())
	require.NoError(t, err)
	// read the frame type
	typ, err := quicvarint.Read(quicvarint.NewReader(str))
	require.NoError(t, err)
	require.Equal(t, uint64(webTransportFrameType), typ)
	// read and discard the session ID
	_, err = quicvarint.Read(quicvarint.NewReader(str))
	require.NoError(t, err)
	sess.addIncomingStream(str)
}

// acceptAndRouteUniStream accepts a unidirectional stream from the connection and routes it to the session.
func acceptAndRouteUniStream(t *testing.T, conn *quic.Conn, sess *Session) {
	t.Helper()
	str, err := conn.AcceptUniStream(context.Background())
	require.NoError(t, err)
	// read the stream type
	typ, err := quicvarint.Read(quicvarint.NewReader(str))
	require.NoError(t, err)
	require.Equal(t, uint64(webTransportUniStreamType), typ)
	// read and discard the session ID
	_, err = quicvarint.Read(quicvarint.NewReader(str))
	require.NoError(t, err)
	sess.addIncomingUniStream(str)
}

func TestAddStreamAfterSessionClose(t *testing.T) {
	const sessionID = 42
	clientConn, serverConn := newConnPair(t, newUDPConnLocalhost(t), newUDPConnLocalhost(t))
	sess := setupSession(t, clientConn, sessionID)

	str1, err := serverConn.OpenStream()
	require.NoError(t, err)
	_, err = str1.Write(streamHeaderBidirectional(sessionID))
	require.NoError(t, err)

	// Route the stream to the session
	go acceptAndRouteStream(t, clientConn, sess)

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
	// Route the stream - the session is closed, so it should be rejected
	go acceptAndRouteStream(t, clientConn, sess)
	select {
	case <-str2.Context().Done():
		require.ErrorIs(t,
			context.Cause(str2.Context()),
			&quic.StreamError{Remote: true, StreamID: str2.StreamID(), ErrorCode: WTSessionGoneErrorCode},
		)
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}

	ustr, err := serverConn.OpenUniStream()
	require.NoError(t, err)
	_, err = ustr.Write(streamHeaderUnidirectional(sessionID))
	require.NoError(t, err)
	// Route the stream - the session is closed, so it should be rejected
	go acceptAndRouteUniStream(t, clientConn, sess)
	select {
	case <-ustr.Context().Done():
		require.ErrorIs(t,
			context.Cause(ustr.Context()),
			&quic.StreamError{Remote: true, StreamID: ustr.StreamID(), ErrorCode: WTSessionGoneErrorCode},
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
	clientConn, _ := newConnPair(t, newUDPConnLocalhost(t), newUDPConnLocalhost(t))
	sess := setupSession(t, clientConn, sessionID)

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

// TestCloseWithErrorTruncatesSendMessage tests that when CloseWithError is called
// with a message longer than 1024 bytes, the capsule written to the wire contains
// a truncated message.
func TestCloseWithErrorTruncatesSendMessage(t *testing.T) {
	clientConn, serverConn := newConnPair(t, newUDPConnLocalhost(t), newUDPConnLocalhost(t))

	type capsuleData struct {
		errCode uint32
		msg     []byte
	}
	capsuleChan := make(chan capsuleData, 1)

	server := &http3.Server{}
	mux := http.NewServeMux()
	mux.HandleFunc("/webtransport", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()

		reader := quicvarint.NewReader(r.Body)
		typ, capsuleReader, err := http3.ParseCapsule(reader)
		if err != nil {
			return
		}
		if typ == closeSessionCapsuleType {
			var b [4]byte
			if _, err := io.ReadFull(capsuleReader, b[:]); err != nil {
				t.Errorf("failed to read error code: %v", err)
				return
			}
			errCode := binary.BigEndian.Uint32(b[:])
			msg, err := io.ReadAll(capsuleReader)
			if err != nil {
				t.Errorf("failed to read error message: %v", err)
				return
			}
			capsuleChan <- capsuleData{errCode: errCode, msg: msg}
			return
		}
	})
	server.Handler = mux
	t.Cleanup(func() { server.Close() })
	go server.ServeQUICConn(serverConn)

	serverAddr := fmt.Sprintf("https://localhost:%d/webtransport", serverConn.LocalAddr().(*net.UDPAddr).Port)

	tr := &http3.Transport{}
	conn := tr.NewClientConn(clientConn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reqStr, err := conn.OpenRequestStream(ctx)
	require.NoError(t, err)
	require.NoError(t, reqStr.SendRequestHeader(NewWebTransportRequest(t, serverAddr)))
	rsp, err := reqStr.ReadResponse()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rsp.StatusCode)

	sess := newSession(context.Background(), 42, clientConn, reqStr, "")
	require.NoError(t, sess.CloseWithError(42, strings.Repeat("a", maxCloseCapsuleErrorMsgLen+500)))

	select {
	case data := <-capsuleChan:
		require.Equal(t, uint32(42), data.errCode)
		// the message should be truncated to maxCloseCapsuleErrorMsgLen
		require.Len(t, data.msg, maxCloseCapsuleErrorMsgLen)
		require.Equal(t, strings.Repeat("a", maxCloseCapsuleErrorMsgLen), string(data.msg))
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for capsule")
	}
}

// TestCloseWithErrorTruncatesReceiveMessage tests that when receiving a close capsule
// with a message longer than 1024 bytes, the session truncates the message.
func TestCloseWithErrorTruncatesReceiveMessage(t *testing.T) {
	clientConn, serverConn := newConnPair(t, newUDPConnLocalhost(t), newUDPConnLocalhost(t))

	longMsg := strings.Repeat("b", maxCloseCapsuleErrorMsgLen+500)

	server := &http3.Server{}
	mux := http.NewServeMux()
	mux.HandleFunc("/webtransport", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()

		payload := make([]byte, 4+len(longMsg))
		binary.BigEndian.PutUint32(payload[:4], uint32(1337))
		copy(payload[4:], longMsg)

		if err := http3.WriteCapsule(quicvarint.NewWriter(w), closeSessionCapsuleType, payload); err != nil {
			t.Errorf("failed to write capsule: %v", err)
		}
		w.(http.Flusher).Flush()
	})
	server.Handler = mux
	t.Cleanup(func() { server.Close() })
	go server.ServeQUICConn(serverConn)

	serverAddr := fmt.Sprintf("https://localhost:%d/webtransport", serverConn.LocalAddr().(*net.UDPAddr).Port)

	tr := &http3.Transport{}
	conn := tr.NewClientConn(clientConn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reqStr, err := conn.OpenRequestStream(ctx)
	require.NoError(t, err)
	require.NoError(t, reqStr.SendRequestHeader(NewWebTransportRequest(t, serverAddr)))
	rsp, err := reqStr.ReadResponse()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rsp.StatusCode)

	sess := newSession(context.Background(), 42, clientConn, reqStr, "")

	select {
	case <-sess.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for session to close")
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	_, err = sess.AcceptStream(ctx2)
	require.Error(t, err)

	var sessErr *SessionError
	require.ErrorAs(t, err, &sessErr)
	require.True(t, sessErr.Remote)
	require.Equal(t, SessionErrorCode(1337), sessErr.ErrorCode)
	// the message should be truncated to maxCloseCapsuleErrorMsgLen
	require.Len(t, sessErr.Message, maxCloseCapsuleErrorMsgLen)
	require.Equal(t, strings.Repeat("b", maxCloseCapsuleErrorMsgLen), sessErr.Message)
}

func TestTruncateUTF8(t *testing.T) {
	input := "Go 🚀"
	require.Len(t, input, 7)

	require.Equal(t, "Go 🚀", truncateUTF8(input, 100))
	require.Equal(t, "Go 🚀", truncateUTF8(input, 7))
	require.Equal(t, "Go ", truncateUTF8(input, 6))
	require.Equal(t, "Go ", truncateUTF8(input, 5))
	require.Equal(t, "Go ", truncateUTF8(input, 4))
	require.Equal(t, "Go ", truncateUTF8(input, 3))
	require.Equal(t, "Go", truncateUTF8(input, 2))
	require.Equal(t, "G", truncateUTF8(input, 1))
	require.Equal(t, "", truncateUTF8(input, 0))
}
