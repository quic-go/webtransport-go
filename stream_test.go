package webtransport

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/stretchr/testify/require"
)

func newUniStreamPair(t *testing.T) (*quic.SendStream, *quic.ReceiveStream) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	client, server := newConnPair(t)
	t.Cleanup(func() { client.CloseWithError(0, "") })

	clientStr, err := client.OpenUniStreamSync(ctx)
	require.NoError(t, err)
	clientStr.Write([]byte("foo"))

	serverStr, err := server.AcceptUniStream(ctx)
	require.NoError(t, err)

	return clientStr, serverStr
}

func TestSendStreamClose(t *testing.T) {
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
			recvStr.CancelRead(tc.quicErrorCode)
			str := newSendStream(sendStr, nil, func() {})

			// eventually, the stream reset will be received and the write will fail
			var writeErr error
			for {
				_, writeErr = str.Write([]byte("foo"))
				if writeErr != nil {
					break
				}
				time.Sleep(time.Millisecond)
			}

			if tc.expectedErr != nil {
				require.ErrorIs(t, writeErr, tc.expectedErr)
			} else {
				require.ErrorContains(t, writeErr, tc.errMsgContains)
			}
		})
	}
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
			str := newReceiveStream(recvStr, func() {})

			_, readErr := io.ReadFull(str, make([]byte, 100))

			if tc.expectedErr != nil {
				require.ErrorIs(t, readErr, tc.expectedErr)
			} else {
				require.ErrorContains(t, readErr, tc.errMsgContains)
			}
		})
	}
}
