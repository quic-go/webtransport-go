package webtransport

import (
	"context"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/quicvarint"
)

// session is the map value in the conns map
type session struct {
	created chan struct{} // is closed once the session map has been initialized
	counter int           // how many streams are waiting for this session to be established
	sess    *Session
}

type connEntry struct {
	conn     *quic.Conn
	sessions map[sessionID]*session
}

type sessionManager struct {
	refCount  sync.WaitGroup
	ctx       context.Context
	ctxCancel context.CancelFunc

	timeout time.Duration

	mx    sync.Mutex
	conns map[*quic.Conn]connEntry
}

func newSessionManager(timeout time.Duration) *sessionManager {
	m := &sessionManager{
		timeout: timeout,
		conns:   make(map[*quic.Conn]connEntry),
	}
	m.ctx, m.ctxCancel = context.WithCancel(context.Background())
	return m
}

// AddStream adds a new bidirectional stream to a WebTransport session.
// If the WebTransport session has not yet been established,
// it starts a new go routine and waits for establishment of the session.
// If that takes longer than timeout, the stream is reset.
func (m *sessionManager) AddStream(conn *quic.Conn, str *quic.Stream, id sessionID) {
	sess, isExisting := m.getOrCreateSession(conn, id)
	if isExisting {
		sess.sess.addIncomingStream(str)
		return
	}

	m.refCount.Add(1)
	go func() {
		defer m.refCount.Done()
		m.handleStream(str, sess)

		m.mx.Lock()
		defer m.mx.Unlock()

		sess.counter--
		// Once no more streams are waiting for this session to be established,
		// and this session is still outstanding, delete it from the map.
		if sess.counter == 0 && sess.sess == nil {
			m.maybeDelete(conn, id)
		}
	}()
}

func (m *sessionManager) maybeDelete(conn *quic.Conn, id sessionID) {
	entry, ok := m.conns[conn]
	if !ok { // should never happen
		return
	}
	delete(entry.sessions, id)
	if len(entry.sessions) == 0 {
		delete(m.conns, conn)
	}
}

// AddUniStream adds a new unidirectional stream to a WebTransport session.
// If the WebTransport session has not yet been established,
// it starts a new go routine and waits for establishment of the session.
// If that takes longer than timeout, the stream is reset.
func (m *sessionManager) AddUniStream(conn *quic.Conn, str *quic.ReceiveStream) {
	idv, err := quicvarint.Read(quicvarint.NewReader(str))
	if err != nil {
		str.CancelRead(1337)
		return
	}
	id := sessionID(idv)

	sess, isExisting := m.getOrCreateSession(conn, id)
	if isExisting {
		sess.sess.addIncomingUniStream(str)
		return
	}

	m.refCount.Add(1)
	go func() {
		defer m.refCount.Done()
		m.handleUniStream(str, sess)

		m.mx.Lock()
		defer m.mx.Unlock()

		sess.counter--
		// Once no more streams are waiting for this session to be established,
		// and this session is still outstanding, delete it from the map.
		if sess.counter == 0 && sess.sess == nil {
			m.maybeDelete(conn, id)
		}
	}()
}

func (m *sessionManager) getOrCreateSession(conn *quic.Conn, id sessionID) (sess *session, existed bool) {
	m.mx.Lock()
	defer m.mx.Unlock()

	entry, ok := m.conns[conn]
	if !ok {
		entry = connEntry{
			conn:     conn,
			sessions: make(map[sessionID]*session),
		}
		m.conns[conn] = entry
	}

	sess, ok = entry.sessions[id]
	if ok && sess.sess != nil {
		return sess, true
	}
	if !ok {
		sess = &session{created: make(chan struct{})}
		entry.sessions[id] = sess
	}
	sess.counter++
	return sess, false
}

func (m *sessionManager) handleStream(str *quic.Stream, sess *session) {
	t := time.NewTimer(m.timeout)
	defer t.Stop()

	// When multiple streams are waiting for the same session to be established,
	// the timeout is calculated for every stream separately.
	select {
	case <-sess.created:
		sess.sess.addIncomingStream(str)
	case <-t.C:
		str.CancelRead(WTBufferedStreamRejectedErrorCode)
		str.CancelWrite(WTBufferedStreamRejectedErrorCode)
	case <-m.ctx.Done():
	}
}

func (m *sessionManager) handleUniStream(str *quic.ReceiveStream, sess *session) {
	t := time.NewTimer(m.timeout)
	defer t.Stop()

	// When multiple streams are waiting for the same session to be established,
	// the timeout is calculated for every stream separately.
	select {
	case <-sess.created:
		sess.sess.addIncomingUniStream(str)
	case <-t.C:
		str.CancelRead(WTBufferedStreamRejectedErrorCode)
	case <-m.ctx.Done():
	}
}

// AddSession adds a new WebTransport session.
func (m *sessionManager) AddSession(ctx context.Context, conn *quic.Conn, id sessionID, str http3Stream, applicationProtocol string) *Session {
	m.mx.Lock()
	defer m.mx.Unlock()

	entry, ok := m.conns[conn]
	if !ok {
		entry = connEntry{
			conn:     conn,
			sessions: make(map[sessionID]*session),
		}
		m.conns[conn] = entry
	}
	s := newSession(ctx, id, conn, str, applicationProtocol)

	if sess, ok := entry.sessions[id]; ok {
		// We might already have an entry of this session.
		// This can happen when we receive a stream for this WebTransport session before we complete
		// the HTTP request (that establishes the session).
		sess.sess = s
		close(sess.created)
		return s
	}
	c := make(chan struct{})
	close(c)
	entry.sessions[id] = &session{created: c, sess: s}

	// Delete the webtransport session from the session manager once its context is cancalled.
	go func() {
		<-conn.Context().Done()
		m.mx.Lock()
		defer m.mx.Unlock()
		m.maybeDelete(conn, id)
	}()
	return s
}

func (m *sessionManager) Close() {
	m.ctxCancel()
	m.refCount.Wait()
}
