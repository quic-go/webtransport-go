package webtransport

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOutgoingStreamsMapOpenStreamSyncCloseSession(t *testing.T) {
	t.Run("bidirectional", func(t *testing.T) {
		testOutgoingStreamsMapOpenStreamSyncCloseSession(
			t,
			func(m *outgoingStreamsMap) error { _, err := m.OpenStream(); return err },
			func(m *outgoingStreamsMap) error { _, err := m.OpenStreamSync(context.Background()); return err },
		)
	})
	t.Run("unidirectional", func(t *testing.T) {
		testOutgoingStreamsMapOpenStreamSyncCloseSession(
			t,
			func(m *outgoingStreamsMap) error { _, err := m.OpenUniStream(); return err },
			func(m *outgoingStreamsMap) error { _, err := m.OpenUniStreamSync(context.Background()); return err },
		)
	})
}

func testOutgoingStreamsMapOpenStreamSyncCloseSession(t *testing.T, openStream, openStreamSync func(*outgoingStreamsMap) error) {
	clientConn, _ := newConnPair(t, newUDPConnLocalhost(t), newUDPConnLocalhost(t))
	streams := newOutgoingStreamsMap(clientConn, 42, func(c capsule) {
		t.Fatalf("unexpected capsule: %#v", c)
	})

	for {
		if err := openStream(streams); err != nil {
			break
		}
	}

	errChan := make(chan error, 1)
	go func() { errChan <- openStreamSync(streams) }()

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
		var capsules []capsule
		streams := newOutgoingStreamsMap(nil, 42, func(c capsule) { capsules = append(capsules, c) })
		streams.bidiLimit.MaxStreams = 0
		_, err := streams.OpenStream()
		var limitErr *StreamLimitReachedError
		require.ErrorAs(t, err, &limitErr)
		require.Equal(t, []capsule{streamsBlockedBidiCapsule{MaximumStreams: 0}}, capsules)

		_, err = streams.OpenStream()
		require.ErrorAs(t, err, &limitErr)
		require.Len(t, capsules, 1)
	})

	t.Run("unidirectional", func(t *testing.T) {
		var capsules []capsule
		streams := newOutgoingStreamsMap(nil, 42, func(c capsule) { capsules = append(capsules, c) })
		streams.uniLimit.MaxStreams = 0
		_, err := streams.OpenUniStream()
		var limitErr *StreamLimitReachedError
		require.ErrorAs(t, err, &limitErr)
		require.Equal(t, []capsule{streamsBlockedUniCapsule{MaximumStreams: 0}}, capsules)

		_, err = streams.OpenUniStream()
		require.ErrorAs(t, err, &limitErr)
		require.Len(t, capsules, 1)
	})
}

func TestOutgoingStreamsMapBlockedCapsules(t *testing.T) {
	clientConn, _ := newConnPair(t, newUDPConnLocalhost(t), newUDPConnLocalhost(t))
	capsuleChan := make(chan capsule, 2)
	streams := newOutgoingStreamsMap(clientConn, 42, func(c capsule) { capsuleChan <- c })
	streams.bidiLimit.MaxStreams = 0

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errChan := make(chan error, 1)
	go func() {
		_, err := streams.OpenStreamSync(ctx)
		errChan <- err
	}()

	select {
	case c := <-capsuleChan:
		require.Equal(t, streamsBlockedBidiCapsule{MaximumStreams: 0}, c)
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
			func(m *outgoingStreamsMap) error { _, err := m.OpenStream(); return err },
			func(m *outgoingStreamsMap, ctx context.Context) error { _, err := m.OpenStreamSync(ctx); return err },
			(*outgoingStreamsMap).UpdateBidiStreamLimit,
			streamsBlockedBidiCapsule{MaximumStreams: 0},
			streamsBlockedBidiCapsule{MaximumStreams: 1},
		)
	})

	t.Run("unidirectional", func(t *testing.T) {
		testOutgoingStreamsMapOpenStreamSyncBlockedUntilLimitUpdate(
			t,
			func(m *outgoingStreamsMap) error { _, err := m.OpenUniStream(); return err },
			func(m *outgoingStreamsMap, ctx context.Context) error { _, err := m.OpenUniStreamSync(ctx); return err },
			(*outgoingStreamsMap).UpdateUniStreamLimit,
			streamsBlockedUniCapsule{MaximumStreams: 0},
			streamsBlockedUniCapsule{MaximumStreams: 1},
		)
	})
}

func testOutgoingStreamsMapOpenStreamSyncBlockedUntilLimitUpdate(
	t *testing.T,
	openStream func(*outgoingStreamsMap) error,
	openStreamSync func(*outgoingStreamsMap, context.Context) error,
	updateLimit func(*outgoingStreamsMap, uint64),
	initialBlocked capsule,
	nextBlocked capsule,
) {
	clientConn, _ := newConnPair(t, newUDPConnLocalhost(t), newUDPConnLocalhost(t))
	capsuleChan := make(chan capsule, 2)
	streams := newOutgoingStreamsMap(clientConn, 42, func(c capsule) { capsuleChan <- c })
	streams.bidiLimit.MaxStreams = 0
	streams.uniLimit.MaxStreams = 0

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	errChan := make(chan error, 1)
	go func() { errChan <- openStreamSync(streams, ctx) }()

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

	updateLimit(streams, 1)

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

	err := openStream(streams)
	var limitErr *StreamLimitReachedError
	require.ErrorAs(t, err, &limitErr)
	select {
	case c := <-capsuleChan:
		require.Equal(t, nextBlocked, c)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for capsule")
	}

	go func() { errChan <- openStreamSync(streams, ctx) }()
	select {
	case <-errChan:
		t.Fatal("should not have opened stream")
	case <-time.After(scaleDuration(10 * time.Millisecond)):
	}
}

func TestOutgoingStreamsMapOpenStreamSyncOrder(t *testing.T) {
	clientConn, _ := newConnPair(t, newUDPConnLocalhost(t), newUDPConnLocalhost(t))
	const numStreams = 16
	capsuleChan := make(chan capsule, numStreams)
	streams := newOutgoingStreamsMap(clientConn, 42, func(c capsule) { capsuleChan <- c })
	streams.bidiLimit.MaxStreams = 0

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
			return len(streams.bidiLimit.OpenQueue) == i+1
		}, time.Second, scaleDuration(time.Millisecond))
	}
	select {
	case c := <-capsuleChan:
		require.Equal(t, streamsBlockedBidiCapsule{MaximumStreams: 0}, c)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for capsule")
	}
	select {
	case c := <-capsuleChan:
		t.Fatalf("unexpected capsule: %#v", c)
	case <-time.After(scaleDuration(10 * time.Millisecond)):
	}

	for i := range numStreams {
		streams.UpdateBidiStreamLimit(uint64(i + 1))
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
			require.Equal(t, streamsBlockedBidiCapsule{MaximumStreams: uint64(i + 1)}, c)
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for capsule")
		}
	}
}
