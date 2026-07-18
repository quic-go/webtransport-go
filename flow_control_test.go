package webtransport

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOutgoingDataFlowController(t *testing.T) {
	fc := newOutgoingDataFlowController(10)
	require.Equal(t, int64(4), fc.AddBytesSent(4))
	blocked, maxData := fc.IsNewlyBlocked()
	require.False(t, blocked)
	require.Zero(t, maxData)

	require.Equal(t, int64(6), fc.AddBytesSent(10))
	blocked, maxData = fc.IsNewlyBlocked()
	require.True(t, blocked)
	require.Equal(t, int64(10), maxData)

	require.Equal(t, int64(0), fc.AddBytesSent(1))
	blocked, maxData = fc.IsNewlyBlocked()
	require.False(t, blocked)
	require.Zero(t, maxData)

	updated := fc.NextUpdate()
	require.ErrorIs(t, fc.UpdateMaxData(10), errMaxDataNotIncreased)
	select {
	case <-updated:
		t.Fatal("rejected update notified waiter")
	default:
	}

	require.NoError(t, fc.UpdateMaxData(20))
	select {
	case <-updated:
	default:
		t.Fatal("increased limit didn't notify waiter")
	}
	require.Equal(t, int64(10), fc.AddBytesSent(20))
}

func TestOutgoingDataFlowControllerBlockedAtZero(t *testing.T) {
	fc := newOutgoingDataFlowController(0)

	blocked, maxData := fc.IsNewlyBlocked()
	require.True(t, blocked)
	require.Zero(t, maxData)

	blocked, _ = fc.IsNewlyBlocked()
	require.False(t, blocked)
}

func TestIncomingDataFlowController(t *testing.T) {
	var updates []int64
	fc := newIncomingDataFlowController(0, 8, func(maxData int64) { updates = append(updates, maxData) })

	require.NoError(t, fc.AddBytesRead(1))
	require.Empty(t, updates)
	require.NoError(t, fc.AddBytesRead(1))
	require.Equal(t, []int64{10}, updates)
	require.Error(t, fc.AddBytesRead(9))
}
