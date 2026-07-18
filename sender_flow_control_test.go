package webtransport

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/stretchr/testify/require"
)

type fakeQuicSendStream struct {
	ctx    context.Context
	cancel context.CancelFunc

	bytes.Buffer
}

func newFakeQuicSendStream() *fakeQuicSendStream {
	ctx, cancel := context.WithCancel(context.Background())
	return &fakeQuicSendStream{ctx: ctx, cancel: cancel}
}

func (s *fakeQuicSendStream) Close() error                     { return nil }
func (s *fakeQuicSendStream) CancelWrite(quic.StreamErrorCode) { s.cancel() }
func (s *fakeQuicSendStream) Context() context.Context         { return s.ctx }
func (s *fakeQuicSendStream) SetWriteDeadline(time.Time) error { return nil }
func (s *fakeQuicSendStream) SetReliableBoundary()             {}
func (s *fakeQuicSendStream) WriteWithLimit(p []byte, limiter func(int) int) (int, error) {
	n, _ := s.Write(p[:limiter(len(p))])
	if n < len(p) {
		return n, quic.ErrWriteLimitReached
	}
	return n, nil
}

func TestSendStreamDataFlowControl(t *testing.T) {
	fc := newOutgoingDataFlowController(5)
	qstr := newFakeQuicSendStream()
	var capsules []capsule
	str := newSendStream(qstr, []byte("hdr"), func(c capsule) { capsules = append(capsules, c) }, func() {})
	str.fc = fc

	n, err := str.Write([]byte("abcde"))
	require.Equal(t, 5, n)
	require.NoError(t, err)

	require.NoError(t, str.SetWriteDeadline(time.Now().Add(scaleDuration(20*time.Millisecond))))
	n, err = str.Write([]byte("fgh"))
	require.Zero(t, n)
	require.ErrorIs(t, err, os.ErrDeadlineExceeded)
	require.Equal(t, "hdrabcde", qstr.String())
	require.Equal(t, []capsule{dataBlockedCapsule{MaximumData: 5}}, capsules)
}

func TestOutgoingDataFlowControlAddsBytesSent(t *testing.T) {
	fc := newOutgoingDataFlowController(5)
	added := fc.AddBytesSent(5)
	require.Equal(t, uint64(5), added)
	added = fc.AddBytesSent(1)
	require.Zero(t, added)
}

func TestOutgoingDataFlowControlReportsBlockedOnce(t *testing.T) {
	fc := newOutgoingDataFlowController(5)
	require.Equal(t, uint64(5), fc.AddBytesSent(5))

	blocked, maxData := fc.IsNewlyBlocked()
	require.True(t, blocked)
	require.Equal(t, uint64(5), maxData)
	blocked, _ = fc.IsNewlyBlocked()
	require.False(t, blocked)
}

func TestOutgoingDataFlowControlUpdatesMaxData(t *testing.T) {
	fc := newOutgoingDataFlowController(5)
	require.Equal(t, uint64(5), fc.AddBytesSent(5))
	updated := fc.NextUpdate()

	require.NoError(t, fc.UpdateMaxData(8))
	select {
	case <-updated:
	default:
		t.Fatal("max data update didn't wake blocked writers")
	}
	require.Equal(t, uint64(3), fc.AddBytesSent(3))
	require.ErrorIs(t, fc.UpdateMaxData(8), errMaxDataNotIncreased)
}
