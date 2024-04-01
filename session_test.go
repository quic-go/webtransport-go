package webtransport

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

//go:generate sh -c "go run go.uber.org/mock/mockgen -package webtransport -destination mock_stream_creator_test.go github.com/quic-go/quic-go/http3 StreamCreator"
//go:generate sh -c "go run go.uber.org/mock/mockgen -package webtransport -destination mock_stream_test.go github.com/quic-go/quic-go Stream && cat mock_stream_test.go | sed s@protocol\\.StreamID@quic.StreamID@g | sed s@qerr\\.StreamErrorCode@quic.StreamErrorCode@g > tmp.go && mv tmp.go mock_stream_test.go && goimports -w mock_stream_test.go"

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

	mockSess := NewMockStreamCreator(ctrl)
	mockSess.EXPECT().Context().Return(context.WithValue(context.Background(), quic.ConnectionTracingKey, quic.ConnectionTracingID(1337)))
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
	mockSess.EXPECT().Context().Return(context.WithValue(context.Background(), quic.ConnectionTracingKey, quic.ConnectionTracingID(1337)))
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
	mockSess.EXPECT().Context().Return(context.WithValue(context.Background(), quic.ConnectionTracingKey, quic.ConnectionTracingID(1337)))

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

func TestOpenStreamAfterSessionClose(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockSess := NewMockStreamCreator(ctrl)
	mockSess.EXPECT().Context().Return(context.WithValue(context.Background(), quic.ConnectionTracingKey, quic.ConnectionTracingID(1337)))
	wait := make(chan struct{})
	streamOpen := make(chan struct{})
	mockSess.EXPECT().OpenStreamSync(gomock.Any()).DoAndReturn(func(context.Context) (quic.Stream, error) {
		streamOpen <- struct{}{}
		str := NewMockStream(ctrl)
		str.EXPECT().CancelRead(sessionCloseErrorCode)
		str.EXPECT().CancelWrite(sessionCloseErrorCode)
		<-wait
		return str, nil
	})

	sess := newSession(42, mockSess, newMockRequestStream(ctrl))

	errChan := make(chan error, 1)
	go func() {
		_, err := sess.OpenStreamSync(context.Background())
		errChan <- err
	}()
	<-streamOpen

	require.NoError(t, sess.CloseWithError(0, "session closed"))

	close(wait)
	require.EqualError(t, <-errChan, "session closed")
}

func TestOpenUniStreamAfterSessionClose(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockSess := NewMockStreamCreator(ctrl)
	mockSess.EXPECT().Context().Return(context.WithValue(context.Background(), quic.ConnectionTracingKey, quic.ConnectionTracingID(1337)))
	wait := make(chan struct{})
	streamOpen := make(chan struct{})
	mockSess.EXPECT().OpenUniStreamSync(gomock.Any()).DoAndReturn(func(context.Context) (quic.SendStream, error) {
		streamOpen <- struct{}{}
		str := NewMockStream(ctrl)
		str.EXPECT().CancelWrite(sessionCloseErrorCode)
		<-wait
		return str, nil
	})

	sess := newSession(42, mockSess, newMockRequestStream(ctrl))

	errChan := make(chan error, 1)
	go func() {
		_, err := sess.OpenUniStreamSync(context.Background())
		errChan <- err
	}()
	<-streamOpen

	require.NoError(t, sess.CloseWithError(0, "session closed"))

	close(wait)
	require.EqualError(t, <-errChan, "session closed")
}
