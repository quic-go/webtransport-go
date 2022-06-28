package webtransport

import (
	"io"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/lucas-clemente/quic-go"
	"github.com/stretchr/testify/require"
)

//go:generate sh -c "mockgen -package webtransport -destination mock_stream_creator_test.go github.com/lucas-clemente/quic-go/http3 StreamCreator"
//go:generate sh -c "mockgen -package webtransport -destination mock_stream_test.go github.com/lucas-clemente/quic-go Stream && cat mock_stream_test.go | sed s@protocol\\.StreamID@quic.StreamID@g | sed s@qerr\\.StreamErrorCode@quic.StreamErrorCode@g > tmp.go && mv tmp.go mock_stream_test.go && goimports -w mock_stream_test.go"

type mockRequestStream struct {
	c      chan struct{}
	closed bool
}

func newMockRequestStream() *mockRequestStream {
	return &mockRequestStream{c: make(chan struct{})}
}

var _ io.ReadWriteCloser = &mockRequestStream{}

func (s *mockRequestStream) Close() error {
	if !s.closed {
		close(s.c)
	}
	s.closed = true
	return nil
}

func (s *mockRequestStream) Read(b []byte) (int, error)  { <-s.c; return 0, io.EOF }
func (s *mockRequestStream) Write(b []byte) (int, error) { return len(b), nil }

func TestCloseStreamsOnClose(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockSess := NewMockStreamCreator(ctrl)
	sess := newSession(42, mockSess, newMockRequestStream())

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
	require.NoError(t, sess.Close())
}

func TestAddStreamAfterSessionClose(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	sess := newSession(42, NewMockStreamCreator(ctrl), newMockRequestStream())
	require.NoError(t, sess.Close())

	str := NewMockStream(ctrl)
	str.EXPECT().CancelRead(sessionCloseErrorCode)
	str.EXPECT().CancelWrite(sessionCloseErrorCode)
	sess.addIncomingStream(str)

	ustr := NewMockStream(ctrl)
	ustr.EXPECT().CancelRead(sessionCloseErrorCode)
	sess.addIncomingUniStream(ustr)
}
