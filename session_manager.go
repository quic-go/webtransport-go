package webtransport

import (
	"context"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

type unestablishedSession struct {
	Streams    []*quic.Stream
	UniStreams []*quic.ReceiveStream

	Timer *time.Timer
}

type sessionEntry struct {
	// at any point in time, only one of these will be non-nil
	Unestablished *unestablishedSession
	Session       *Session
}

type sessionManager struct {
	timeout time.Duration

	mx    sync.Mutex
	conns map[*quic.Conn]map[sessionID]sessionEntry
}

func newSessionManager(timeout time.Duration) *sessionManager {
	return &sessionManager{
		timeout: timeout,
		conns:   make(map[*quic.Conn]map[sessionID]sessionEntry),
	}
}

// AddStream adds a new bidirectional stream to a WebTransport session.
// If the WebTransport session has not yet been established,
// it starts a new Goroutine and waits for establishment of the session.
// If that takes longer than timeout, the stream is reset.
func (m *sessionManager) AddStream(conn *quic.Conn, str *quic.Stream, id sessionID) {
	m.mx.Lock()
	defer m.mx.Unlock()

	entry := m.getOrCreateSession(conn, id)
	if entry.Session != nil {
		entry.Session.addIncomingStream(str)
		return
	}

	entry.Unestablished.Streams = append(entry.Unestablished.Streams, str)
	m.resetTimer(entry, conn, id)
}

// AddUniStream adds a new unidirectional stream to a WebTransport session.
// If the WebTransport session has not yet been established,
// it starts a new Goroutine and waits for establishment of the session.
// If that takes longer than timeout, the stream is reset.
func (m *sessionManager) AddUniStream(conn *quic.Conn, str *quic.ReceiveStream, id sessionID) {
	m.mx.Lock()
	defer m.mx.Unlock()

	entry := m.getOrCreateSession(conn, id)
	if entry.Session != nil {
		entry.Session.addIncomingUniStream(str)
		return
	}

	entry.Unestablished.UniStreams = append(entry.Unestablished.UniStreams, str)
	m.resetTimer(entry, conn, id)
}

func (m *sessionManager) resetTimer(entry *sessionEntry, conn *quic.Conn, id sessionID) {
	if entry.Unestablished.Timer != nil {
		entry.Unestablished.Timer.Reset(m.timeout)
		return
	}
	entry.Unestablished.Timer = time.AfterFunc(m.timeout, func() { m.onTimer(conn, id) })
}

func (m *sessionManager) onTimer(conn *quic.Conn, id sessionID) {
	m.mx.Lock()
	defer m.mx.Unlock()

	entry, ok := m.conns[conn]
	if !ok { // connection already closed
		return
	}
	sessionEntry, ok := entry[id]
	if !ok { // session already closed
		return
	}
	if sessionEntry.Session != nil { // session already established
		return
	}
	for _, str := range sessionEntry.Unestablished.Streams {
		str.CancelRead(WTBufferedStreamRejectedErrorCode)
		str.CancelWrite(WTBufferedStreamRejectedErrorCode)
	}
	for _, uniStr := range sessionEntry.Unestablished.UniStreams {
		uniStr.CancelRead(WTBufferedStreamRejectedErrorCode)
	}
	delete(entry, id)
	if len(entry) == 0 {
		delete(m.conns, conn)
	}
}

func (m *sessionManager) getOrCreateSession(conn *quic.Conn, id sessionID) *sessionEntry {
	sessionMap, ok := m.conns[conn]
	if !ok {
		sessionMap = make(map[sessionID]sessionEntry)
		m.conns[conn] = sessionMap
	}

	entry, ok := sessionMap[id]
	if ok {
		return &entry
	}
	entry = sessionEntry{Unestablished: &unestablishedSession{}}
	sessionMap[id] = entry
	return &entry
}

// AddSession adds a new WebTransport session.
func (m *sessionManager) AddSession(ctx context.Context, conn *quic.Conn, id sessionID, str http3Stream, applicationProtocol string) *Session {
	m.mx.Lock()
	defer m.mx.Unlock()

	sessionMap, ok := m.conns[conn]
	if !ok {
		sessionMap = make(map[sessionID]sessionEntry)
		m.conns[conn] = sessionMap
	}
	entry, ok := sessionMap[id]

	s := newSession(ctx, id, conn, str, applicationProtocol)
	if ok && entry.Unestablished != nil {
		// We might already have an entry of this session.
		// This can happen when we receive streams for this WebTransport session before we complete
		// the Extended CONNECT request.
		for _, str := range entry.Unestablished.Streams {
			s.addIncomingStream(str)
		}
		for _, uniStr := range entry.Unestablished.UniStreams {
			s.addIncomingUniStream(uniStr)
		}
		if entry.Unestablished.Timer != nil {
			entry.Unestablished.Timer.Stop()
		}
		entry.Unestablished = nil
	}
	sessionMap[id] = sessionEntry{Session: s}

	context.AfterFunc(s.Context(), func() {
		m.mx.Lock()
		defer m.mx.Unlock()
		delete(sessionMap, id)
		if len(sessionMap) == 0 {
			delete(m.conns, conn)
		}
	})

	return s
}

func (m *sessionManager) Close() {
	m.mx.Lock()
	defer m.mx.Unlock()

	for conn, sessionMap := range m.conns {
		for _, entry := range sessionMap {
			if entry.Unestablished != nil && entry.Unestablished.Timer != nil {
				entry.Unestablished.Timer.Stop()
			}
		}
		delete(m.conns, conn)
	}
}
