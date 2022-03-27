package webtransport

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/lucas-clemente/quic-go"
)

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

func (s *stream) maybeConvertStreamError(err error) error {
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

func (s *stream) Read(b []byte) (int, error) {
	n, err := s.str.Read(b)
	return n, s.maybeConvertStreamError(err)
}

func (s *stream) Write(b []byte) (int, error) {
	n, err := s.str.Write(b)
	return n, s.maybeConvertStreamError(err)
}

func (s *stream) CancelRead(e ErrorCode) {
	s.str.CancelRead(webtransportCodeToHTTPCode(e))
}

func (s *stream) CancelWrite(e ErrorCode) {
	s.str.CancelWrite(webtransportCodeToHTTPCode(e))
}

func (s *stream) Close() error {
	return s.maybeConvertStreamError(s.str.Close())
}

func (s *stream) SetDeadline(t time.Time) error {
	return s.maybeConvertStreamError(s.str.SetDeadline(t))
}

func (s *stream) SetReadDeadline(t time.Time) error {
	return s.maybeConvertStreamError(s.str.SetReadDeadline(t))
}

func (s *stream) SetWriteDeadline(t time.Time) error {
	return s.maybeConvertStreamError(s.str.SetWriteDeadline(t))
}
