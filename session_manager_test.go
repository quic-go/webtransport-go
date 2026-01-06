package webtransport

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/quic-go/webtransport-go/internal/synctest"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/testutils/simnet"

	"github.com/stretchr/testify/require"
)

func (m *sessionManager) NumSessions() int {
	m.mx.Lock()
	defer m.mx.Unlock()
	return len(m.sessions)
}

func newSimnetLink(t *testing.T, rtt time.Duration) (client, server *simnet.SimConn, close func(t *testing.T)) {
	t.Helper()

	n := &simnet.Simnet{Router: &simnet.PerfectRouter{}}
	settings := simnet.NodeBiDiLinkSettings{Latency: rtt / 2}
	clientPacketConn := n.NewEndpoint(&net.UDPAddr{IP: net.ParseIP("1.0.0.1"), Port: 9001}, settings)
	serverPacketConn := n.NewEndpoint(&net.UDPAddr{IP: net.ParseIP("1.0.0.2"), Port: 9002}, settings)

	require.NoError(t, n.Start())

	return clientPacketConn, serverPacketConn, func(t *testing.T) {
		require.NoError(t, clientPacketConn.Close())
		require.NoError(t, serverPacketConn.Close())
		require.NoError(t, n.Close())
	}
}

func TestSessionManagerAddingStreams(t *testing.T) {
	clientConn, serverConn := newConnPair(t, newUDPConnLocalhost(t), newUDPConnLocalhost(t))
	t.Cleanup(func() {
		clientConn.CloseWithError(0, "")
		serverConn.CloseWithError(0, "")
	})

	serverStr, err := serverConn.OpenStream()
	require.NoError(t, err)
	_, err = serverStr.Write([]byte("hello"))
	require.NoError(t, err)
	require.NoError(t, serverStr.Close())

	serverUniStr, err := serverConn.OpenUniStream()
	require.NoError(t, err)
	_, err = serverUniStr.Write([]byte("world"))
	require.NoError(t, err)
	require.NoError(t, serverUniStr.Close())

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	clientStr, err := clientConn.AcceptStream(ctx)
	require.NoError(t, err)
	clientUniStr, err := clientConn.AcceptUniStream(ctx)
	require.NoError(t, err)

	tr := http3.Transport{}
	cc := tr.NewRawClientConn(clientConn)
	reqStr, err := cc.OpenRequestStream(ctx)
	require.NoError(t, err)
	require.NoError(t, reqStr.SendRequestHeader(httptest.NewRequest(http.MethodGet, "/", nil)))
	server := http3.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}),
	}
	sc, err := server.NewRawServerConn(serverConn)
	require.NoError(t, err)
	serverReqStr, err := serverConn.AcceptStream(ctx)
	require.NoError(t, err)
	go sc.HandleRequestStream(serverReqStr)

	_, err = reqStr.ReadResponse()
	require.NoError(t, err)

	const sessionID = 42

	sessMgr := newSessionManager(time.Hour)
	require.Zero(t, sessMgr.NumSessions())
	// first add the streams...
	sess := newSession(context.Background(), sessionID, clientConn, reqStr, "")
	// ...then add the session
	sessMgr.AddStream(clientStr, sessionID)
	require.Equal(t, 1, sessMgr.NumSessions())
	sessMgr.AddUniStream(clientUniStr, sessionID)
	sessMgr.AddSession(sessionID, sess)
	require.Equal(t, 1, sessMgr.NumSessions())

	// the streams should now be returned from the session
	sessStr, err := sess.AcceptStream(ctx)
	require.NoError(t, err)
	data, err := io.ReadAll(sessStr)
	require.NoError(t, err)
	require.Equal(t, []byte("hello"), data)

	sessUniStr, err := sess.AcceptUniStream(ctx)
	require.NoError(t, err)
	data, err = io.ReadAll(sessUniStr)
	require.NoError(t, err)
	require.Equal(t, []byte("world"), data)
}

func TestSessionManagerStreamReordering(t *testing.T) {
	// synctest works slightly differently on Go 1.24,
	// so we skip the test
	if strings.HasPrefix(runtime.Version(), "go1.24") {
		t.Skip("skipping on Go 1.24 due to synctest issues")
	}

	synctest.Test(t, func(t *testing.T) {
		clientPacketConn, serverPacketConn, closeFn := newSimnetLink(t, 10*time.Millisecond)
		defer closeFn(t)
		clientConn, serverConn := newConnPair(t, clientPacketConn, serverPacketConn)
		t.Cleanup(func() {
			clientConn.CloseWithError(0, "")
			serverConn.CloseWithError(0, "")
		})

		serverStr1, err := serverConn.OpenStream()
		require.NoError(t, err)
		_, err = serverStr1.Write([]byte("lorem"))
		require.NoError(t, err)
		require.NoError(t, serverStr1.Close())

		serverUniStr1, err := serverConn.OpenUniStream()
		require.NoError(t, err)
		_, err = serverUniStr1.Write([]byte("ipsum"))
		require.NoError(t, err)
		require.NoError(t, serverUniStr1.Close())

		serverStr2, err := serverConn.OpenStream()
		require.NoError(t, err)
		_, err = serverStr2.Write([]byte("dolor"))
		require.NoError(t, err)
		require.NoError(t, serverStr2.Close())

		serverUniStr2, err := serverConn.OpenUniStream()
		require.NoError(t, err)
		_, err = serverUniStr2.Write([]byte("sit"))
		require.NoError(t, err)
		require.NoError(t, serverUniStr2.Close())

		serverUniStr3, err := serverConn.OpenUniStream()
		require.NoError(t, err)
		_, err = serverUniStr3.Write([]byte("amet"))
		require.NoError(t, err)
		require.NoError(t, serverUniStr3.Close())

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		clientStr1, err := clientConn.AcceptStream(ctx)
		require.NoError(t, err)
		clientUniStr1, err := clientConn.AcceptUniStream(ctx)
		require.NoError(t, err)
		clientStr2, err := clientConn.AcceptStream(ctx)
		require.NoError(t, err)
		clientUniStr2, err := clientConn.AcceptUniStream(ctx)
		require.NoError(t, err)
		clientUniStr3, err := clientConn.AcceptUniStream(ctx)
		require.NoError(t, err)

		tr := http3.Transport{}
		cc := tr.NewRawClientConn(clientConn)
		reqStr, err := cc.OpenRequestStream(ctx)
		require.NoError(t, err)
		require.NoError(t, reqStr.SendRequestHeader(httptest.NewRequest(http.MethodGet, "/", nil)))
		server := http3.Server{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}),
		}
		sc, err := server.NewRawServerConn(serverConn)
		require.NoError(t, err)
		serverReqStr, err := serverConn.AcceptStream(ctx)
		require.NoError(t, err)
		go sc.HandleRequestStream(serverReqStr)

		_, err = reqStr.ReadResponse()
		require.NoError(t, err)

		const sessionID = 42
		const timeout = 3 * time.Second

		sessMgr := newSessionManager(timeout)
		require.Zero(t, sessMgr.NumSessions())
		// add the first stream
		sessMgr.AddStream(clientStr1, sessionID)
		require.Equal(t, 1, sessMgr.NumSessions())
		time.Sleep(timeout + time.Second)
		// the stream should have been reset and the session manager should have no sessions
		require.Zero(t, sessMgr.NumSessions())
		_, err = serverStr1.Read([]byte{0})
		require.ErrorIs(t, err, &quic.StreamError{Remote: true, StreamID: serverStr1.StreamID(), ErrorCode: WTBufferedStreamRejectedErrorCode})

		sessMgr.AddUniStream(clientUniStr1, sessionID)
		sessMgr.AddUniStream(clientUniStr2, sessionID)
		require.Equal(t, 1, sessMgr.NumSessions())
		time.Sleep(timeout - time.Second)
		// adding another stream resets the timer
		sessMgr.AddStream(clientStr2, sessionID)
		time.Sleep(timeout - time.Second)
		require.Equal(t, 1, sessMgr.NumSessions())

		// now add the session
		sess := newSession(context.Background(), sessionID, clientConn, reqStr, "")
		sessMgr.AddSession(sessionID, sess)
		require.Equal(t, 1, sessMgr.NumSessions())

		// wait for a long time, then add another stream
		time.Sleep(timeout + time.Second)
		require.Equal(t, 1, sessMgr.NumSessions())
		sessMgr.AddUniStream(clientUniStr3, sessionID)
		time.Sleep(timeout + time.Second)

		// the "lorem" stream should have been reset and the "dolor" stream should have been returned
		sessStr, err := sess.AcceptStream(ctx)
		require.NoError(t, err)
		data, err := io.ReadAll(sessStr)
		require.NoError(t, err)
		require.Equal(t, []byte("dolor"), data)

		sessUniStr1, err := sess.AcceptUniStream(ctx)
		require.NoError(t, err)
		data, err = io.ReadAll(sessUniStr1)
		require.NoError(t, err)
		require.Equal(t, []byte("ipsum"), data)

		sessUniStr2, err := sess.AcceptUniStream(ctx)
		require.NoError(t, err)
		data, err = io.ReadAll(sessUniStr2)
		require.NoError(t, err)
		require.Equal(t, []byte("sit"), data)

		sessUniStr3, err := sess.AcceptUniStream(ctx)
		require.NoError(t, err)
		data, err = io.ReadAll(sessUniStr3)
		require.NoError(t, err)
		require.Equal(t, []byte("amet"), data)
	})
}
