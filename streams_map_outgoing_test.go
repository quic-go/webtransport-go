package webtransport

import (
	"context"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"
)

func TestOutgoingStreamsMapOpenStreamSyncCloseSession(t *testing.T) {
	t.Run("bidirectional", func(t *testing.T) {
		testOutgoingStreamsMapOpenStreamSyncCloseSession(t, newOutgoingBidiStreamsMap)
	})
	t.Run("unidirectional", func(t *testing.T) {
		testOutgoingStreamsMapOpenStreamSyncCloseSession(t, newOutgoingUniStreamsMap)
	})
}

func testOutgoingStreamsMapOpenStreamSyncCloseSession[T outgoingStream](
	t *testing.T,
	newStreams func(*quic.Conn, sessionID, func(capsule)) *outgoingStreamsMap[T],
) {
	clientConn, _ := newConnPair(t, newUDPConnLocalhost(t), newUDPConnLocalhost(t))
	streams := newStreams(clientConn, 42, func(c capsule) {
		t.Fatalf("unexpected capsule: %#v", c)
	})

	for {
		if _, err := streams.OpenStream(); err != nil {
			break
		}
	}

	errChan := make(chan error, 1)
	go func() {
		_, err := streams.OpenStreamSync(context.Background())
		errChan <- err
	}()

	select {
	case <-errChan:
		t.Fatal("should not have opened stream")
	case <-time.After(scaleDuration(10 * time.Millisecond)):
	}

	sessionErr := &SessionError{ErrorCode: 1337, Message: "closing"}
	streams.CloseSession(sessionErr)

	select {
	case err := <-errChan:
		require.ErrorIs(t, err, sessionErr)
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestOutgoingStreamsMapOpenStreamLimit(t *testing.T) {
	t.Run("bidirectional", func(t *testing.T) {
		testOutgoingStreamsMapOpenStreamLimit(t, newOutgoingBidiStreamsMap, streamsBlockedBidiCapsule{MaximumStreams: 0})
	})
	t.Run("unidirectional", func(t *testing.T) {
		testOutgoingStreamsMapOpenStreamLimit(t, newOutgoingUniStreamsMap, streamsBlockedUniCapsule{MaximumStreams: 0})
	})
}

func testOutgoingStreamsMapOpenStreamLimit[T outgoingStream](
	t *testing.T,
	newStreams func(*quic.Conn, sessionID, func(capsule)) *outgoingStreamsMap[T],
	blocked capsule,
) {
	var capsules []capsule
	streams := newStreams(nil, 42, func(c capsule) { capsules = append(capsules, c) })
	streams.maxStreams = 0

	_, err := streams.OpenStream()
	var limitErr *StreamLimitReachedError
	require.ErrorAs(t, err, &limitErr)
	require.Equal(t, []capsule{blocked}, capsules)

	_, err = streams.OpenStream()
	require.ErrorAs(t, err, &limitErr)
	require.Len(t, capsules, 1)
}

func TestOutgoingStreamsMapBlockedCapsules(t *testing.T) {
	t.Run("bidirectional", func(t *testing.T) {
		testOutgoingStreamsMapBlockedCapsules(t, newOutgoingBidiStreamsMap, streamsBlockedBidiCapsule{MaximumStreams: 0})
	})
	t.Run("unidirectional", func(t *testing.T) {
		testOutgoingStreamsMapBlockedCapsules(t, newOutgoingUniStreamsMap, streamsBlockedUniCapsule{MaximumStreams: 0})
	})
}

func testOutgoingStreamsMapBlockedCapsules[T outgoingStream](
	t *testing.T,
	newStreams func(*quic.Conn, sessionID, func(capsule)) *outgoingStreamsMap[T],
	blocked capsule,
) {
	clientConn, _ := newConnPair(t, newUDPConnLocalhost(t), newUDPConnLocalhost(t))
	capsuleChan := make(chan capsule, 2)
	streams := newStreams(clientConn, 42, func(c capsule) { capsuleChan <- c })
	streams.maxStreams = 0

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errChan := make(chan error, 1)
	go func() {
		_, err := streams.OpenStreamSync(ctx)
		errChan <- err
	}()

	select {
	case c := <-capsuleChan:
		require.Equal(t, blocked, c)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for capsule")
	}

	_, err := streams.OpenStream()
	var limitErr *StreamLimitReachedError
	require.ErrorAs(t, err, &limitErr)
	select {
	case c := <-capsuleChan:
		t.Fatalf("unexpected capsule: %#v", c)
	default:
	}

	cancel()
	select {
	case err := <-errChan:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for stream cancellation")
	}
}

func TestOutgoingStreamsMapOpenStreamSyncBlockedUntilLimitUpdate(t *testing.T) {
	t.Run("bidirectional", func(t *testing.T) {
		testOutgoingStreamsMapOpenStreamSyncBlockedUntilLimitUpdate(
			t,
			newOutgoingBidiStreamsMap,
			streamsBlockedBidiCapsule{MaximumStreams: 0},
			streamsBlockedBidiCapsule{MaximumStreams: 1},
		)
	})
	t.Run("unidirectional", func(t *testing.T) {
		testOutgoingStreamsMapOpenStreamSyncBlockedUntilLimitUpdate(
			t,
			newOutgoingUniStreamsMap,
			streamsBlockedUniCapsule{MaximumStreams: 0},
			streamsBlockedUniCapsule{MaximumStreams: 1},
		)
	})
}

func testOutgoingStreamsMapOpenStreamSyncBlockedUntilLimitUpdate[T outgoingStream](
	t *testing.T,
	newStreams func(*quic.Conn, sessionID, func(capsule)) *outgoingStreamsMap[T],
	initialBlocked capsule,
	nextBlocked capsule,
) {
	clientConn, _ := newConnPair(t, newUDPConnLocalhost(t), newUDPConnLocalhost(t))
	capsuleChan := make(chan capsule, 2)
	streams := newStreams(clientConn, 42, func(c capsule) { capsuleChan <- c })
	streams.maxStreams = 0

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		_, err := streams.OpenStreamSync(ctx)
		errChan <- err
	}()

	select {
	case <-errChan:
		t.Fatal("should not have opened stream")
	case <-time.After(scaleDuration(10 * time.Millisecond)):
	}
	select {
	case c := <-capsuleChan:
		require.Equal(t, initialBlocked, c)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for capsule")
	}

	streams.UpdateStreamLimit(1)

	select {
	case err := <-errChan:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
	select {
	case c := <-capsuleChan:
		t.Fatalf("unexpected capsule: %#v", c)
	default:
	}

	_, err := streams.OpenStream()
	var limitErr *StreamLimitReachedError
	require.ErrorAs(t, err, &limitErr)
	select {
	case c := <-capsuleChan:
		require.Equal(t, nextBlocked, c)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for capsule")
	}

	go func() {
		_, err := streams.OpenStreamSync(ctx)
		errChan <- err
	}()
	select {
	case <-errChan:
		t.Fatal("should not have opened stream")
	case <-time.After(scaleDuration(10 * time.Millisecond)):
	}
}

func TestOutgoingStreamsMapOpenStreamSyncOrder(t *testing.T) {
	t.Run("bidirectional", func(t *testing.T) {
		testOutgoingStreamsMapOpenStreamSyncOrder(t, newOutgoingBidiStreamsMap, streamsBlockedBidiCapsule{MaximumStreams: 0}, func(limit uint64) capsule {
			return streamsBlockedBidiCapsule{MaximumStreams: limit}
		})
	})
	t.Run("unidirectional", func(t *testing.T) {
		testOutgoingStreamsMapOpenStreamSyncOrder(t, newOutgoingUniStreamsMap, streamsBlockedUniCapsule{MaximumStreams: 0}, func(limit uint64) capsule {
			return streamsBlockedUniCapsule{MaximumStreams: limit}
		})
	})
}

func testOutgoingStreamsMapOpenStreamSyncOrder[T outgoingStream](
	t *testing.T,
	newStreams func(*quic.Conn, sessionID, func(capsule)) *outgoingStreamsMap[T],
	initialBlocked capsule,
	blockedAt func(uint64) capsule,
) {
	clientConn, _ := newConnPair(t, newUDPConnLocalhost(t), newUDPConnLocalhost(t))
	const numStreams = 16
	capsuleChan := make(chan capsule, numStreams)
	streams := newStreams(clientConn, 42, func(c capsule) { capsuleChan <- c })
	streams.maxStreams = 0

	type result struct {
		index int
		err   error
	}
	resultChan := make(chan result, numStreams)
	for i := range numStreams {
		go func() {
			_, err := streams.OpenStreamSync(context.Background())
			resultChan <- result{index: i, err: err}
		}()
		require.Eventually(t, func() bool {
			streams.mx.Lock()
			defer streams.mx.Unlock()
			return len(streams.openQueue) == i+1
		}, time.Second, scaleDuration(time.Millisecond))
	}
	select {
	case c := <-capsuleChan:
		require.Equal(t, initialBlocked, c)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for capsule")
	}
	select {
	case c := <-capsuleChan:
		t.Fatalf("unexpected capsule: %#v", c)
	case <-time.After(scaleDuration(10 * time.Millisecond)):
	}

	for i := range numStreams {
		streams.UpdateStreamLimit(uint64(i + 1))
		select {
		case r := <-resultChan:
			require.NoError(t, r.err)
			require.Equal(t, i, r.index)
		case <-time.After(time.Second):
			t.Fatal("timeout")
		}
		if i == numStreams-1 {
			select {
			case c := <-capsuleChan:
				t.Fatalf("unexpected capsule: %#v", c)
			default:
			}
			continue
		}
		select {
		case c := <-capsuleChan:
			require.Equal(t, blockedAt(uint64(i+1)), c)
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for capsule")
		}
	}
}
