package webtransport

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"
)

type outgoingStreamForTests struct{}

func (*outgoingStreamForTests) closeWithSession(error) {}

func TestOutgoingStreamsMapOpenStreamSyncClose(t *testing.T) {
	var nextStreamID atomic.Uint64
	openStream := func() (*outgoingStreamForTests, quic.StreamID, error) {
		return &outgoingStreamForTests{}, quic.StreamID(nextStreamID.Add(1)), nil
	}
	openStreamSyncCalled := make(chan struct{}, 1)
	streams := newOutgoingStreamsMap(
		openStream,
		func(ctx context.Context) (*outgoingStreamForTests, quic.StreamID, error) {
			openStreamSyncCalled <- struct{}{}
			<-ctx.Done()
			return nil, invalidStreamID, ctx.Err()
		},
		func(uint64) {},
	)
	streams.maxStreams = 1

	errChan := make(chan error, 1)
	go func() {
		_, err := streams.OpenStreamSync(context.Background())
		errChan <- err
	}()
	select {
	case <-openStreamSyncCalled:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for quic OpenStreamSync call")
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

func TestOutgoingStreamsMapOpenStreamSyncBlocksAtLimit(t *testing.T) {
	var nextStreamID atomic.Uint64
	//nolint:unparam // ignore unused error return value
	openStream := func() (*outgoingStreamForTests, quic.StreamID, error) {
		return &outgoingStreamForTests{}, quic.StreamID(nextStreamID.Add(1)), nil
	}
	blocked := make(chan uint64, 10)
	openStreamSyncCalled := make(chan struct{}, 1)
	streams := newOutgoingStreamsMap(
		openStream,
		func(context.Context) (*outgoingStreamForTests, quic.StreamID, error) {
			openStreamSyncCalled <- struct{}{}
			return openStream()
		},
		func(limit uint64) { blocked <- limit },
	)
	streams.maxStreams = 0

	ctx, cancel := context.WithCancel(context.Background())
	errChan := make(chan error, 1)
	go func() {
		_, err := streams.OpenStreamSync(ctx)
		errChan <- err
	}()
	select {
	case limit := <-blocked:
		require.Equal(t, uint64(0), limit)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for blocked capsule")
	}
	select {
	case err := <-errChan:
		t.Fatalf("OpenStreamSync returned while blocked: %v", err)
	default:
	}
	select {
	case <-openStreamSyncCalled:
		t.Fatal("quic OpenStreamSync called while blocked by WebTransport stream limit")
	default:
	}

	_, err := streams.OpenStream()
	var limitErr *StreamLimitReachedError
	require.ErrorAs(t, err, &limitErr)
	select {
	case limit := <-blocked:
		t.Fatalf("unexpected blocked capsule for limit %d", limit)
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

func TestOutgoingStreamsMapBlockedCapsules(t *testing.T) {
	var nextStreamID atomic.Uint64
	//nolint:unparam // ignore unused error return value
	openStream := func() (*outgoingStreamForTests, quic.StreamID, error) {
		return &outgoingStreamForTests{}, quic.StreamID(nextStreamID.Add(1)), nil
	}
	blocked := make(chan uint64, 10)
	openStreamSyncCalled := make(chan quic.StreamID, 10)
	releaseOpenStreamSync := make(chan struct{})
	streams := newOutgoingStreamsMap(
		openStream,
		func(ctx context.Context) (*outgoingStreamForTests, quic.StreamID, error) {
			str, id, err := openStream()
			if err != nil {
				return nil, invalidStreamID, err
			}
			openStreamSyncCalled <- id
			select {
			case <-releaseOpenStreamSync:
				return str, id, nil
			case <-ctx.Done():
				return nil, invalidStreamID, ctx.Err()
			}
		},
		func(limit uint64) { blocked <- limit },
	)
	streams.maxStreams = 3
	for range 3 {
		_, err := streams.OpenStream()
		require.NoError(t, err)
	}
	_, err := streams.OpenStream()
	var limitErr *StreamLimitReachedError
	require.ErrorAs(t, err, &limitErr)
	select {
	case limit := <-blocked:
		require.Equal(t, uint64(3), limit)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for blocked capsule")
	}

	// further calls to OpenStream don't trigger new blocked capsules
	for range 5 {
		_, err = streams.OpenStream()
		require.ErrorAs(t, err, &limitErr)
	}
	select {
	case limit := <-blocked:
		t.Fatalf("unexpected blocked capsule for limit %d", limit)
	default:
	}

	errChan := make(chan error, 3)
	for range 3 {
		go func() {
			_, err := streams.OpenStreamSync(context.Background())
			errChan <- err
		}()
	}
	select {
	case limit := <-blocked:
		t.Fatalf("unexpected blocked capsule for limit %d", limit)
	default:
	}
	select {
	case err := <-errChan:
		t.Fatalf("OpenStreamSync returned while blocked: %v", err)
	default:
	}

	// allow 2 more streams
	streams.UpdateStreamLimit(5)
	for range 2 {
		select {
		case <-openStreamSyncCalled:
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for quic OpenStreamSync call")
		}
	}
	select {
	case limit := <-blocked:
		require.Equal(t, uint64(5), limit)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for blocked capsule")
	}
	select {
	case id := <-openStreamSyncCalled:
		t.Fatalf("too many quic OpenStreamSync calls reached: %d", id)
	default:
	}
	select {
	case err := <-errChan:
		t.Fatalf("OpenStreamSync returned while quic OpenStreamSync was blocked: %v", err)
	default:
	}

	streams.UpdateStreamLimit(6)
	select {
	case <-openStreamSyncCalled:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for quic OpenStreamSync call")
	}
	select {
	case limit := <-blocked:
		t.Fatalf("unexpected blocked capsule for limit %d", limit)
	default:
	}
	select {
	case err := <-errChan:
		t.Fatalf("OpenStreamSync returned while quic OpenStreamSync was blocked: %v", err)
	default:
	}

	close(releaseOpenStreamSync)
	for range 3 {
		select {
		case err := <-errChan:
			require.NoError(t, err)
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for stream")
		}
	}
}

func TestOutgoingStreamsMapQueueBlockedCanCloseSession(t *testing.T) {
	var nextStreamID atomic.Uint64
	//nolint:unparam // ignore unused error return value
	openStream := func() (*outgoingStreamForTests, quic.StreamID, error) {
		return &outgoingStreamForTests{}, quic.StreamID(nextStreamID.Add(1)), nil
	}
	sessionErr := &SessionError{ErrorCode: 1337, Message: "closing"}
	var streams *outgoingStreamsMap[*outgoingStreamForTests]
	streams = newOutgoingStreamsMap(
		openStream,
		func(context.Context) (*outgoingStreamForTests, quic.StreamID, error) { return openStream() },
		func(uint64) { streams.CloseSession(sessionErr) },
	)
	streams.maxStreams = 0

	errChan := make(chan error, 1)
	go func() {
		_, err := streams.OpenStream()
		errChan <- err
	}()

	select {
	case err := <-errChan:
		var limitErr *StreamLimitReachedError
		require.ErrorAs(t, err, &limitErr)
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}
