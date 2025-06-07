package webtransport

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

const sessionCloseErrorCode quic.StreamErrorCode = 0x170d7b68

type SendStream struct {
	str *quic.SendStream
	// WebTransport stream header.
	// Set by the constructor, set to nil once sent out.
	// Might be initialized to nil if this sendStream is part of an incoming bidirectional stream.
	streamHdr []byte

	onClose func()

	once sync.Once
}

func newSendStream(str *quic.SendStream, hdr []byte, onClose func()) *SendStream {
	return &SendStream{str: str, streamHdr: hdr, onClose: onClose}
}

func (s *SendStream) maybeSendStreamHeader() (err error) {
	s.once.Do(func() {
		if _, e := s.str.Write(s.streamHdr); e != nil {
			err = e
			return
		}
		s.streamHdr = nil
	})
	return
}

func (s *SendStream) Write(b []byte) (int, error) {
	if err := s.maybeSendStreamHeader(); err != nil {
		return 0, err
	}
	n, err := s.str.Write(b)
	if err != nil && !isTimeoutError(err) {
		s.onClose()
	}
	return n, maybeConvertStreamError(err)
}

func (s *SendStream) CancelWrite(e StreamErrorCode) {
	s.str.CancelWrite(webtransportCodeToHTTPCode(e))
	s.onClose()
}

func (s *SendStream) closeWithSession() {
	s.str.CancelWrite(sessionCloseErrorCode)
}

func (s *SendStream) Close() error {
	if err := s.maybeSendStreamHeader(); err != nil {
		return err
	}
	s.onClose()
	return maybeConvertStreamError(s.str.Close())
}

func (s *SendStream) SetWriteDeadline(t time.Time) error {
	return maybeConvertStreamError(s.str.SetWriteDeadline(t))
}

func (s *SendStream) StreamID() quic.StreamID {
	return s.str.StreamID()
}

type ReceiveStream struct {
	str     *quic.ReceiveStream
	onClose func()
}

func newReceiveStream(str *quic.ReceiveStream, onClose func()) *ReceiveStream {
	return &ReceiveStream{str: str, onClose: onClose}
}

func (s *ReceiveStream) Read(b []byte) (int, error) {
	n, err := s.str.Read(b)
	if err != nil && !isTimeoutError(err) {
		s.onClose()
	}
	return n, maybeConvertStreamError(err)
}

func (s *ReceiveStream) CancelRead(e StreamErrorCode) {
	s.str.CancelRead(webtransportCodeToHTTPCode(e))
	s.onClose()
}

func (s *ReceiveStream) closeWithSession() {
	s.str.CancelRead(sessionCloseErrorCode)
}

func (s *ReceiveStream) SetReadDeadline(t time.Time) error {
	return maybeConvertStreamError(s.str.SetReadDeadline(t))
}

func (s *ReceiveStream) StreamID() quic.StreamID {
	return s.str.StreamID()
}

type Stream struct {
	*SendStream
	*ReceiveStream

	mx                             sync.Mutex
	sendSideClosed, recvSideClosed bool
	onClose                        func()
}

func newStream(str *quic.Stream, hdr []byte, onClose func()) *Stream {
	s := &Stream{onClose: onClose}
	s.SendStream = newSendStream(str.SendStream, hdr, func() { s.registerClose(true) })
	s.ReceiveStream = newReceiveStream(str.ReceiveStream, func() { s.registerClose(false) })
	return s
}

func (s *Stream) registerClose(isSendSide bool) {
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

func (s *Stream) closeWithSession() {
	s.SendStream.closeWithSession()
	s.ReceiveStream.closeWithSession()
}

func (s *Stream) SetDeadline(t time.Time) error {
	err1 := s.SendStream.SetWriteDeadline(t)
	err2 := s.ReceiveStream.SetReadDeadline(t)
	if err1 != nil {
		return err1
	}
	return err2
}

func (s *Stream) StreamID() quic.StreamID {
	return s.ReceiveStream.StreamID()
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
		return &StreamError{
			ErrorCode: errorCode,
			Remote:    streamErr.Remote,
		}
	}
	return err
}

func isTimeoutError(err error) bool {
	nerr, ok := err.(net.Error)
	if !ok {
		return false
	}
	return nerr.Timeout()
}
