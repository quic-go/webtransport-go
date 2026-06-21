package webtransport

import (
	"context"
	"sync"

	"github.com/quic-go/quic-go"
)

type acceptQueue[T any] struct {
	// The channel is used to notify consumers about new incoming items.
	// Needs to be buffered to preserve the notification if an item is enqueued
	// between checking the queue and waiting on this channel.
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

func (q *acceptQueue[T]) add(str T) {
	q.mx.Lock()
	q.queue = append(q.queue, str)
	q.mx.Unlock()

	select {
	case q.c <- struct{}{}:
	default:
	}
}

func (q *acceptQueue[T]) next() T {
	q.mx.Lock()
	defer q.mx.Unlock()

	if len(q.queue) == 0 {
		return *new(T)
	}
	str := q.queue[0]
	q.queue = q.queue[1:]
	return str
}

type incomingStreamsMap struct {
	ctx context.Context

	bidiAcceptQueue acceptQueue[*Stream]
	uniAcceptQueue  acceptQueue[*ReceiveStream]

	mx       sync.Mutex
	closeErr error
	m        map[quic.StreamID]func(error)
}

func newIncomingStreamsMap(ctx context.Context) *incomingStreamsMap {
	return &incomingStreamsMap{
		ctx:             ctx,
		bidiAcceptQueue: *newAcceptQueue[*Stream](),
		uniAcceptQueue:  *newAcceptQueue[*ReceiveStream](),
		m:               make(map[quic.StreamID]func(error)),
	}
}

func (s *incomingStreamsMap) AddStream(qstr *quic.Stream) {
	s.mx.Lock()
	if s.closeErr != nil {
		s.mx.Unlock()
		qstr.CancelRead(WTSessionGoneErrorCode)
		qstr.CancelWrite(WTSessionGoneErrorCode)
		return
	}
	str := newStream(qstr, nil, func() { s.removeStream(qstr.StreamID()) })
	s.m[qstr.StreamID()] = str.closeWithSession
	s.mx.Unlock()

	s.bidiAcceptQueue.add(str)
}

func (s *incomingStreamsMap) AddUniStream(qstr *quic.ReceiveStream) {
	s.mx.Lock()
	if s.closeErr != nil {
		s.mx.Unlock()
		qstr.CancelRead(WTSessionGoneErrorCode)
		return
	}
	str := newReceiveStream(qstr, func() { s.removeStream(qstr.StreamID()) })
	s.m[qstr.StreamID()] = str.closeWithSession
	s.mx.Unlock()

	s.uniAcceptQueue.add(str)
}

func (s *incomingStreamsMap) removeStream(id quic.StreamID) {
	s.mx.Lock()
	delete(s.m, id)
	s.mx.Unlock()
}

func (s *incomingStreamsMap) AcceptStream(ctx context.Context) (*Stream, error) {
	s.mx.Lock()
	closeErr := s.closeErr
	s.mx.Unlock()
	if closeErr != nil {
		return nil, closeErr
	}

	for {
		// If there's a stream in the accept queue, return it immediately.
		if str := s.bidiAcceptQueue.next(); str != nil {
			return str, nil
		}
		// No stream in the accept queue. Wait until we accept one.
		select {
		case <-s.ctx.Done():
			s.mx.Lock()
			closeErr := s.closeErr
			s.mx.Unlock()
			return nil, closeErr
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.bidiAcceptQueue.c:
		}
	}
}

func (s *incomingStreamsMap) AcceptUniStream(ctx context.Context) (*ReceiveStream, error) {
	s.mx.Lock()
	closeErr := s.closeErr
	s.mx.Unlock()
	if closeErr != nil {
		return nil, closeErr
	}

	for {
		// If there's a stream in the accept queue, return it immediately.
		if str := s.uniAcceptQueue.next(); str != nil {
			return str, nil
		}
		// No stream in the accept queue. Wait until we accept one.
		select {
		case <-s.ctx.Done():
			s.mx.Lock()
			closeErr := s.closeErr
			s.mx.Unlock()
			return nil, closeErr
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.uniAcceptQueue.c:
		}
	}
}

func (s *incomingStreamsMap) CloseSession(err error) {
	s.mx.Lock()
	defer s.mx.Unlock()

	if s.closeErr != nil {
		return
	}
	s.closeErr = err

	for _, cl := range s.m {
		cl(err)
	}
	s.m = nil
}
