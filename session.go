package webtransport

import (
	"context"
	"io"
	"net"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/quicvarint"
)

// sessionID is the WebTransport Session ID
type sessionID uint64

type acceptQueue[T any] struct {
	// The channel is used to notify consumers (via Chan) about new incoming items.
	// Needs to be buffered to preserve the notification if an item is enqueued
	// between a call to Next and to Chan.
	c chan struct{}

	mx sync.Mutex
	// Contains all the streams waiting to be accepted.
	// There's no explicit limit to the length of the queue, but it is implicitly
	// limited by the stream flow control provided by QUIC.
	queue []T
}

func newAcceptQueue[T any]() *acceptQueue[T] {
	return &acceptQueue[T]{c: make(chan struct{}, 1)}
}

func (q *acceptQueue[T]) Add(str T) {
	q.mx.Lock()
	q.queue = append(q.queue, str)
	q.mx.Unlock()

	select {
	case q.c <- struct{}{}:
	default:
	}
}

func (q *acceptQueue[T]) Next() T {
	q.mx.Lock()
	defer q.mx.Unlock()

	if len(q.queue) == 0 {
		return *new(T)
	}
	str := q.queue[0]
	q.queue = q.queue[1:]
	return str
}

func (q *acceptQueue[T]) Chan() <-chan struct{} { return q.c }

type http3Stream interface {
	io.ReadWriteCloser
	ReceiveDatagram(context.Context) ([]byte, error)
	SendDatagram([]byte) error
	CancelRead(quic.StreamErrorCode)
	CancelWrite(quic.StreamErrorCode)
	SetWriteDeadline(time.Time) error
}

var (
	_ http3Stream = &http3.Stream{}
	_ http3Stream = &http3.RequestStream{}
)

// SessionState contains the state of a WebTransport session
type SessionState struct {
	// ConnectionState contains the QUIC connection state, including TLS handshake information
	ConnectionState quic.ConnectionState

	// ApplicationProtocol contains the application protocol negotiated for the session
	ApplicationProtocol string
}

type Session struct {
	sessionID           sessionID
	conn                *quic.Conn
	str                 http3Stream
	applicationProtocol string

	ctx      context.Context
	closeMx  sync.Mutex
	closeErr error // not nil once the session is closed

	bidiAcceptQueue acceptQueue[*Stream]
	uniAcceptQueue  acceptQueue[*ReceiveStream]

	incomingStreams incomingStreamsMap
	outgoingStreams outgoingStreamsMap
}

func newSession(ctx context.Context, sessionID sessionID, conn *quic.Conn, str http3Stream, applicationProtocol string) *Session {
	ctx, ctxCancel := context.WithCancel(ctx)
	c := &Session{
		sessionID:           sessionID,
		conn:                conn,
		str:                 str,
		applicationProtocol: applicationProtocol,
		ctx:                 ctx,
		bidiAcceptQueue:     *newAcceptQueue[*Stream](),
		uniAcceptQueue:      *newAcceptQueue[*ReceiveStream](),
		incomingStreams:     *newIncomingStreamsMap(),
		outgoingStreams:     *newOutgoingStreamsMap(conn, sessionID),
	}

	go func() {
		defer ctxCancel()
		c.handleConn()
	}()
	return c
}

func (s *Session) handleConn() {
	err := parseNextCapsule(s.str)
	s.closeWithError(err)
}

// addIncomingStream adds a bidirectional stream that the remote peer opened
func (s *Session) addIncomingStream(qstr *quic.Stream) {
	s.closeMx.Lock()
	closeErr := s.closeErr
	if closeErr != nil {
		s.closeMx.Unlock()
		qstr.CancelRead(WTSessionGoneErrorCode)
		qstr.CancelWrite(WTSessionGoneErrorCode)
		return
	}
	str := newStream(qstr, nil, func() { s.incomingStreams.removeStream(qstr.StreamID()) })
	s.incomingStreams.addStream(qstr.StreamID(), str.closeWithSession)
	s.closeMx.Unlock()

	s.bidiAcceptQueue.Add(str)
}

// addIncomingUniStream adds a unidirectional stream that the remote peer opened
func (s *Session) addIncomingUniStream(qstr *quic.ReceiveStream) {
	s.closeMx.Lock()
	closeErr := s.closeErr
	if closeErr != nil {
		s.closeMx.Unlock()
		qstr.CancelRead(WTSessionGoneErrorCode)
		return
	}
	str := newReceiveStream(qstr, func() { s.incomingStreams.removeStream(qstr.StreamID()) })
	s.incomingStreams.addStream(qstr.StreamID(), str.closeWithSession)
	s.closeMx.Unlock()

	s.uniAcceptQueue.Add(str)
}

// Context returns a context that is closed when the session is closed.
func (s *Session) Context() context.Context {
	return s.ctx
}

func (s *Session) AcceptStream(ctx context.Context) (*Stream, error) {
	s.closeMx.Lock()
	closeErr := s.closeErr
	s.closeMx.Unlock()
	if closeErr != nil {
		return nil, closeErr
	}

	for {
		// If there's a stream in the accept queue, return it immediately.
		if str := s.bidiAcceptQueue.Next(); str != nil {
			return str, nil
		}
		// No stream in the accept queue. Wait until we accept one.
		select {
		case <-s.ctx.Done():
			return nil, s.closeErr
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.bidiAcceptQueue.Chan():
		}
	}
}

func (s *Session) AcceptUniStream(ctx context.Context) (*ReceiveStream, error) {
	s.closeMx.Lock()
	closeErr := s.closeErr
	s.closeMx.Unlock()
	if closeErr != nil {
		return nil, s.closeErr
	}

	for {
		// If there's a stream in the accept queue, return it immediately.
		if str := s.uniAcceptQueue.Next(); str != nil {
			return str, nil
		}
		// No stream in the accept queue. Wait until we accept one.
		select {
		case <-s.ctx.Done():
			return nil, s.closeErr
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.uniAcceptQueue.Chan():
		}
	}
}

func (s *Session) OpenStream() (*Stream, error) {
	s.closeMx.Lock()
	defer s.closeMx.Unlock()

	if s.closeErr != nil {
		return nil, s.closeErr
	}
	return s.outgoingStreams.OpenStream()
}

func (s *Session) OpenStreamSync(ctx context.Context) (*Stream, error) {
	s.closeMx.Lock()
	if s.closeErr != nil {
		s.closeMx.Unlock()
		return nil, s.closeErr
	}
	s.closeMx.Unlock()

	str, err := s.outgoingStreams.OpenStreamSync(ctx)

	s.closeMx.Lock()
	defer s.closeMx.Unlock()

	// the session might have been closed concurrently with OpenStreamSync returning
	if s.closeErr != nil {
		return nil, s.closeErr
	}
	return str, err
}

func (s *Session) OpenUniStream() (*SendStream, error) {
	s.closeMx.Lock()
	defer s.closeMx.Unlock()

	if s.closeErr != nil {
		return nil, s.closeErr
	}
	return s.outgoingStreams.OpenUniStream()
}

func (s *Session) OpenUniStreamSync(ctx context.Context) (str *SendStream, err error) {
	s.closeMx.Lock()
	if s.closeErr != nil {
		s.closeMx.Unlock()
		return nil, s.closeErr
	}
	s.closeMx.Unlock()

	str, err = s.outgoingStreams.OpenUniStreamSync(ctx)

	s.closeMx.Lock()
	defer s.closeMx.Unlock()

	// the session might have been closed concurrently with OpenStreamSync returning
	if s.closeErr != nil {
		return nil, s.closeErr
	}
	return str, err
}

func (s *Session) LocalAddr() net.Addr {
	return s.conn.LocalAddr()
}

func (s *Session) RemoteAddr() net.Addr {
	return s.conn.RemoteAddr()
}

func (s *Session) CloseWithError(code SessionErrorCode, msg string) error {
	first, err := s.closeWithError(&SessionError{ErrorCode: code, Message: msg})
	if err != nil || !first {
		return err
	}

	err = closeSessionStream(s.str, code, msg)
	<-s.ctx.Done()
	return err
}

func closeSessionStream(str http3Stream, code SessionErrorCode, msg string) error {
	// Optimistically send the WT_CLOSE_SESSION Capsule:
	// If we're flow-control limited, we don't want to wait for the receiver to issue new flow control credits.
	// There's no idiomatic way to do a non-blocking write in Go, so we set a short deadline.
	str.SetWriteDeadline(time.Now().Add(10 * time.Millisecond))
	payload := appendCloseSessionCapsulePayload(nil, code, msg)
	if err := http3.WriteCapsule(quicvarint.NewWriter(str), closeSessionCapsuleType, payload); err != nil {
		str.CancelWrite(WTSessionGoneErrorCode)
	}

	str.CancelRead(WTSessionGoneErrorCode)
	return str.Close()
}

func (s *Session) SendDatagram(b []byte) error {
	return s.str.SendDatagram(b)
}

func (s *Session) ReceiveDatagram(ctx context.Context) ([]byte, error) {
	return s.str.ReceiveDatagram(ctx)
}

func (s *Session) closeWithError(closeErr error) (bool /* first call to close session */, error) {
	s.closeMx.Lock()
	defer s.closeMx.Unlock()
	// Duplicate call, or the remote already closed this session.
	if s.closeErr != nil {
		return false, nil
	}
	s.closeErr = closeErr

	s.incomingStreams.closeSession(closeErr)
	s.outgoingStreams.CloseSession(closeErr)

	return true, nil
}

// SessionState returns the current state of the session
func (s *Session) SessionState() SessionState {
	return SessionState{
		ConnectionState:     s.conn.ConnectionState(),
		ApplicationProtocol: s.applicationProtocol,
	}
}
