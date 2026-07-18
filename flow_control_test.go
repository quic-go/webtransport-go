package webtransport

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOutgoingDataFlowController(t *testing.T) {
	fc := newOutgoingDataFlowController(10)
	require.Equal(t, uint64(4), fc.AddBytesSent(4))
	require.Equal(t, uint64(6), fc.AddBytesSent(10))
	require.Equal(t, uint64(0), fc.AddBytesSent(1))
}
