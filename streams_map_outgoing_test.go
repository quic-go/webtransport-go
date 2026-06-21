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
