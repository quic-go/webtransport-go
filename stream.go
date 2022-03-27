package webtransport

import (
	"io"
	"time"

	"github.com/lucas-clemente/quic-go"
)

type ErrorCode uint8

type Stream interface {
	io.Reader
	io.Writer
	io.Closer

	CancelRead(ErrorCode)
	CancelWrite(ErrorCode)

	SetDeadline(time.Time) error
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

type stream struct {
	str quic.Stream
}

var _ Stream = &stream{}

func (s *stream) Read(b []byte) (int, error) {
	return s.str.Read(b)
}

func (s *stream) Write(b []byte) (int, error) {
	return s.str.Write(b)
}

func (s *stream) CancelRead(e ErrorCode) {
	// TODO: calculate correct error code
	s.str.CancelRead(quic.StreamErrorCode(e))
}

func (s *stream) CancelWrite(e ErrorCode) {
	// TODO: calculate correct error code
	s.str.CancelWrite(quic.StreamErrorCode(e))
}

func (s *stream) Close() error {
	return s.str.Close()
}

func (s *stream) SetDeadline(t time.Time) error {
	return s.str.SetDeadline(t)
}

func (s *stream) SetReadDeadline(t time.Time) error {
	return s.str.SetReadDeadline(t)
}

func (s *stream) SetWriteDeadline(t time.Time) error {
	return s.str.SetWriteDeadline(t)
}
