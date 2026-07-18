package webtransport

import (
	"io"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReceiveStreamSessionGone(t *testing.T) {
	sendStr, recvStr := newUniStreamPair(t)
	str := newReceiveStream(recvStr, 0, func() {}, nil, nil)

	// simulate remote side sending WTSessionGoneErrorCode
	sendStr.CancelWrite(WTSessionGoneErrorCode)

	errChan := make(chan error, 1)
	go func() {
		if _, err := str.Read(make([]byte, 100)); err != nil {
			errChan <- err
			return
		}
	}()

	select {
	case <-errChan:
		t.Fatal("Read should be blocking")
	case <-time.After(scaleDuration(10 * time.Millisecond)):
	}

	str.closeWithSession(assert.AnError)

	select {
	case err := <-errChan:
		require.ErrorIs(t, err, assert.AnError)
	case <-time.After(scaleDuration(10 * time.Millisecond)):
		t.Fatal("Read didn't unblock")
	}
}

func TestReceiveStreamReadDuringSessionGoneAndCloseSession(t *testing.T) {
	sendStr, recvStr := newUniStreamPair(t)

	sm := newIncomingStreamsMap[*ReceiveStream](maxStreamsLimit, nil)
	str := newReceiveStream(recvStr, 0, func() { sm.RemoveStream(recvStr.StreamID()) }, nil, nil)
	require.NoError(t, sm.AddStream(recvStr.StreamID(), str))

	// start reading
	errChan := make(chan error, 1)
	go func() {
		_, err := str.Read(make([]byte, 100))
		errChan <- err
	}()

	// the remote peer sends a WT_SESSION_GONE
	sendStr.CancelWrite(WTSessionGoneErrorCode)

	// Read() should block, waiting for CloseSession()
	select {
	case <-errChan:
		t.Fatal("should not happen")
	case <-time.After(scaleDuration(10 * time.Millisecond)):
	}

	sessionErr := &SessionError{ErrorCode: 42, Message: "bye"}
	sm.CloseSession(sessionErr)

	select {
	case err := <-errChan:
		require.ErrorIs(t, err, sessionErr)
	case <-time.After(scaleDuration(time.Second)):
		t.Fatal("Read() should not hang after CloseSession()")
	}
}

func TestReceiveStreamSessionGoneDeadline(t *testing.T) {
	t.Run("deadline expires while waiting", func(t *testing.T) {
		sendStr, recvStr := newUniStreamPair(t)
		str := newReceiveStream(recvStr, 0, func() {}, nil, nil)

		require.NoError(t, str.SetReadDeadline(time.Now().Add(scaleDuration(20*time.Millisecond))))
		sendStr.CancelWrite(WTSessionGoneErrorCode)

		errChan := make(chan error, 1)
		go func() {
			if _, err := str.Read(make([]byte, 100)); err != nil {
				errChan <- err
				return
			}
		}()

		select {
		case err := <-errChan:
			require.True(t, isTimeoutError(err), "expected timeout error, got: %v", err)
		case <-time.After(scaleDuration(100 * time.Millisecond)):
			t.Fatal("Read didn't unblock after deadline")
		}
	})

	t.Run("deadline changed while waiting", func(t *testing.T) {
		sendStr, recvStr := newUniStreamPair(t)
		str := newReceiveStream(recvStr, 0, func() {}, nil, nil)

		sendStr.CancelWrite(WTSessionGoneErrorCode)

		errChan := make(chan error, 1)
		go func() {
			if _, err := str.Read(make([]byte, 100)); err != nil {
				errChan <- err
				return
			}
		}()

		select {
		case <-errChan:
			t.Fatal("Read should be blocking")
		case <-time.After(scaleDuration(10 * time.Millisecond)):
		}

		require.NoError(t, str.SetReadDeadline(time.Now().Add(scaleDuration(10*time.Millisecond))))

		select {
		case err := <-errChan:
			require.True(t, isTimeoutError(err), "expected timeout error, got: %v", err)
		case <-time.After(scaleDuration(100 * time.Millisecond)):
			t.Fatal("Read didn't unblock after deadline was set")
		}
	})
}

func TestReceiveStreamClose(t *testing.T) {
	testCases := []struct {
		name           string
		quicErrorCode  quic.StreamErrorCode
		expectedErr    error
		errMsgContains string
	}{
		{
			name:           "invalid stream error",
			quicErrorCode:  1337,
			errMsgContains: "stream reset, but failed to convert stream error",
		},
		{
			name:          "valid stream error",
			quicErrorCode: 0x52e5ac983162, // value taken from the draft, corresponds to 0xffffffff
			expectedErr:   &StreamError{ErrorCode: StreamErrorCode(0xffffffff), Remote: true},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			sendStr, recvStr := newUniStreamPair(t)
			sendStr.CancelWrite(tc.quicErrorCode)
			str := newReceiveStream(recvStr, 0, func() {}, nil, nil)

			_, readErr := io.ReadFull(str, make([]byte, 100))

			if tc.expectedErr != nil {
				require.ErrorIs(t, readErr, tc.expectedErr)
			} else {
				require.ErrorContains(t, readErr, tc.errMsgContains)
			}
		})
	}
}

func TestReceiveStreamDataFlowControl(t *testing.T) {
	sendStr, recvStr := newUniStreamPair(t)
	updates := make(chan int64, 1)
	fc := newIncomingDataFlowController(0, 8, func(maxData int64) { updates <- maxData })
	str := newReceiveStream(recvStr, 3, func() {}, fc, func(error) {}) // newUniStreamPair already consumed a 3-byte stream header

	_, err := sendStr.Write([]byte("1"))
	require.NoError(t, err)
	n, err := str.Read(make([]byte, 1))
	require.Equal(t, 1, n)
	require.NoError(t, err)
	require.Equal(t, int64(1), fc.bytesRead)

	_, err = sendStr.Write([]byte("234567"))
	require.NoError(t, err)
	_, err = recvStr.Peek(make([]byte, 6))
	require.NoError(t, err)
	sendStr.CancelWrite(webtransportCodeToHTTPCode(1))
	select {
	case update := <-updates:
		require.Equal(t, int64(15), update)
	case <-time.After(time.Second):
		t.Fatal("flow control window wasn't updated")
	}

	n, err = str.Read(make([]byte, 8))
	require.Zero(t, n)
	var streamErr *StreamError
	require.ErrorAs(t, err, &streamErr)
	require.Equal(t, int64(7), fc.bytesRead)
}
