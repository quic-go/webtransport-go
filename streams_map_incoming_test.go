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
	streams := newIncomingStreamsMap[*Stream](maxStreamsLimit, nil)
	uniStreams := newIncomingStreamsMap[*ReceiveStream](maxStreamsLimit, nil)

	serverStr, err := serverConn.OpenStream()
	require.NoError(t, err)
	_, err = serverStr.Write([]byte("x"))
	require.NoError(t, err)
	clientStr, err := clientConn.AcceptStream(ctx)
	require.NoError(t, err)
	streamID := clientStr.StreamID()
	streams.AddStream(streamID, newStream(clientStr, nil, func() { streams.RemoveStream(streamID) }))

	str, err := streams.AcceptStream(ctx)
	require.NoError(t, err)
	require.NotNil(t, str)

	sessionErr := &SessionError{ErrorCode: 42, Message: "bye"}
	streams.CloseSession(sessionErr)
	uniStreams.CloseSession(sessionErr)
	_, err = streams.AcceptStream(ctx)
	require.ErrorIs(t, err, sessionErr)

	serverStr, err = serverConn.OpenStream()
	require.NoError(t, err)
	_, err = serverStr.Write([]byte("x"))
	require.NoError(t, err)
	clientStr, err = clientConn.AcceptStream(ctx)
	require.NoError(t, err)
	streamID = clientStr.StreamID()
	streams.AddStream(streamID, newStream(clientStr, nil, func() { streams.RemoveStream(streamID) }))

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
	uniStreamID := clientUniStr.StreamID()
	uniStreams.AddStream(uniStreamID, newReceiveStream(clientUniStr, func() { uniStreams.RemoveStream(uniStreamID) }))

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

func TestIncomingStreamsMapCloseSessionUnblocksAcceptStream(t *testing.T) {
	streams := newIncomingStreamsMap[*Stream](maxStreamsLimit, nil)
	sessionErr := &SessionError{ErrorCode: 42, Message: "bye"}

	errChan := make(chan error, 1)
	go func() {
		_, err := streams.AcceptStream(t.Context())
		errChan <- err
	}()

	streams.CloseSession(sessionErr)
	select {
	case err := <-errChan:
		require.ErrorIs(t, err, sessionErr)
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestIncomingStreamsMapQueuesMaxStreamsOnRemove(t *testing.T) {
	var capsules []uint64
	streams := newIncomingStreamsMap[*ReceiveStream](3, func(limit uint64) {
		capsules = append(capsules, limit)
	})

	streams.AddStream(1, newReceiveStream(nil, nil))
	streams.AddStream(2, newReceiveStream(nil, nil))

	streams.RemoveStream(1)
	require.Equal(t, []uint64{4}, capsules)

	streams.RemoveStream(2)
	require.Equal(t, []uint64{4, 5}, capsules)
}

func TestIncomingStreamsMapMaxStreamsLimit(t *testing.T) {
	var capsules []uint64
	streams := newIncomingStreamsMap[*ReceiveStream](maxStreamsLimit-1, func(limit uint64) {
		capsules = append(capsules, limit)
	})

	streams.AddStream(1, newReceiveStream(nil, nil))
	streams.AddStream(2, newReceiveStream(nil, nil))

	streams.RemoveStream(1)
	require.Equal(t, []uint64{maxStreamsLimit}, capsules)

	streams.RemoveStream(2)
	require.Equal(t, []uint64{maxStreamsLimit}, capsules)
}
