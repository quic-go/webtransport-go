package webtransport

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/quicvarint"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockHTTP3Stream struct{ *bytes.Reader }

func (s mockHTTP3Stream) Write(p []byte) (int, error)                     { return len(p), nil }
func (s mockHTTP3Stream) Close() error                                    { return nil }
func (s mockHTTP3Stream) ReceiveDatagram(context.Context) ([]byte, error) { return nil, io.EOF }
func (s mockHTTP3Stream) SendDatagram([]byte) error                       { return nil }
func (s mockHTTP3Stream) CancelRead(quic.StreamErrorCode)                 {}
func (s mockHTTP3Stream) CancelWrite(quic.StreamErrorCode)                {}
func (s mockHTTP3Stream) SetWriteDeadline(time.Time) error                { return nil }

type quicHTTP3Stream struct{ *quic.Stream }

func (s quicHTTP3Stream) ReceiveDatagram(context.Context) ([]byte, error) { return nil, io.EOF }
func (s quicHTTP3Stream) SendDatagram([]byte) error                       { return nil }

func scaleDuration(d time.Duration) time.Duration {
	if os.Getenv("CI") != "" {
		return 5 * d
	}
	return d
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
	return newConnPairWithServerConfig(
		t,
		clientConn,
		serverConn,
		&quic.Config{
			InitialStreamReceiveWindow:       1 << 60,
			InitialConnectionReceiveWindow:   1 << 60,
			EnableDatagrams:                  true,
			EnableStreamResetPartialDelivery: true,
		},
	)
}

func newConnPairWithServerConfig(t *testing.T, clientConn, serverConn net.PacketConn, serverConf *quic.Config) (client, server *quic.Conn) {
	t.Helper()

	ln, err := quic.ListenEarly(serverConn, TLSConf, serverConf)
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

func TestExportKeyingMaterial(t *testing.T) {
	clientConn, serverConn := newConnPair(t, newUDPConnLocalhost(t), newUDPConnLocalhost(t))
	id := sessionID(0x1020304050607080)
	clientSess := newSession(context.Background(), id, clientConn, mockHTTP3Stream{bytes.NewReader(nil)}, "")
	serverSess := newSession(context.Background(), id, serverConn, mockHTTP3Stream{bytes.NewReader(nil)}, "")

	label := "label"
	context := []byte("context")
	clientMaterial, err := clientSess.ExportKeyingMaterial(label, context, 32)
	require.NoError(t, err)
	serverMaterial, err := serverSess.ExportKeyingMaterial(label, context, 32)
	require.NoError(t, err)
	require.Equal(t, clientMaterial, serverMaterial)

	exporterContext := make([]byte, 8, 8+1+len(label)+1+len(context))
	binary.BigEndian.PutUint64(exporterContext, uint64(id))
	exporterContext = append(exporterContext, byte(len(label)))
	exporterContext = append(exporterContext, label...)
	exporterContext = append(exporterContext, byte(len(context)))
	exporterContext = append(exporterContext, context...)
	connState := clientConn.ConnectionState()
	expected, err := connState.TLS.ExportKeyingMaterial("EXPORTER-WebTransport", exporterContext, 32)
	require.NoError(t, err)
	require.Equal(t, expected, clientMaterial)

	otherMaterial, err := clientSess.ExportKeyingMaterial("other", context, 32)
	require.NoError(t, err)
	require.NotEqual(t, clientMaterial, otherMaterial)

	_, err = clientSess.ExportKeyingMaterial(strings.Repeat("a", 256), nil, 32)
	require.ErrorContains(t, err, "exporter label")
	_, err = clientSess.ExportKeyingMaterial(label, bytes.Repeat([]byte("a"), 256), 32)
	require.ErrorContains(t, err, "exporter context")
	_, err = clientSess.ExportKeyingMaterial(label, nil, -1)
	require.ErrorContains(t, err, "non-negative")
}

func TestCapsuleParseErrorClosesSessionWithDatagramError(t *testing.T) {
	b := quicvarint.Append(nil, uint64(maxStreamsBidiCapsuleType))
	b = quicvarint.Append(b, uint64(quicvarint.Len(42)+1))
	b = quicvarint.Append(b, 42)
	b = append(b, 0)

	sess := newSession(context.Background(), 42, nil, mockHTTP3Stream{bytes.NewReader(b)}, "")
	select {
	case <-sess.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}

	sess.closeMx.Lock()
	err := sess.closeErr
	sess.closeMx.Unlock()

	require.ErrorIs(t, err, &http3.Error{ErrorCode: http3.ErrCodeDatagramError})
	require.ErrorContains(t, err, "trailing data")
}

func TestForbiddenStreamDataFlowControlCapsulesCloseSession(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  http3.CapsuleType
		msg  string
	}{
		{
			name: "WT_MAX_STREAM_DATA",
			typ:  maxStreamDataCapsuleType,
			msg:  "WT_MAX_STREAM_DATA capsule received",
		},
		{
			name: "WT_STREAM_DATA_BLOCKED",
			typ:  streamDataBlockedCapsuleType,
			msg:  "WT_STREAM_DATA_BLOCKED capsule received",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := quicvarint.Append(nil, uint64(tc.typ))
			b = quicvarint.Append(b, 0)

			sess := newSession(context.Background(), 42, nil, mockHTTP3Stream{bytes.NewReader(b)}, "")
			select {
			case <-sess.Context().Done():
			case <-time.After(time.Second):
				t.Fatal("timeout")
			}

			sess.closeMx.Lock()
			err := sess.closeErr
			sess.closeMx.Unlock()

			require.ErrorIs(t, err, &http3.Error{ErrorCode: http3.ErrCodeDatagramError})
			require.ErrorContains(t, err, tc.msg)
		})
	}
}

func TestMaxStreamsCapsuleDecreaseClosesSession(t *testing.T) {
	b := (maxStreamsBidiCapsule{MaximumStreams: maxOutgoingStreams}).Append(nil)
	b = (maxStreamsBidiCapsule{MaximumStreams: maxOutgoingStreams - 1}).Append(b)

	sess := newSession(context.Background(), 42, nil, mockHTTP3Stream{bytes.NewReader(b)}, "")
	select {
	case <-sess.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}

	sess.closeMx.Lock()
	err := sess.closeErr
	sess.closeMx.Unlock()

	require.ErrorIs(t, err, &http3.Error{ErrorCode: http3.ErrCode(WTFlowControlErrorCode)})
	require.ErrorContains(t, err, errMaxStreamsDecreased.Error())
}

func TestSessionSendsQueuedCapsules(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	clientConn, serverConn := newConnPair(t, newUDPConnLocalhost(t), newUDPConnLocalhost(t))

	clientStr, err := clientConn.OpenStreamSync(ctx)
	require.NoError(t, err)
	sess := newSession(context.Background(), 42, clientConn, quicHTTP3Stream{clientStr}, "")
	sess.queueCapsule(streamsBlockedBidiCapsule{MaximumStreams: 42})
	sess.queueCapsule(streamsBlockedUniCapsule{MaximumStreams: 1337})

	serverStr, err := serverConn.AcceptStream(ctx)
	require.NoError(t, err)
	require.NoError(t, serverStr.SetReadDeadline(time.Now().Add(time.Second)))

	c, err := parseNextCapsule(serverStr)
	require.NoError(t, err)
	require.Equal(t, streamsBlockedBidiCapsule{MaximumStreams: 42}, c)
	c, err = parseNextCapsule(serverStr)
	require.NoError(t, err)
	require.Equal(t, streamsBlockedUniCapsule{MaximumStreams: 1337}, c)
}

func TestSessionClosesWhenOutgoingCapsuleQueueFull(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	clientConn, serverConn := newConnPairWithServerConfig(
		t,
		newUDPConnLocalhost(t),
		newUDPConnLocalhost(t),
		&quic.Config{
			InitialStreamReceiveWindow:       1,
			MaxStreamReceiveWindow:           1,
			InitialConnectionReceiveWindow:   1,
			MaxConnectionReceiveWindow:       1,
			EnableDatagrams:                  true,
			EnableStreamResetPartialDelivery: true,
		},
	)

	clientStr, err := clientConn.OpenStreamSync(ctx)
	require.NoError(t, err)

	_, err = clientStr.Write([]byte{0})
	require.NoError(t, err)
	serverStr, err := serverConn.AcceptStream(ctx)
	require.NoError(t, err)
	require.NoError(t, serverStr.SetReadDeadline(time.Now().Add(time.Second)))
	_, err = io.ReadFull(serverStr, make([]byte, 1))
	require.NoError(t, err)
	require.NoError(t, serverStr.SetReadDeadline(time.Time{}))

	// Fill quic-go's per-stream packet buffer. With the peer window capped at 1,
	// the next capsule write stays blocked while we fill our capsule queue.
	_, err = clientStr.Write(make([]byte, 1450))
	require.NoError(t, err)

	sess := newSession(context.Background(), 0, clientConn, quicHTTP3Stream{clientStr}, "")
	for range maxQueuedOutgoingCapsules {
		sess.queueCapsule(streamsBlockedBidiCapsule{})
	}
	sess.queueCapsule(streamsBlockedUniCapsule{})

	b := make([]byte, 1024)
	require.NoError(t, serverStr.SetReadDeadline(time.Now().Add(time.Second)))
	for err == nil {
		_, err = serverStr.Read(b)
	}
	require.ErrorIs(t, err, &quic.StreamError{
		StreamID:  serverStr.StreamID(),
		ErrorCode: quic.StreamErrorCode(http3.ErrCodeExcessiveLoad),
		Remote:    true,
	})

	_, err = sess.OpenStream()
	var h3Err *http3.Error
	require.ErrorAs(t, err, &h3Err)
	require.Equal(t, http3.ErrCodeExcessiveLoad, h3Err.ErrorCode)
}

func TestCloseWithErrorDropsQueuedCapsulesWhenConnectStreamBlocked(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	clientConn, serverConn := newConnPairWithServerConfig(
		t,
		newUDPConnLocalhost(t),
		newUDPConnLocalhost(t),
		&quic.Config{
			InitialStreamReceiveWindow:       1,
			MaxStreamReceiveWindow:           1,
			InitialConnectionReceiveWindow:   1,
			MaxConnectionReceiveWindow:       1,
			EnableDatagrams:                  true,
			EnableStreamResetPartialDelivery: true,
		},
	)

	clientStr, err := clientConn.OpenStreamSync(ctx)
	require.NoError(t, err)

	_, err = clientStr.Write([]byte{0})
	require.NoError(t, err)
	serverStr, err := serverConn.AcceptStream(ctx)
	require.NoError(t, err)
	require.NoError(t, serverStr.SetReadDeadline(time.Now().Add(time.Second)))
	_, err = io.ReadFull(serverStr, make([]byte, 1))
	require.NoError(t, err)
	require.NoError(t, serverStr.SetReadDeadline(time.Time{}))

	_, err = clientStr.Write(make([]byte, 1450))
	require.NoError(t, err)

	sess := newSession(context.Background(), 0, clientConn, quicHTTP3Stream{clientStr}, "")
	sess.queueCapsule(streamsBlockedBidiCapsule{})

	done := make(chan error, 1)
	go func() { done <- sess.CloseWithError(42, "close") }()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}
