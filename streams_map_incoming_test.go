package webtransport

import (
	"context"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"
)

func TestIncomingStreamsMapAddStreamAfterCloseSession(t *testing.T) {
	ctx := context.Background()
	clientConn, serverConn := newConnPair(t, newUDPConnLocalhost(t), newUDPConnLocalhost(t))
	streams := newIncomingStreamsMap(ctx)

	serverStr, err := serverConn.OpenStream()
	require.NoError(t, err)
	_, err = serverStr.Write([]byte("x"))
	require.NoError(t, err)
	clientStr, err := clientConn.AcceptStream(ctx)
	require.NoError(t, err)
	streams.AddStream(clientStr)

	str, err := streams.AcceptStream(ctx)
	require.NoError(t, err)
	require.NotNil(t, str)

	sessionErr := &SessionError{ErrorCode: 42, Message: "bye"}
	streams.CloseSession(sessionErr)
	_, err = streams.AcceptStream(ctx)
	require.ErrorIs(t, err, sessionErr)

	serverStr, err = serverConn.OpenStream()
	require.NoError(t, err)
	_, err = serverStr.Write([]byte("x"))
	require.NoError(t, err)
	clientStr, err = clientConn.AcceptStream(ctx)
	require.NoError(t, err)
	streams.AddStream(clientStr)

	select {
	case <-serverStr.Context().Done():
		require.ErrorIs(t,
			context.Cause(serverStr.Context()),
			&quic.StreamError{Remote: true, StreamID: serverStr.StreamID(), ErrorCode: WTSessionGoneErrorCode},
		)
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}

	serverUniStr, err := serverConn.OpenUniStream()
	require.NoError(t, err)
	_, err = serverUniStr.Write([]byte("x"))
	require.NoError(t, err)
	clientUniStr, err := clientConn.AcceptUniStream(ctx)
	require.NoError(t, err)
	streams.AddUniStream(clientUniStr)

	select {
	case <-serverUniStr.Context().Done():
		require.ErrorIs(t,
			context.Cause(serverUniStr.Context()),
			&quic.StreamError{Remote: true, StreamID: serverUniStr.StreamID(), ErrorCode: WTSessionGoneErrorCode},
		)
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}
