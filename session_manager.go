package webtransport

import (
	"context"
	"github.com/lucas-clemente/quic-go/quicvarint"
	"sync"
	"time"

	"github.com/lucas-clemente/quic-go"
	"github.com/lucas-clemente/quic-go/http3"
)

// sessionKey is used as a map key in the conns map
type sessionKey struct {
	qconn http3.StreamCreator
	id    sessionID
}

// session is the map value in the conns map
type session struct {
	created chan struct{} // is closed once the session map has been initialized
	counter int           // how many streams are waiting for this session to be established
	conn    *Conn
}

type sessionManager struct {
	refCount  sync.WaitGroup
	ctx       context.Context
	ctxCancel context.CancelFunc

	timeout time.Duration

	mx    sync.Mutex
	conns map[sessionKey]*session
}

func newSessionManager(timeout time.Duration) *sessionManager {
	m := &sessionManager{
		timeout: timeout,
		conns:   make(map[sessionKey]*session),
	}
	m.ctx, m.ctxCancel = context.WithCancel(context.Background())
	return m
}

// AddStream adds a new bidirectional stream to a WebTransport session.
// If the WebTransport session has not yet been established,
// it starts a new go routine and waits for establishment of the session.
// If that takes longer than timeout, the stream is reset.
func (m *sessionManager) AddStream(qconn http3.StreamCreator, str quic.Stream, id sessionID) {
	key := sessionKey{qconn: qconn, id: id}
	sess, isExisting := m.getSession(key)
	if isExisting {
		sess.conn.addStream(str)
		return
	}

	m.refCount.Add(1)
	go func() {
		defer m.refCount.Done()
		m.handleStream(str, sess, key)
	}()
}

// AddUniStream adds a new unidirectional stream to a WebTransport session.
// If the WebTransport session has not yet been established,
// it starts a new go routine and waits for establishment of the session.
// If that takes longer than timeout, the stream is reset.
func (m *sessionManager) AddUniStream(qconn http3.StreamCreator, str quic.ReceiveStream) {
	id, err := quicvarint.Read(quicvarint.NewReader(str))
	if err != nil {
		str.CancelRead(1337)
	}

	key := sessionKey{qconn: qconn, id: sessionID(id)}
	sess, isExisting := m.getSession(key)
	if isExisting {
		sess.conn.addUniStream(str)
		return
	}

	m.refCount.Add(1)
	go func() {
		defer m.refCount.Done()
		m.handleUniStream(str, sess, key)
	}()
}

func (m *sessionManager) getSession(key sessionKey) (sess *session, existed bool) {
	m.mx.Lock()
	defer m.mx.Unlock()

	sess, ok := m.conns[key]
	if ok && sess.conn != nil {
		return sess, true
	}
	if !ok {
		sess = &session{created: make(chan struct{})}
		m.conns[key] = sess
	}
	sess.counter++
	return sess, false
}

func (m *sessionManager) handleStream(str quic.Stream, session *session, key sessionKey) {
	t := time.NewTimer(m.timeout)
	defer t.Stop()

	// When multiple streams are waiting for the same session to be established,
	// the timeout is calculated for every stream separately.
	select {
	case <-session.created:
		session.conn.addStream(str)
	case <-t.C:
		str.CancelRead(WebTransportBufferedStreamRejectedErrorCode)
		str.CancelWrite(WebTransportBufferedStreamRejectedErrorCode)
	case <-m.ctx.Done():
	}

	m.mx.Lock()
	defer m.mx.Unlock()

	session.counter--
	// Once no more streams are waiting for this session to be established,
	// and this session is still outstanding, delete it from the map.
	if session.counter == 0 && session.conn == nil {
		delete(m.conns, key)
	}
}

func (m *sessionManager) handleUniStream(str quic.ReceiveStream, session *session, key sessionKey) {
	t := time.NewTimer(m.timeout)
	defer t.Stop()

	// When multiple streams are waiting for the same session to be established,
	// the timeout is calculated for every stream separately.
	select {
	case <-session.created:
		session.conn.addUniStream(str)
	case <-t.C:
		str.CancelRead(WebTransportBufferedStreamRejectedErrorCode)
	case <-m.ctx.Done():
	}

	m.mx.Lock()
	defer m.mx.Unlock()

	session.counter--
	// Once no more streams are waiting for this session to be established,
	// and this session is still outstanding, delete it from the map.
	if session.counter == 0 && session.conn == nil {
		delete(m.conns, key)
	}
}

// AddSession adds a new WebTransport session.
func (m *sessionManager) AddSession(qconn http3.StreamCreator, id sessionID, conn *Conn) {
	m.mx.Lock()
	defer m.mx.Unlock()

	key := sessionKey{qconn: qconn, id: id}
	if sess, ok := m.conns[key]; ok {
		sess.conn = conn
		close(sess.created)
		return
	}
	c := make(chan struct{})
	close(c)
	m.conns[key] = &session{created: c, conn: conn}
}

func (m *sessionManager) Close() {
	m.ctxCancel()
	m.refCount.Wait()
}
