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
	streams := newOutgoingStreamsMap(clientConn, 42)

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
		streams := newOutgoingStreamsMap(nil, 42)
		streams.bidiLimit.MaxStreams = 0
		_, err := streams.OpenStream()
		var limitErr *StreamLimitReachedError
		require.ErrorAs(t, err, &limitErr)
	})

	t.Run("unidirectional", func(t *testing.T) {
		streams := newOutgoingStreamsMap(nil, 42)
		streams.uniLimit.MaxStreams = 0
		_, err := streams.OpenUniStream()
		var limitErr *StreamLimitReachedError
		require.ErrorAs(t, err, &limitErr)
	})
}

func TestOutgoingStreamsMapOpenStreamSyncBlockedUntilLimitUpdate(t *testing.T) {
	t.Run("bidirectional", func(t *testing.T) {
		testOutgoingStreamsMapOpenStreamSyncBlockedUntilLimitUpdate(
			t,
			func(m *outgoingStreamsMap) error { _, err := m.OpenStream(); return err },
			func(m *outgoingStreamsMap, ctx context.Context) error { _, err := m.OpenStreamSync(ctx); return err },
			(*outgoingStreamsMap).UpdateBidiStreamLimit,
		)
	})

	t.Run("unidirectional", func(t *testing.T) {
		testOutgoingStreamsMapOpenStreamSyncBlockedUntilLimitUpdate(
			t,
			func(m *outgoingStreamsMap) error { _, err := m.OpenUniStream(); return err },
			func(m *outgoingStreamsMap, ctx context.Context) error { _, err := m.OpenUniStreamSync(ctx); return err },
			(*outgoingStreamsMap).UpdateUniStreamLimit,
		)
	})
}

func testOutgoingStreamsMapOpenStreamSyncBlockedUntilLimitUpdate(
	t *testing.T,
	openStream func(*outgoingStreamsMap) error,
	openStreamSync func(*outgoingStreamsMap, context.Context) error,
	updateLimit func(*outgoingStreamsMap, uint64),
) {
	clientConn, _ := newConnPair(t, newUDPConnLocalhost(t), newUDPConnLocalhost(t))
	streams := newOutgoingStreamsMap(clientConn, 42)
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

	updateLimit(streams, 1)

	select {
	case err := <-errChan:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}

	err := openStream(streams)
	var limitErr *StreamLimitReachedError
	require.ErrorAs(t, err, &limitErr)

	go func() { errChan <- openStreamSync(streams, ctx) }()
	select {
	case <-errChan:
		t.Fatal("should not have opened stream")
	case <-time.After(scaleDuration(10 * time.Millisecond)):
	}
}

func TestOutgoingStreamsMapOpenStreamSyncOrder(t *testing.T) {
	clientConn, _ := newConnPair(t, newUDPConnLocalhost(t), newUDPConnLocalhost(t))
	streams := newOutgoingStreamsMap(clientConn, 42)
	streams.bidiLimit.MaxStreams = 0

	const numStreams = 16
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

	for i := range numStreams {
		streams.UpdateBidiStreamLimit(uint64(i + 1))
		select {
		case r := <-resultChan:
			require.NoError(t, r.err)
			require.Equal(t, i, r.index)
		case <-time.After(time.Second):
			t.Fatal("timeout")
		}
	}
}
