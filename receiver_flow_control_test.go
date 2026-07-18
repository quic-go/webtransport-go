package webtransport

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIncomingDataFlowControl(t *testing.T) {
	sendStr, recvStr := newUniStreamPair(t)
	updates := make(chan uint64, 2)
	fc := newIncomingDataFlowController(
		0,
		8,
		func(maxData uint64) { updates <- maxData },
	)
	requireUpdate := func(want uint64) {
		t.Helper()
		select {
		case update := <-updates:
			require.Equal(t, want, update)
		case <-time.After(time.Second):
			t.Fatal("flow control window wasn't updated")
		}
	}

	require.NoError(t, fc.AddBytesRead(1))
	select {
	case <-updates:
		t.Fatal("flow control window updated too early")
	default:
	}
	require.NoError(t, fc.AddBytesRead(1))
	requireUpdate(10)

	str := newReceiveStream(recvStr, 3, func() {}, fc, func(error) {}) // newUniStreamPair already consumed a 3-byte stream header

	_, err := sendStr.Write([]byte("1"))
	require.NoError(t, err)
	n, err := str.Read(make([]byte, 1))
	require.Equal(t, 1, n)
	require.NoError(t, err)

	_, err = sendStr.Write([]byte("234567"))
	require.NoError(t, err)
	_, err = recvStr.Peek(make([]byte, 6))
	require.NoError(t, err)
	sendStr.CancelWrite(webtransportCodeToHTTPCode(1))
	requireUpdate(17)
	n, err = str.Read(make([]byte, 8))
	require.Zero(t, n)
	var streamErr *StreamError
	require.ErrorAs(t, err, &streamErr)

	require.Error(t, fc.AddBytesRead(9))
}

func TestIncomingDataFlowControlExcludesStreamHeader(t *testing.T) {
	fc := newIncomingDataFlowController(0, 0, func(uint64) {
		t.Fatal("stream header triggered a flow control update")
	})
	var flowControlErr error
	str := &ReceiveStream{
		fc:                 fc,
		bytesRead:          3,
		onFlowControlError: func(err error) { flowControlErr = err },
	}

	str.onReceiveFinalSize(3)
	require.NoError(t, flowControlErr)
	require.Zero(t, fc.bytesRead)
}
