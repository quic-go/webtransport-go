package webtransport

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOutgoingDataFlowController(t *testing.T) {
	fc := newOutgoingDataFlowController(10)
	require.Equal(t, uint64(4), fc.AddBytesSent(4))
	blocked, maxData := fc.IsNewlyBlocked()
	require.False(t, blocked)
	require.Zero(t, maxData)

	require.Equal(t, uint64(6), fc.AddBytesSent(10))
	blocked, maxData = fc.IsNewlyBlocked()
	require.True(t, blocked)
	require.Equal(t, uint64(10), maxData)

	require.Equal(t, uint64(0), fc.AddBytesSent(1))
	blocked, maxData = fc.IsNewlyBlocked()
	require.False(t, blocked)
	require.Zero(t, maxData)
}
