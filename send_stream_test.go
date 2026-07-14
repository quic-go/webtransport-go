package webtransport

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func TestSendStreamWriteDuringSessionGoneAndCloseSession(t *testing.T) {
	sendStr, recvStr := newUniStreamPair(t)

	sm := newOutgoingUniStreamsMap(nil, 0, maxOutgoingStreams, func(c capsule) {
		t.Fatalf("unexpected capsule: %#v", c)
	})
	str := newSendStream(sendStr, nil, func() { sm.removeStream(sendStr.StreamID()) })
	sm.mx.Lock()
	sm.m[sendStr.StreamID()] = str
	sm.mx.Unlock()

	// write in a loop
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

	// the remote peer sends a WT_SESSION_GONE
	recvStr.CancelRead(WTSessionGoneErrorCode)

	// Write() should block, waiting for closeWithSession()
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
		t.Fatal("Write() should not hang after CloseSession()")
	}
}

type blockingHeaderStream struct {
	unblock chan struct{}
}

func (s *blockingHeaderStream) Write(b []byte) (int, error) {
	if string(b) == "hdr" {
		<-s.unblock
	}
	return len(b), nil
}

func (*blockingHeaderStream) Close() error                     { return nil }
func (*blockingHeaderStream) CancelWrite(quic.StreamErrorCode) {}
func (*blockingHeaderStream) Context() context.Context         { return context.Background() }
func (*blockingHeaderStream) SetWriteDeadline(time.Time) error { return nil }
func (*blockingHeaderStream) SetReliableBoundary()             {}

func TestSendStreamWriteWhileSendingHeaderAsync(t *testing.T) {
	const errorCode StreamErrorCode = 42

	testCases := []struct {
		name        string
		stop        func(*SendStream) error
		expectedErr error
	}{
		{
			name:        "cancel",
			stop:        func(s *SendStream) error { s.CancelWrite(errorCode); return nil },
			expectedErr: &StreamError{ErrorCode: errorCode},
		},
		{
			name:        "close",
			stop:        (*SendStream).Close,
			expectedErr: errors.New("write on closed stream"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			qstr := &blockingHeaderStream{unblock: make(chan struct{})}
			str := newSendStream(qstr, []byte("hdr"), func() {})

			require.NoError(t, tc.stop(str))
			n, err := str.Write([]byte("payload"))
			close(qstr.unblock)
			require.Zero(t, n)
			require.Equal(t, tc.expectedErr, err)
		})
	}
}
