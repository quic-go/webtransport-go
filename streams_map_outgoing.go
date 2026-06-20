package webtransport

import (
	"context"
	"math/rand/v2"
	"sync"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/quicvarint"
)

type outgoingStreamsMap struct {
	conn         *quic.Conn
	streamHdr    []byte
	uniStreamHdr []byte

	mx         sync.Mutex
	closeErr   error
	streamCtxs map[int]context.CancelFunc
	m          map[quic.StreamID]func(error)
}

func newOutgoingStreamsMap(conn *quic.Conn, sessionID sessionID) *outgoingStreamsMap {
	uniStreamHdr := make([]byte, 0, 2+quicvarint.Len(uint64(sessionID)))
	uniStreamHdr = quicvarint.Append(uniStreamHdr, webTransportUniStreamType)
	uniStreamHdr = quicvarint.Append(uniStreamHdr, uint64(sessionID))

	streamHdr := make([]byte, 0, 2+quicvarint.Len(uint64(sessionID)))
	streamHdr = quicvarint.Append(streamHdr, webTransportFrameType)
	streamHdr = quicvarint.Append(streamHdr, uint64(sessionID))

	return &outgoingStreamsMap{
		conn:         conn,
		streamHdr:    streamHdr,
		uniStreamHdr: uniStreamHdr,
		streamCtxs:   make(map[int]context.CancelFunc),
		m:            make(map[quic.StreamID]func(error)),
	}
}

func (s *outgoingStreamsMap) removeStream(id quic.StreamID) {
	s.mx.Lock()
	delete(s.m, id)
	s.mx.Unlock()
}

func (s *outgoingStreamsMap) addStream(qstr *quic.Stream) *Stream {
	str := newStream(qstr, s.streamHdr, func() { s.removeStream(qstr.StreamID()) })
	s.m[qstr.StreamID()] = str.closeWithSession
	return str
}

func (s *outgoingStreamsMap) addSendStream(qstr *quic.SendStream) *SendStream {
	str := newSendStream(qstr, s.uniStreamHdr, func() { s.removeStream(qstr.StreamID()) })
	s.m[qstr.StreamID()] = str.closeWithSession
	return str
}

func (s *outgoingStreamsMap) OpenStream() (*Stream, error) {
	s.mx.Lock()
	defer s.mx.Unlock()

	if s.closeErr != nil {
		return nil, s.closeErr
	}

	qstr, err := s.conn.OpenStream()
	if err != nil {
		return nil, err
	}
	return s.addStream(qstr), nil
}

func (s *outgoingStreamsMap) addStreamCtxCancel(cancel context.CancelFunc) (id int) {
rand:
	id = rand.Int()
	if _, ok := s.streamCtxs[id]; ok {
		goto rand
	}
	s.streamCtxs[id] = cancel
	return id
}

func (s *outgoingStreamsMap) OpenStreamSync(ctx context.Context) (*Stream, error) {
	s.mx.Lock()
	if s.closeErr != nil {
		s.mx.Unlock()
		return nil, s.closeErr
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	id := s.addStreamCtxCancel(cancel)
	s.mx.Unlock()

	qstr, err := s.conn.OpenStreamSync(ctx)

	s.mx.Lock()
	defer s.mx.Unlock()
	delete(s.streamCtxs, id)

	if qstr != nil && s.closeErr != nil {
		qstr.CancelRead(WTSessionGoneErrorCode)
		qstr.CancelWrite(WTSessionGoneErrorCode)
		return nil, s.closeErr
	}
	if err != nil {
		if s.closeErr != nil {
			return nil, s.closeErr
		}
		return nil, err
	}
	return s.addStream(qstr), nil
}

func (s *outgoingStreamsMap) OpenUniStream() (*SendStream, error) {
	s.mx.Lock()
	defer s.mx.Unlock()

	if s.closeErr != nil {
		return nil, s.closeErr
	}
	qstr, err := s.conn.OpenUniStream()
	if err != nil {
		return nil, err
	}
	return s.addSendStream(qstr), nil
}

func (s *outgoingStreamsMap) OpenUniStreamSync(ctx context.Context) (str *SendStream, err error) {
	s.mx.Lock()
	if s.closeErr != nil {
		s.mx.Unlock()
		return nil, s.closeErr
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	id := s.addStreamCtxCancel(cancel)
	s.mx.Unlock()

	qstr, err := s.conn.OpenUniStreamSync(ctx)

	s.mx.Lock()
	defer s.mx.Unlock()
	delete(s.streamCtxs, id)

	if qstr != nil && s.closeErr != nil {
		qstr.CancelWrite(WTSessionGoneErrorCode)
		return nil, s.closeErr
	}
	if err != nil {
		if s.closeErr != nil {
			return nil, s.closeErr
		}
		return nil, err
	}
	return s.addSendStream(qstr), nil
}

func (s *outgoingStreamsMap) CloseSession(err error) {
	s.mx.Lock()
	defer s.mx.Unlock()

	if s.closeErr != nil {
		return
	}
	s.closeErr = err

	for _, cancel := range s.streamCtxs {
		cancel()
	}

	for _, cl := range s.m {
		cl(err)
	}
	s.m = nil
}
