package webtransport

import (
	"context"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/quicvarint"
)

// session is the map value in the conns map
type session struct {
	created chan struct{} // is closed once the session map has been initialized
	counter int           // how many streams are waiting for this session to be established
	conn    *Session
}

type sessionManager struct {
	refCount  sync.WaitGroup
	ctx       context.Context
	ctxCancel context.CancelFunc

	timeout time.Duration

	mx             sync.Mutex
	conns          map[quic.ConnectionTracingID]map[sessionID]*session
	goawayReceived bool // Set to true when HTTP/3 GOAWAY is received
}

func newSessionManager(timeout time.Duration) *sessionManager {
	m := &sessionManager{
		timeout: timeout,
		conns:   make(map[quic.ConnectionTracingID]map[sessionID]*session),
	}
	m.ctx, m.ctxCancel = context.WithCancel(context.Background())
	return m
}

// AddStream adds a new bidirectional stream to a WebTransport session.
// If the WebTransport session has not yet been established,
// it starts a new go routine and waits for establishment of the session.
// If that takes longer than timeout, the stream is reset.
func (m *sessionManager) AddStream(connTracingID quic.ConnectionTracingID, str *quic.Stream, id sessionID) {
	sess, isExisting := m.getOrCreateSession(connTracingID, id)
	if isExisting {
		sess.conn.addIncomingStream(str)
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
		if sess.counter == 0 && sess.conn == nil {
			m.maybeDelete(connTracingID, id)
		}
	}()
}

func (m *sessionManager) maybeDelete(connTracingID quic.ConnectionTracingID, id sessionID) {
	sessions, ok := m.conns[connTracingID]
	if !ok { // should never happen
		return
	}
	delete(sessions, id)
	if len(sessions) == 0 {
		delete(m.conns, connTracingID)
	}
}

// AddUniStream adds a new unidirectional stream to a WebTransport session.
// If the WebTransport session has not yet been established,
// it starts a new go routine and waits for establishment of the session.
// If that takes longer than timeout, the stream is reset.
func (m *sessionManager) AddUniStream(connTracingID quic.ConnectionTracingID, str *quic.ReceiveStream) {
	idv, err := quicvarint.Read(quicvarint.NewReader(str))
	if err != nil {
		str.CancelRead(1337)
	}
	id := sessionID(idv)

	sess, isExisting := m.getOrCreateSession(connTracingID, id)
	if isExisting {
		sess.conn.addIncomingUniStream(str)
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
		if sess.counter == 0 && sess.conn == nil {
			m.maybeDelete(connTracingID, id)
		}
	}()
}

func (m *sessionManager) getOrCreateSession(connTracingID quic.ConnectionTracingID, id sessionID) (sess *session, existed bool) {
	m.mx.Lock()
	defer m.mx.Unlock()

	sessions, ok := m.conns[connTracingID]
	if !ok {
		sessions = make(map[sessionID]*session)
		m.conns[connTracingID] = sessions
	}

	sess, ok = sessions[id]
	if ok && sess.conn != nil {
		return sess, true
	}
	if !ok {
		sess = &session{created: make(chan struct{})}
		sessions[id] = sess
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
		sess.conn.addIncomingStream(str)
	case <-t.C:
		str.CancelRead(WebTransportBufferedStreamRejectedErrorCode)
		str.CancelWrite(WebTransportBufferedStreamRejectedErrorCode)
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
		sess.conn.addIncomingUniStream(str)
	case <-t.C:
		str.CancelRead(WebTransportBufferedStreamRejectedErrorCode)
	case <-m.ctx.Done():
	}
}

// AddSession adds a new WebTransport session.
// maxSessions is the peer's advertised SETTINGS_WT_MAX_SESSIONS value.
// Returns nil if GOAWAY has been received (server is shutting down).
func (m *sessionManager) AddSession(qconn *http3.Conn, id sessionID, str http3Stream, maxSessions uint64) *Session {
	m.mx.Lock()
	defer m.mx.Unlock()

	// Reject new sessions if GOAWAY received
	if m.goawayReceived {
		return nil
	}

	conn := newSession(id, qconn, str, maxSessions)
	connTracingID := qconn.Context().Value(quic.ConnectionTracingKey).(quic.ConnectionTracingID)

	sessions, ok := m.conns[connTracingID]
	if !ok {
		sessions = make(map[sessionID]*session)
		m.conns[connTracingID] = sessions
	}
	if sess, ok := sessions[id]; ok {
		// We might already have an entry of this session.
		// This can happen when we receive a stream for this WebTransport session before we complete the HTTP request
		// that establishes the session.
		sess.conn = conn
		close(sess.created)
		return conn
	}
	c := make(chan struct{})
	close(c)
	sessions[id] = &session{created: c, conn: conn}
	return conn
}

func (m *sessionManager) Close() {
	m.ctxCancel()
	m.refCount.Wait()
}

// OnGOAWAY handles HTTP/3 GOAWAY frames by initiating graceful shutdown of all active sessions.
// This method is called when the underlying HTTP/3 connection receives a GOAWAY frame,
// indicating that the server is shutting down and will not accept new requests.
//
// For each active WebTransport session, this method calls Drain() to send a WT_DRAIN_SESSION
// capsule, allowing clients to gracefully wind down operations. After calling OnGOAWAY,
// AddSession will reject new session establishments.
//
// RFC Section 4.7: "When an HTTP/3 endpoint sends a GOAWAY frame, the endpoint SHOULD send
// a DRAIN_WEBTRANSPORT_SESSION capsule on every existing WebTransport session."
func (m *sessionManager) OnGOAWAY() {
	m.mx.Lock()
	defer m.mx.Unlock()

	m.goawayReceived = true

	// Iterate all active sessions and initiate graceful shutdown
	for _, sessions := range m.conns {
		for _, sess := range sessions {
			if sess.conn != nil {
				// Call Drain() on each active session
				// Ignore errors - session might already be closing
				_ = sess.conn.Drain()
			}
		}
	}
}
