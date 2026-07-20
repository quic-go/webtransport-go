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
}

func TestOutgoingDataFlowControllerBlockedAtZero(t *testing.T) {
	fc := newOutgoingDataFlowController(0)

	blocked, maxData := fc.IsNewlyBlocked()
	require.True(t, blocked)
	require.Zero(t, maxData)

	blocked, _ = fc.IsNewlyBlocked()
	require.False(t, blocked)
}
