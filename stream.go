package webtransport

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/lucas-clemente/quic-go"
)

type SendStream interface {
	io.Writer
	io.Closer

	CancelWrite(ErrorCode)

	SetWriteDeadline(time.Time) error
}

type ReceiveStream interface {
	io.Reader
	CancelRead(ErrorCode)

	SetReadDeadline(time.Time) error
}

type Stream interface {
	SendStream
	ReceiveStream

	SetDeadline(time.Time) error
}

type sendStream struct {
	str quic.SendStream
}

var _ SendStream = &sendStream{}

func newSendStream(str quic.SendStream) SendStream {
	return &sendStream{str: str}
}

func (s *sendStream) Write(b []byte) (int, error) {
	n, err := s.str.Write(b)
	return n, maybeConvertStreamError(err)
}

func (s *sendStream) CancelWrite(e ErrorCode) {
	s.str.CancelWrite(webtransportCodeToHTTPCode(e))
}

func (s *sendStream) Close() error {
	return maybeConvertStreamError(s.str.Close())
}

func (s *sendStream) SetWriteDeadline(t time.Time) error {
	return maybeConvertStreamError(s.str.SetWriteDeadline(t))
}

type receiveStream struct {
	str quic.ReceiveStream
}

var _ ReceiveStream = &receiveStream{}

func newReceiveStream(str quic.ReceiveStream) ReceiveStream {
	return &receiveStream{str: str}
}

func (s *receiveStream) Read(b []byte) (int, error) {
	n, err := s.str.Read(b)
	return n, maybeConvertStreamError(err)
}

func (s *receiveStream) CancelRead(e ErrorCode) {
	s.str.CancelRead(webtransportCodeToHTTPCode(e))
}

func (s *receiveStream) SetReadDeadline(t time.Time) error {
	return maybeConvertStreamError(s.str.SetReadDeadline(t))
}

type stream struct {
	SendStream
	ReceiveStream
}

var _ Stream = &stream{}

func newStream(str quic.Stream) Stream {
	return &stream{
		SendStream:    &sendStream{str: str},
		ReceiveStream: &receiveStream{str: str},
	}
}

func maybeConvertStreamError(err error) error {
	if err == nil {
		return nil
	}
	var streamErr *quic.StreamError
	if errors.As(err, &streamErr) {
		errorCode, cerr := httpCodeToWebtransportCode(streamErr.ErrorCode)
		if cerr != nil {
			return fmt.Errorf("stream reset, but failed to convert stream error %d: %w", streamErr.ErrorCode, cerr)
		}
		return &StreamError{ErrorCode: errorCode}
	}
	return err
}

func (s *stream) SetDeadline(t time.Time) error {
	// TODO: implement
	return nil
	// return maybeConvertStreamError(s.SendStream.SetDeadline(t))
}
