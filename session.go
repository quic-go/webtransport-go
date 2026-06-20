package webtransport

import (
	"context"
	"io"
	"net"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

// sessionID is the WebTransport Session ID
type sessionID uint64

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
		incomingStreams:     *newIncomingStreamsMap(ctx),
		outgoingStreams:     *newOutgoingStreamsMap(conn, sessionID),
	}

	go func() {
		defer ctxCancel()
		c.handleConn()
	}()
	return c
}

func (s *Session) handleConn() {
	for {
		c, err := parseNextCapsule(s.str)
		if err != nil {
			s.closeWithError(err)
			return
		}
		switch c := c.(type) {
		case closeSessionCapsule:
			s.closeWithError(c.ToSessionError())
			return
		case maxStreamsBidiCapsule, maxStreamsUniCapsule:
			// TODO: handle max streams capsules
		}
	}
}

// addIncomingStream adds a bidirectional stream that the remote peer opened
func (s *Session) addIncomingStream(qstr *quic.Stream) {
	s.incomingStreams.AddStream(qstr)
}

// addIncomingUniStream adds a unidirectional stream that the remote peer opened
func (s *Session) addIncomingUniStream(qstr *quic.ReceiveStream) {
	s.incomingStreams.AddUniStream(qstr)
}

// Context returns a context that is closed when the session is closed.
func (s *Session) Context() context.Context {
	return s.ctx
}

func (s *Session) AcceptStream(ctx context.Context) (*Stream, error) {
	return s.incomingStreams.AcceptStream(ctx)
}

func (s *Session) AcceptUniStream(ctx context.Context) (*ReceiveStream, error) {
	return s.incomingStreams.AcceptUniStream(ctx)
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
	if _, err := str.Write((closeSessionCapsule{ErrorCode: code, Message: msg}).Append(nil)); err != nil {
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

	s.outgoingStreams.CloseSession(closeErr)
	s.incomingStreams.CloseSession(closeErr)

	return true, nil
}

// SessionState returns the current state of the session
func (s *Session) SessionState() SessionState {
	return SessionState{
		ConnectionState:     s.conn.ConnectionState(),
		ApplicationProtocol: s.applicationProtocol,
	}
}
