package webtransport

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

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

// sessions is the map value in the conns map
type sessions struct {
	created chan struct{} // is closed once the sessions map has been initialized
	conn    *Conn
}

type Server struct {
	H3 http3.Server

	initOnce sync.Once
	initErr  error

	connMx sync.Mutex
	conns  map[sessionKey]*sessions
}

func (s *Server) initialize() error {
	s.initOnce.Do(func() {
		s.initErr = s.init()
	})
	return s.initErr
}

func (s *Server) init() error {
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
			s.conns = make(map[sessionKey]*sessions)
		}
		key := sessionKey{qconn: qconn, id: sessionID(sID)}
		session, ok := s.conns[key]
		if !ok {
			sess := &sessions{created: make(chan struct{})}
			s.conns[key] = sess
			go s.handleUnassociatedStream(str, sess)
		} else {
			session.conn.addStream(str)
		}
		return true, nil
	}
	return nil
}

func (s *Server) handleUnassociatedStream(str quic.Stream, sessions *sessions) {
	t := time.NewTimer(5 * time.Second)
	defer t.Stop()

	select {
	case <-sessions.created:
		sessions.conn.addStream(str)
	case <-t.C:
		// TODO: use correct error code
		str.CancelRead(1337)
		str.CancelWrite(1337)
	}
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
	return s.H3.Close()
}

func (s *Server) addConn(qconn http3.StreamCreator, id sessionID, conn *Conn) {
	s.connMx.Lock()
	defer s.connMx.Unlock()

	if s.conns == nil {
		s.conns = make(map[sessionKey]*sessions)
	}
	key := sessionKey{qconn: qconn, id: id}
	if sess, ok := s.conns[key]; ok {
		sess.conn = conn
		close(sess.created)
		return
	}
	c := make(chan struct{})
	close(c)
	s.conns[key] = &sessions{created: c, conn: conn}
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
	// TODO: verify origin
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
