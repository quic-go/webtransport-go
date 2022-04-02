package webtransport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lucas-clemente/quic-go"
	"github.com/lucas-clemente/quic-go/http3"
	"github.com/lucas-clemente/quic-go/quicvarint"
)

const (
	webTransportDraftOfferHeaderKey = "Sec-Webtransport-Http3-Draft02"
	webTransportDraftHeaderKey      = "Sec-Webtransport-Http3-Draft"
	webTransportDraftHeaderValue    = "draft02"
)

const (
	webTransportFrameType = 0x41
)

type streamIDGetter interface {
	StreamID() quic.StreamID
}

var _ streamIDGetter = quic.Stream(nil)

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

type Server struct {
	H3 http3.Server

	// StreamReorderingTime is the time an incoming WebTransport stream that cannot be associated
	// with a session is buffered.
	// This can happen if the CONNECT request (that creates a new session) is reordered, and arrives
	// after the first WebTransport stream(s) for that session.
	// Defaults to 5 seconds.
	StreamReorderingTimeout time.Duration

	// CheckOrigin is used to validate the request origin, thereby preventing cross-site request forgery.
	// CheckOrigin returns true if the request Origin header is acceptable.
	// If unset, a safe default is used: If the Origin header is set, it is checked that it
	// matches the request's Host header.
	CheckOrigin func(r *http.Request) bool

	ctx       context.Context // is closed when Close is called
	ctxCancel context.CancelFunc
	refCount  sync.WaitGroup

	initOnce sync.Once
	initErr  error

	connMx sync.Mutex
	conns  map[sessionKey]*session
}

func (s *Server) initialize() error {
	s.initOnce.Do(func() {
		s.initErr = s.init()
	})
	return s.initErr
}

func (s *Server) init() error {
	s.ctx, s.ctxCancel = context.WithCancel(context.Background())
	if s.StreamReorderingTimeout == 0 {
		s.StreamReorderingTimeout = 5 * time.Second
	}
	if s.CheckOrigin == nil {
		s.CheckOrigin = checkSameOrigin
	}

	// configure the http3.Server
	if s.H3.AdditionalSettings == nil {
		s.H3.AdditionalSettings = make(map[uint64]uint64)
	}
	s.H3.AdditionalSettings[settingsEnableWebtransport] = 1
	s.H3.EnableDatagrams = true
	if s.H3.StreamHijacker != nil {
		return errors.New("StreamHijacker already set")
	}
	s.H3.StreamHijacker = func(ft http3.FrameType, qconn quic.Connection, str quic.Stream) (bool /* hijacked */, error) {
		if ft != webTransportFrameType {
			return false, nil
		}
		sID, err := quicvarint.Read(quicvarint.NewReader(str))
		if err != nil {
			return false, err
		}
		s.connMx.Lock()
		defer s.connMx.Unlock()
		if s.conns == nil {
			s.conns = make(map[sessionKey]*session)
		}
		key := sessionKey{qconn: qconn, id: sessionID(sID)}
		sess, ok := s.conns[key]
		if ok && sess.conn != nil {
			sess.conn.addStream(str)
			return true, nil
		}
		if !ok {
			sess = &session{created: make(chan struct{})}
			s.conns[key] = sess
		}
		sess.counter++
		s.refCount.Add(1)
		go func() {
			defer s.refCount.Done()
			s.handleUnassociatedStream(str, sess, key)
		}()
		return true, nil
	}
	return nil
}

func (s *Server) handleUnassociatedStream(str quic.Stream, session *session, key sessionKey) {
	t := time.NewTimer(s.StreamReorderingTimeout)
	defer t.Stop()

	// When multiple streams are waiting for the same session to be established,
	// the timeout is calculated for every stream separately.
	select {
	case <-session.created:
		session.conn.addStream(str)
	case <-t.C:
		str.CancelRead(WebTransportBufferedStreamRejectedErrorCode)
		str.CancelWrite(WebTransportBufferedStreamRejectedErrorCode)
	case <-s.ctx.Done():
	}
	s.connMx.Lock()
	session.counter--
	// Once no more streams are waiting for this session to be established,
	// and this session is still outstanding, delete it from the map.
	if session.counter == 0 && session.conn == nil {
		delete(s.conns, key)
	}
	s.connMx.Unlock()
}

func (s *Server) Serve(conn net.PacketConn) error {
	s.initialize()
	return s.H3.Serve(conn)
}

func (s *Server) ListenAndServe() error {
	s.initialize()
	return s.H3.ListenAndServe()
}

func (s *Server) ListenAndServeTLS(certFile, keyFile string) error {
	s.initialize()
	return s.H3.ListenAndServeTLS(certFile, keyFile)
}

func (s *Server) Close() error {
	s.ctxCancel()
	err := s.H3.Close()
	s.refCount.Wait()
	return err
}

func (s *Server) addConn(qconn http3.StreamCreator, id sessionID, conn *Conn) {
	s.connMx.Lock()
	defer s.connMx.Unlock()

	if s.conns == nil {
		s.conns = make(map[sessionKey]*session)
	}
	key := sessionKey{qconn: qconn, id: id}
	if sess, ok := s.conns[key]; ok {
		sess.conn = conn
		close(sess.created)
		return
	}
	c := make(chan struct{})
	close(c)
	s.conns[key] = &session{created: c, conn: conn}
}

func (s *Server) Upgrade(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	if r.Method != http.MethodConnect {
		return nil, fmt.Errorf("expected CONNECT request, got %s", r.Method)
	}
	if r.Proto != protocolHeader {
		return nil, fmt.Errorf("unexpected protocol: %s", r.Proto)
	}
	if v, ok := r.Header[webTransportDraftOfferHeaderKey]; !ok || len(v) != 1 || v[0] != "1" {
		return nil, fmt.Errorf("missing or invalid %s header", webTransportDraftOfferHeaderKey)
	}
	if !s.CheckOrigin(r) {
		return nil, errors.New("webtransport: request origin not allowed")
	}
	w.Header().Add(webTransportDraftHeaderKey, webTransportDraftHeaderValue)
	w.WriteHeader(200)
	w.(http.Flusher).Flush()

	str, ok := w.(streamIDGetter)
	if !ok { // should never happen, unless quic-go changed the API
		return nil, errors.New("failed to get stream ID")
	}
	sID := sessionID(str.StreamID())

	hijacker, ok := w.(http3.Hijacker)
	if !ok { // should never happen, unless quic-go changed the API
		return nil, errors.New("failed to hijack")
	}
	qconn := hijacker.StreamCreator()
	c := newConn(sID, qconn, r.Body)

	s.addConn(qconn, sID, c)
	return c, nil
}

// copied from https://github.com/gorilla/websocket
func checkSameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return equalASCIIFold(u.Host, r.Host)
}

// copied from https://github.com/gorilla/websocket
func equalASCIIFold(s, t string) bool {
	for s != "" && t != "" {
		sr, size := utf8.DecodeRuneInString(s)
		s = s[size:]
		tr, size := utf8.DecodeRuneInString(t)
		t = t[size:]
		if sr == tr {
			continue
		}
		if 'A' <= sr && sr <= 'Z' {
			sr = sr + 'a' - 'A'
		}
		if 'A' <= tr && tr <= 'Z' {
			tr = tr + 'a' - 'A'
		}
		if sr != tr {
			return false
		}
	}
	return s == t
}
