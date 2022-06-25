package webtransport

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
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
	// WebTransport stream header.
	// Set by the constructor, set to nil once sent out.
	// Might be initialized to nil if this sendStream is part of an incoming bidirectional stream.
	streamHdr []byte

	onClose func()
}

var _ SendStream = &sendStream{}

func newSendStream(str quic.SendStream, hdr []byte, onClose func()) *sendStream {
	return &sendStream{str: str, streamHdr: hdr, onClose: onClose}
}

func (s *sendStream) maybeSendStreamHeader() error {
	if len(s.streamHdr) == 0 {
		return nil
	}
	if _, err := s.str.Write(s.streamHdr); err != nil {
		return err
	}
	s.streamHdr = nil
	return nil
}

func (s *sendStream) Write(b []byte) (int, error) {
	if err := s.maybeSendStreamHeader(); err != nil {
		return 0, err
	}
	n, err := s.str.Write(b)
	if err != nil && !isTimeoutError(err) {
		s.onClose()
	}
	return n, maybeConvertStreamError(err)
}

func (s *sendStream) cancelWrite(e ErrorCode) {
	s.str.CancelWrite(webtransportCodeToHTTPCode(e))
}

func (s *sendStream) CancelWrite(e ErrorCode) {
	s.str.CancelWrite(webtransportCodeToHTTPCode(e))
	s.onClose()
}

func (s *sendStream) Close() error {
	if err := s.maybeSendStreamHeader(); err != nil {
		return err
	}
	s.onClose()
	return maybeConvertStreamError(s.str.Close())
}

func (s *sendStream) SetWriteDeadline(t time.Time) error {
	return maybeConvertStreamError(s.str.SetWriteDeadline(t))
}

type receiveStream struct {
	str     quic.ReceiveStream
	onClose func()
}

var _ ReceiveStream = &receiveStream{}

func newReceiveStream(str quic.ReceiveStream, onClose func()) *receiveStream {
	return &receiveStream{str: str, onClose: onClose}
}

func (s *receiveStream) Read(b []byte) (int, error) {
	n, err := s.str.Read(b)
	if err != nil && !isTimeoutError(err) {
		s.onClose()
	}
	return n, maybeConvertStreamError(err)
}

func (s *receiveStream) cancelRead(e ErrorCode) {
	s.str.CancelRead(webtransportCodeToHTTPCode(e))
}

func (s *receiveStream) CancelRead(e ErrorCode) {
	s.cancelRead(e)
	s.onClose()
}

func (s *receiveStream) SetReadDeadline(t time.Time) error {
	return maybeConvertStreamError(s.str.SetReadDeadline(t))
}

type stream struct {
	*sendStream
	*receiveStream

	mx                             sync.Mutex
	sendSideClosed, recvSideClosed bool
	onClose                        func()
}

var _ Stream = &stream{}

func newStream(str quic.Stream, hdr []byte, onClose func()) *stream {
	s := &stream{onClose: onClose}
	s.sendStream = newSendStream(str, hdr, func() { s.registerClose(true) })
	s.receiveStream = newReceiveStream(str, func() { s.registerClose(false) })
	return s
}

func (s *stream) registerClose(isSendSide bool) {
	s.mx.Lock()
	if isSendSide {
		s.sendSideClosed = true
	} else {
		s.recvSideClosed = true
	}
	isClosed := s.sendSideClosed && s.recvSideClosed
	s.mx.Unlock()

	if isClosed {
		s.onClose()
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

func isTimeoutError(err error) bool {
	nerr, ok := err.(net.Error)
	if !ok {
		return false
	}
	return nerr.Timeout()
}
