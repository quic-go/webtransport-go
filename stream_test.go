package webtransport

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newUniStreamPair(t *testing.T) (*quic.SendStream, *quic.ReceiveStream) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	client, server := newConnPair(t, newUDPConnLocalhost(t), newUDPConnLocalhost(t))
	t.Cleanup(func() { client.CloseWithError(0, "") })

	clientStr, err := client.OpenUniStreamSync(ctx)
	require.NoError(t, err)
	clientStr.Write([]byte("foo"))

	serverStr, err := server.AcceptUniStream(ctx)
	require.NoError(t, err)
	_, err = io.ReadFull(serverStr, make([]byte, 3))
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

func TestSendStreamSessionGone(t *testing.T) {
	sendStr, recvStr := newUniStreamPair(t)
	str := newSendStream(sendStr, nil, func() {})

	// simulate remote side sending WTSessionGoneErrorCode
	recvStr.CancelRead(WTSessionGoneErrorCode)

	errChan := make(chan error, 1)
	go func() {
		for {
			if _, err := str.Write([]byte("foo")); err != nil {
				errChan <- err
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	select {
	case <-errChan:
		t.Fatal("Write should be blocking")
	case <-time.After(scaleDuration(10 * time.Millisecond)):
	}

	str.closeWithSession(assert.AnError)

	select {
	case err := <-errChan:
		require.ErrorIs(t, err, assert.AnError)
	case <-time.After(scaleDuration(10 * time.Millisecond)):
		t.Fatal("Write didn't unblock")
	}
}

func TestSendStreamSessionGoneDeadline(t *testing.T) {
	t.Run("deadline expires while waiting", func(t *testing.T) {
		sendStr, recvStr := newUniStreamPair(t)
		str := newSendStream(sendStr, nil, func() {})

		require.NoError(t, str.SetWriteDeadline(time.Now().Add(scaleDuration(20*time.Millisecond))))
		recvStr.CancelRead(WTSessionGoneErrorCode)

		errChan := make(chan error, 1)
		go func() {
			for {
				if _, err := str.Write([]byte("foo")); err != nil {
					errChan <- err
					return
				}
				time.Sleep(time.Millisecond)
			}
		}()

		select {
		case err := <-errChan:
			require.True(t, isTimeoutError(err), "expected timeout error, got: %v", err)
		case <-time.After(scaleDuration(100 * time.Millisecond)):
			t.Fatal("Write didn't unblock after deadline")
		}
	})

	t.Run("deadline changed while waiting", func(t *testing.T) {
		sendStr, recvStr := newUniStreamPair(t)
		str := newSendStream(sendStr, nil, func() {})

		recvStr.CancelRead(WTSessionGoneErrorCode)

		errChan := make(chan error, 1)
		go func() {
			for {
				if _, err := str.Write([]byte("foo")); err != nil {
					errChan <- err
					return
				}
				time.Sleep(time.Millisecond)
			}
		}()

		select {
		case <-errChan:
			t.Fatal("Write should be blocking")
		case <-time.After(scaleDuration(10 * time.Millisecond)):
		}

		require.NoError(t, str.SetWriteDeadline(time.Now().Add(scaleDuration(10*time.Millisecond))))

		select {
		case err := <-errChan:
			require.True(t, isTimeoutError(err), "expected timeout error, got: %v", err)
		case <-time.After(scaleDuration(100 * time.Millisecond)):
			t.Fatal("Write didn't unblock after deadline was set")
		}
	})
}

func TestReceiveStreamSessionGone(t *testing.T) {
	sendStr, recvStr := newUniStreamPair(t)
	str := newReceiveStream(recvStr, func() {})

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

func TestReceiveStreamSessionGoneDeadline(t *testing.T) {
	t.Run("deadline expires while waiting", func(t *testing.T) {
		sendStr, recvStr := newUniStreamPair(t)
		str := newReceiveStream(recvStr, func() {})

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
		str := newReceiveStream(recvStr, func() {})

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

func TestSendStreamHeaderRetryAfterDeadlineError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	client, server := newConnPair(t, newUDPConnLocalhost(t), newUDPConnLocalhost(t))

	clientStr, err := client.OpenUniStreamSync(ctx)
	require.NoError(t, err)

	hdr := []byte("test-header")
	str := newSendStream(clientStr, hdr, func() {})

	require.NoError(t, str.SetWriteDeadline(time.Now().Add(-time.Second)))

	_, err = str.Write([]byte("data"))
	require.Error(t, err)
	require.True(t, isTimeoutError(err))

	require.NoError(t, str.SetWriteDeadline(time.Time{}))

	// second write should succeed and include the header
	_, err = str.Write([]byte("data"))
	require.NoError(t, err)
	require.NoError(t, str.Close())

	// verify that the header was written
	serverStr, err := server.AcceptUniStream(ctx)
	require.NoError(t, err)

	data, err := io.ReadAll(serverStr)
	require.NoError(t, err)
	require.Equal(t, append(hdr, []byte("data")...), data)
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
