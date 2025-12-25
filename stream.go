package webtransport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

type quicSendStream interface {
	io.WriteCloser
	StreamID() quic.StreamID
	CancelWrite(quic.StreamErrorCode)
	Context() context.Context
	SetWriteDeadline(time.Time) error
}

var (
	_ quicSendStream = &quic.SendStream{}
	_ quicSendStream = &quic.Stream{}
)

type quicReceiveStream interface {
	io.Reader
	StreamID() quic.StreamID
	CancelRead(quic.StreamErrorCode)
	SetReadDeadline(time.Time) error
}

var (
	_ quicReceiveStream = &quic.ReceiveStream{}
	_ quicReceiveStream = &quic.Stream{}
)

type SendStream struct {
	str quicSendStream
	// WebTransport stream header.
	// Set by the constructor, set to nil once sent out.
	// Might be initialized to nil if this sendStream is part of an incoming bidirectional stream.
	streamHdr     []byte
	streamHdrOnce sync.Once

	onClose func() // to remove the stream from the streamsMap

	closeOnce sync.Once
	closed    chan struct{}
	closeErr  error
}

func newSendStream(str quicSendStream, hdr []byte, onClose func()) *SendStream {
	return &SendStream{
		str:       str,
		closed:    make(chan struct{}),
		streamHdr: hdr,
		onClose:   onClose,
	}
}

func (s *SendStream) maybeSendStreamHeader() (err error) {
	s.streamHdrOnce.Do(func() {
		if _, e := s.str.Write(s.streamHdr); e != nil {
			err = e
			return
		}
		s.streamHdr = nil
		// Set reliable boundary to protect the stream header using RESET_STREAM_AT extension.
		// This ensures the header is delivered reliably even if the stream is reset.
		// Only works if the underlying stream supports it (quic.SendStream does, quic.Stream doesn't).
		if setter, ok := s.str.(interface{ SetReliableBoundary() }); ok {
			setter.SetReliableBoundary()
		}
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
	var strErr *quic.StreamError
	if errors.As(err, &strErr) && strErr.ErrorCode == WTSessionGoneErrorCode {
		// The stream is reset with a WTSessionGoneErrorCode when the session is closed.
		// If the peer is initiating the session close, we might need to wait for the CONNECT stream to be closed.
		// While a malicious peer might withhold the session close, this is not an interesting attack vector:
		// 1. a WebTransport stream consumes very little memory, and
		// 2. the number of concurrent WebTransport sessions is limited.
		<-s.closed // TODO: respect stream deadline
		return n, s.closeErr
	}
	return n, maybeConvertStreamError(err)
}

func (s *SendStream) CancelWrite(e StreamErrorCode) {
	s.str.CancelWrite(webtransportCodeToHTTPCode(e))
	s.onClose()
}

func (s *SendStream) closeWithSession(err error) {
	s.closeOnce.Do(func() {
		s.closeErr = err
		s.str.CancelWrite(WTSessionGoneErrorCode)
		close(s.closed)
	})
}

func (s *SendStream) Close() error {
	if err := s.maybeSendStreamHeader(); err != nil {
		return err
	}
	s.onClose()
	return maybeConvertStreamError(s.str.Close())
}

func (s *SendStream) Context() context.Context {
	return s.str.Context()
}

func (s *SendStream) SetWriteDeadline(t time.Time) error {
	return maybeConvertStreamError(s.str.SetWriteDeadline(t))
}

func (s *SendStream) StreamID() quic.StreamID {
	return s.str.StreamID()
}

type ReceiveStream struct {
	str quicReceiveStream

	onClose func() // to remove the stream from the streamsMap

	closeOnce sync.Once
	closed    chan struct{}
	closeErr  error
}

func newReceiveStream(str quicReceiveStream, onClose func()) *ReceiveStream {
	return &ReceiveStream{
		str:     str,
		closed:  make(chan struct{}),
		onClose: onClose,
	}
}

func (s *ReceiveStream) Read(b []byte) (int, error) {
	n, err := s.str.Read(b)
	if err != nil && !isTimeoutError(err) {
		s.onClose()
	}
	var strErr *quic.StreamError
	if errors.As(err, &strErr) && strErr.ErrorCode == WTSessionGoneErrorCode {
		// The stream is reset with a WTSessionGoneErrorCode when the session is closed.
		// If the peer is initiating the session close, we might need to wait for the CONNECT stream to be closed.
		// While a malicious peer might withhold the session close, this is not an interesting attack vector:
		// 1. a WebTransport stream consumes very little memory, and
		// 2. the number of concurrent WebTransport sessions is limited.
		<-s.closed // TODO: respect stream deadline
		return n, s.closeErr
	}
	return n, maybeConvertStreamError(err)
}

func (s *ReceiveStream) CancelRead(e StreamErrorCode) {
	s.str.CancelRead(webtransportCodeToHTTPCode(e))
	s.onClose()
}

func (s *ReceiveStream) closeWithSession(err error) {
	s.closeOnce.Do(func() {
		s.closeErr = err
		s.str.CancelRead(WTSessionGoneErrorCode)
		close(s.closed)
	})
}

func (s *ReceiveStream) SetReadDeadline(t time.Time) error {
	return maybeConvertStreamError(s.str.SetReadDeadline(t))
}

func (s *ReceiveStream) StreamID() quic.StreamID {
	return s.str.StreamID()
}

type Stream struct {
	sendStr *SendStream
	recvStr *ReceiveStream

	mx                             sync.Mutex
	sendSideClosed, recvSideClosed bool
	onClose                        func()
}

func newStream(str *quic.Stream, hdr []byte, onClose func()) *Stream {
	s := &Stream{onClose: onClose}
	s.sendStr = newSendStream(str, hdr, func() { s.registerClose(true) })
	s.recvStr = newReceiveStream(str, func() { s.registerClose(false) })
	return s
}

func (s *Stream) StreamID() quic.StreamID {
	return s.recvStr.StreamID()
}

func (s *Stream) Write(b []byte) (int, error) {
	return s.sendStr.Write(b)
}

func (s *Stream) Read(b []byte) (int, error) {
	return s.recvStr.Read(b)
}

func (s *Stream) CancelWrite(e StreamErrorCode) {
	s.sendStr.CancelWrite(e)
}

func (s *Stream) CancelRead(e StreamErrorCode) {
	s.recvStr.CancelRead(e)
}

func (s *Stream) Close() error {
	return s.sendStr.Close()
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

func (s *Stream) closeWithSession(err error) {
	s.sendStr.closeWithSession(err)
	s.recvStr.closeWithSession(err)
}

func (s *Stream) Context() context.Context {
	return s.sendStr.Context()
}

func (s *Stream) SetWriteDeadline(t time.Time) error {
	return s.sendStr.SetWriteDeadline(t)
}

func (s *Stream) SetReadDeadline(t time.Time) error {
	return s.recvStr.SetReadDeadline(t)
}

func (s *Stream) SetDeadline(t time.Time) error {
	err1 := s.SetWriteDeadline(t)
	err2 := s.SetReadDeadline(t)
	return errors.Join(err1, err2)
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
