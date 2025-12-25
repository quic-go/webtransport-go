package webtransport

import (
	"context"
	"io"
	"sync"
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

	client, server := newConnPair(t)
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

// mockSendStreamWithReliableBoundary is a mock that implements quicSendStream with SetReliableBoundary
type mockSendStreamWithReliableBoundary struct {
	quicSendStream
	reliableBoundaryCalled bool
	mu                     sync.Mutex
}

func (m *mockSendStreamWithReliableBoundary) SetReliableBoundary() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reliableBoundaryCalled = true
}

func (m *mockSendStreamWithReliableBoundary) WasReliableBoundaryCalled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reliableBoundaryCalled
}

func TestSendStreamHeaderReliableBoundary(t *testing.T) {
	t.Run("SetReliableBoundary is called after header is sent", func(t *testing.T) {
		clientStr, _ := newUniStreamPair(t)
		
		// Wrap the real stream with our mock to track SetReliableBoundary calls
		mockStr := &mockSendStreamWithReliableBoundary{
			quicSendStream: clientStr,
		}
		
		// Create a SendStream with a header
		header := []byte{0x41, 0x00} // Example header
		str := newSendStream(mockStr, header, func() {})
		
		// Write some data, which should trigger header sending and SetReliableBoundary
		_, err := str.Write([]byte("test data"))
		require.NoError(t, err)
		
		// Verify SetReliableBoundary was called
		require.True(t, mockStr.WasReliableBoundaryCalled(), "SetReliableBoundary should be called after sending header")
	})

	t.Run("SetReliableBoundary is called only once even with multiple writes", func(t *testing.T) {
		clientStr, _ := newUniStreamPair(t)
		
		mockStr := &mockSendStreamWithReliableBoundary{
			quicSendStream: clientStr,
		}
		
		header := []byte{0x41, 0x00}
		str := newSendStream(mockStr, header, func() {})
		
		// First write
		_, err := str.Write([]byte("first"))
		require.NoError(t, err)
		require.True(t, mockStr.WasReliableBoundaryCalled())
		
		// Reset the flag to check it's not called again
		mockStr.mu.Lock()
		mockStr.reliableBoundaryCalled = false
		mockStr.mu.Unlock()
		
		// Second write should not call SetReliableBoundary again
		_, err = str.Write([]byte("second"))
		require.NoError(t, err)
		require.False(t, mockStr.WasReliableBoundaryCalled(), "SetReliableBoundary should only be called once")
	})

	t.Run("SetReliableBoundary is called when Close is called without Write", func(t *testing.T) {
		clientStr, _ := newUniStreamPair(t)
		
		mockStr := &mockSendStreamWithReliableBoundary{
			quicSendStream: clientStr,
		}
		
		header := []byte{0x41, 0x00}
		str := newSendStream(mockStr, header, func() {})
		
		// Close without writing
		err := str.Close()
		require.NoError(t, err)
		
		// Verify SetReliableBoundary was called
		require.True(t, mockStr.WasReliableBoundaryCalled(), "SetReliableBoundary should be called on Close")
	})
}
