package webtransport

import (
	"sync"

	"github.com/quic-go/quic-go"
)

type incomingStreamsMap struct {
	mx sync.Mutex
	m  map[quic.StreamID]func(error)
}

func newIncomingStreamsMap() *incomingStreamsMap {
	return &incomingStreamsMap{m: make(map[quic.StreamID]func(error))}
}

func (s *incomingStreamsMap) AddStream(id quic.StreamID, close func(error)) {
	s.mx.Lock()
	s.m[id] = close
	s.mx.Unlock()
}

func (s *incomingStreamsMap) RemoveStream(id quic.StreamID) {
	s.mx.Lock()
	delete(s.m, id)
	s.mx.Unlock()
}

func (s *incomingStreamsMap) CloseSession(err error) {
	s.mx.Lock()
	defer s.mx.Unlock()

	for _, cl := range s.m {
		cl(err)
	}
	s.m = nil
}
