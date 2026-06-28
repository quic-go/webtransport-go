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

func (q *acceptQueue[T]) next() (T, bool) {
	q.mx.Lock()
	defer q.mx.Unlock()

	if len(q.queue) == 0 {
		return *new(T), false
	}
	str := q.queue[0]
	q.queue = q.queue[1:]
	return str, true
}

type incomingStream interface {
	*Stream | *ReceiveStream
	closeWithSession(error)
}

type incomingStreamsMap[T incomingStream] struct {
	ctx    context.Context
	cancel context.CancelCauseFunc

	acceptQueue acceptQueue[T]

	mx sync.Mutex
	m  map[quic.StreamID]T
}

func newIncomingStreamsMap[T incomingStream]() *incomingStreamsMap[T] {
	ctx, cancel := context.WithCancelCause(context.Background())
	return &incomingStreamsMap[T]{
		ctx:         ctx,
		cancel:      cancel,
		acceptQueue: *newAcceptQueue[T](),
		m:           make(map[quic.StreamID]T),
	}
}

func (s *incomingStreamsMap[T]) addStream(id quic.StreamID, str T) {
	s.mx.Lock()
	if closeErr := context.Cause(s.ctx); closeErr != nil {
		s.mx.Unlock()
		str.closeWithSession(closeErr)
		return
	}
	s.m[id] = str
	s.mx.Unlock()

	s.acceptQueue.add(str)
}

func (s *incomingStreamsMap[T]) removeStream(id quic.StreamID) {
	s.mx.Lock()
	delete(s.m, id)
	s.mx.Unlock()
}

func (s *incomingStreamsMap[T]) AcceptStream(ctx context.Context) (T, error) {
	var zero T
	if closeErr := context.Cause(s.ctx); closeErr != nil {
		return zero, closeErr
	}

	for {
		// If there's a stream in the accept queue, return it immediately.
		if str, ok := s.acceptQueue.next(); ok {
			return str, nil
		}
		// No stream in the accept queue. Wait until we accept one.
		select {
		case <-s.ctx.Done():
			return zero, context.Cause(s.ctx)
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-s.acceptQueue.c:
		}
	}
}

func (s *incomingStreamsMap[T]) CloseSession(err error) {
	s.mx.Lock()
	defer s.mx.Unlock()

	if context.Cause(s.ctx) != nil {
		return
	}
	s.cancel(err)

	for _, str := range s.m {
		str.closeWithSession(err)
	}
	s.m = nil
}
