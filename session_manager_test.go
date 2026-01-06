package webtransport

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"

	"github.com/stretchr/testify/require"
)

func TestSessionManager(t *testing.T) {
	clientConn, serverConn := newConnPair(t)
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
	// TODO: somehow send an HTTP response so we can call ReadResponse()
	reqStr.ReadResponse()

	const sessionID = 42

	sessMgr := newSessionManager(time.Hour)
	// first add the streams...
	sessMgr.AddStream(clientStr, sessionID)
	sessMgr.AddUniStream(clientUniStr, sessionID)
	// ...then add the session
	sess := newSession(context.Background(), sessionID, clientConn, reqStr, "")
	sessMgr.AddSession(sessionID, sess)

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
