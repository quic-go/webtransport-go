package webtransport

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/lucas-clemente/quic-go"
	"github.com/stretchr/testify/require"
)

//go:generate sh -c "mockgen -package webtransport -destination mock_stream_creator_test.go github.com/lucas-clemente/quic-go/http3 StreamCreator"
//go:generate sh -c "mockgen -package webtransport -destination mock_stream_test.go github.com/lucas-clemente/quic-go Stream && cat mock_stream_test.go | sed s@protocol\\.StreamID@quic.StreamID@g | sed s@qerr\\.StreamErrorCode@quic.StreamErrorCode@g > tmp.go && mv tmp.go mock_stream_test.go && goimports -w mock_stream_test.go"

type mockRequestStream struct {
	*MockStream
	c chan struct{}
}

func newMockRequestStream(ctrl *gomock.Controller) quic.Stream {
	str := NewMockStream(ctrl)
	str.EXPECT().Close()
	str.EXPECT().CancelRead(gomock.Any())
	return &mockRequestStream{MockStream: str, c: make(chan struct{})}
}

var _ io.ReadWriteCloser = &mockRequestStream{}

func (s *mockRequestStream) Close() error {
	s.MockStream.Close()
	close(s.c)
	return nil
}

func (s *mockRequestStream) Read(b []byte) (int, error)  { <-s.c; return 0, io.EOF }
func (s *mockRequestStream) Write(b []byte) (int, error) { return len(b), nil }

func TestCloseStreamsOnClose(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockSess := NewMockStreamCreator(ctrl)
	mockSess.EXPECT().Context().Return(context.WithValue(context.Background(), quic.ConnectionTracingKey, uint64(1337)))
	sess := newSession(42, mockSess, newMockRequestStream(ctrl))

	str := NewMockStream(ctrl)
	str.EXPECT().StreamID().Return(quic.StreamID(4)).AnyTimes()
	mockSess.EXPECT().OpenStream().Return(str, nil)
	_, err := sess.OpenStream()
	require.NoError(t, err)
	ustr := NewMockStream(ctrl)
	ustr.EXPECT().StreamID().Return(quic.StreamID(5)).AnyTimes()
	mockSess.EXPECT().OpenUniStream().Return(ustr, nil)
	_, err = sess.OpenUniStream()
	require.NoError(t, err)

	str.EXPECT().CancelRead(sessionCloseErrorCode)
	str.EXPECT().CancelWrite(sessionCloseErrorCode)
	ustr.EXPECT().CancelWrite(sessionCloseErrorCode)
	require.NoError(t, sess.CloseWithError(0, ""))
}

func TestOpenStreamSyncCancel(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockSess := NewMockStreamCreator(ctrl)
	mockSess.EXPECT().Context().Return(context.WithValue(context.Background(), quic.ConnectionTracingKey, uint64(1337)))
	sess := newSession(42, mockSess, newMockRequestStream(ctrl))
	defer sess.CloseWithError(0, "")

	str := NewMockStream(ctrl)
	str.EXPECT().StreamID().Return(quic.StreamID(4)).AnyTimes()
	mockSess.EXPECT().OpenStreamSync(gomock.Any()).DoAndReturn(func(ctx context.Context) (quic.Stream, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})

	ctx, cancel := context.WithCancel(context.Background())
	errChan := make(chan error)
	go func() {
		str, err := sess.OpenStreamSync(ctx)
		require.Nil(t, str)
		errChan <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout")
	case err := <-errChan:
		require.Error(t, err)
		require.ErrorIs(t, err, context.Canceled)
	}
}

func TestAddStreamAfterSessionClose(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockSess := NewMockStreamCreator(ctrl)
	mockSess.EXPECT().Context().Return(context.WithValue(context.Background(), quic.ConnectionTracingKey, uint64(1337)))

	sess := newSession(42, mockSess, newMockRequestStream(ctrl))
	require.NoError(t, sess.CloseWithError(0, ""))

	str := NewMockStream(ctrl)
	str.EXPECT().CancelRead(sessionCloseErrorCode)
	str.EXPECT().CancelWrite(sessionCloseErrorCode)
	sess.addIncomingStream(str)

	ustr := NewMockStream(ctrl)
	ustr.EXPECT().CancelRead(sessionCloseErrorCode)
	sess.addIncomingUniStream(ustr)
}
