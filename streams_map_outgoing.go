package webtransport

import (
	"context"
	"math/rand/v2"
	"slices"
	"sync"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/quicvarint"
)

// StreamLimitReachedError is returned from OpenStream and OpenUniStream
// when it is not possible to open a new stream because the peer's stream limit is reached.
type StreamLimitReachedError struct{}

func (e StreamLimitReachedError) Error() string { return "too many open streams" }

const maxOutgoingStreams = 1 << 60

// outgoingStreamLimit tracks the number of WebTransport streams opened against
// the peer's advertised stream limit.
type outgoingStreamLimit struct {
	OpenQueue []chan struct{}

	NumStreams  uint64 // total number of streams opened for this session
	MaxStreams  uint64 // stream limit, as advertised by the peer
	BlockedSent bool   // was a WT_STREAMS_BLOCKED sent for the current MaxStreams
}

type outgoingStreamsMap struct {
	conn         *quic.Conn
	streamHdr    []byte
	uniStreamHdr []byte
	queueCapsule func(capsule)

	mx         sync.Mutex
	closeErr   error
	streamCtxs map[int]context.CancelFunc
	bidiLimit  outgoingStreamLimit
	uniLimit   outgoingStreamLimit
	m          map[quic.StreamID]func(error)
}

func newOutgoingStreamsMap(conn *quic.Conn, sessionID sessionID, queueCapsule func(capsule)) *outgoingStreamsMap {
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
		queueCapsule: queueCapsule,
		streamCtxs:   make(map[int]context.CancelFunc),
		bidiLimit:    outgoingStreamLimit{MaxStreams: maxOutgoingStreams},
		uniLimit:     outgoingStreamLimit{MaxStreams: maxOutgoingStreams},
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

	if s.closeErr != nil {
		s.mx.Unlock()
		return nil, s.closeErr
	}
	if len(s.bidiLimit.OpenQueue) > 0 {
		s.mx.Unlock()
		return nil, &StreamLimitReachedError{}
	}

	if s.bidiLimit.NumStreams >= s.bidiLimit.MaxStreams {
		sendBlocked := !s.bidiLimit.BlockedSent
		maxStreams := s.bidiLimit.MaxStreams
		if sendBlocked {
			s.bidiLimit.BlockedSent = true
		}
		s.mx.Unlock()
		if sendBlocked {
			s.queueCapsule(streamsBlockedBidiCapsule{MaximumStreams: maxStreams})
		}
		return nil, &StreamLimitReachedError{}
	}
	s.bidiLimit.NumStreams++

	qstr, err := s.conn.OpenStream()
	if err != nil {
		s.bidiLimit.NumStreams--
		s.maybeUnblockOpenSync(&s.bidiLimit)
		s.mx.Unlock()
		return nil, err
	}
	str := s.addStream(qstr)
	s.mx.Unlock()
	return str, nil
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
	if err := s.waitForStreamLimit(ctx, &s.bidiLimit, true); err != nil {
		s.mx.Unlock()
		return nil, err
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
		if qstr == nil && s.closeErr == nil {
			s.bidiLimit.NumStreams--
			s.maybeUnblockOpenSync(&s.bidiLimit)
		}
		if s.closeErr != nil {
			return nil, s.closeErr
		}
		return nil, err
	}
	return s.addStream(qstr), nil
}

func (s *outgoingStreamsMap) OpenUniStream() (*SendStream, error) {
	s.mx.Lock()

	if s.closeErr != nil {
		s.mx.Unlock()
		return nil, s.closeErr
	}
	if len(s.uniLimit.OpenQueue) > 0 {
		s.mx.Unlock()
		return nil, &StreamLimitReachedError{}
	}
	if s.uniLimit.NumStreams >= s.uniLimit.MaxStreams {
		sendBlocked := !s.uniLimit.BlockedSent
		maxStreams := s.uniLimit.MaxStreams
		if sendBlocked {
			s.uniLimit.BlockedSent = true
		}
		s.mx.Unlock()
		if sendBlocked {
			s.queueCapsule(streamsBlockedUniCapsule{MaximumStreams: maxStreams})
		}
		return nil, &StreamLimitReachedError{}
	}
	s.uniLimit.NumStreams++
	qstr, err := s.conn.OpenUniStream()
	if err != nil {
		s.uniLimit.NumStreams--
		s.maybeUnblockOpenSync(&s.uniLimit)
		s.mx.Unlock()
		return nil, err
	}
	str := s.addSendStream(qstr)
	s.mx.Unlock()
	return str, nil
}

func (s *outgoingStreamsMap) OpenUniStreamSync(ctx context.Context) (str *SendStream, err error) {
	s.mx.Lock()
	if s.closeErr != nil {
		s.mx.Unlock()
		return nil, s.closeErr
	}
	if err := s.waitForStreamLimit(ctx, &s.uniLimit, false); err != nil {
		s.mx.Unlock()
		return nil, err
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
		if qstr == nil && s.closeErr == nil {
			s.uniLimit.NumStreams--
			s.maybeUnblockOpenSync(&s.uniLimit)
		}
		if s.closeErr != nil {
			return nil, s.closeErr
		}
		return nil, err
	}
	return s.addSendStream(qstr), nil
}

func (s *outgoingStreamsMap) waitForStreamLimit(ctx context.Context, streamLimit *outgoingStreamLimit, bidirectional bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(streamLimit.OpenQueue) == 0 && streamLimit.NumStreams < streamLimit.MaxStreams {
		streamLimit.NumStreams++
		return nil
	}

	waitChan := make(chan struct{}, 1)
	streamLimit.OpenQueue = append(streamLimit.OpenQueue, waitChan)

	if streamLimit.NumStreams >= streamLimit.MaxStreams && !streamLimit.BlockedSent {
		streamLimit.BlockedSent = true
		maxStreams := streamLimit.MaxStreams
		s.mx.Unlock()
		if bidirectional {
			s.queueCapsule(streamsBlockedBidiCapsule{MaximumStreams: maxStreams})
		} else {
			s.queueCapsule(streamsBlockedUniCapsule{MaximumStreams: maxStreams})
		}
		s.mx.Lock()
		if s.closeErr != nil {
			return s.closeErr
		}
	}

	s.mx.Unlock()
	select {
	case <-ctx.Done():
		s.mx.Lock()
		streamLimit.OpenQueue = slices.DeleteFunc(streamLimit.OpenQueue, func(c chan struct{}) bool {
			return c == waitChan
		})
		// If we just received a WT_MAX_STREAMS capsule, this might have been the next stream
		// that could be opened. Make sure we unblock the next OpenStreamSync call.
		s.maybeUnblockOpenSync(streamLimit)
		return ctx.Err()
	case <-waitChan:
	}

	s.mx.Lock()
	if s.closeErr != nil {
		return s.closeErr
	}
	if err := ctx.Err(); err != nil {
		streamLimit.OpenQueue = slices.DeleteFunc(streamLimit.OpenQueue, func(c chan struct{}) bool {
			return c == waitChan
		})
		s.maybeUnblockOpenSync(streamLimit)
		return err
	}

	streamLimit.NumStreams++
	streamLimit.OpenQueue = streamLimit.OpenQueue[1:]
	if len(streamLimit.OpenQueue) > 0 && streamLimit.NumStreams >= streamLimit.MaxStreams {
		if !streamLimit.BlockedSent {
			streamLimit.BlockedSent = true
			maxStreams := streamLimit.MaxStreams
			s.mx.Unlock()
			if bidirectional {
				s.queueCapsule(streamsBlockedBidiCapsule{MaximumStreams: maxStreams})
			} else {
				s.queueCapsule(streamsBlockedUniCapsule{MaximumStreams: maxStreams})
			}
			s.mx.Lock()
			if s.closeErr != nil {
				return s.closeErr
			}
		}
	} else {
		s.maybeUnblockOpenSync(streamLimit)
	}
	return nil
}

func (s *outgoingStreamsMap) UpdateBidiStreamLimit(limit uint64) {
	s.updateStreamLimit(&s.bidiLimit, limit)
}

func (s *outgoingStreamsMap) UpdateUniStreamLimit(limit uint64) {
	s.updateStreamLimit(&s.uniLimit, limit)
}

func (s *outgoingStreamsMap) updateStreamLimit(streamLimit *outgoingStreamLimit, limit uint64) {
	s.mx.Lock()
	defer s.mx.Unlock()

	if limit <= streamLimit.MaxStreams || s.closeErr != nil {
		return
	}
	streamLimit.MaxStreams = limit
	streamLimit.BlockedSent = false
	s.maybeUnblockOpenSync(streamLimit)
}

// unblockOpenSync unblocks the next OpenStreamSync go-routine to open a new stream.
func (s *outgoingStreamsMap) maybeUnblockOpenSync(streamLimit *outgoingStreamLimit) {
	if len(streamLimit.OpenQueue) == 0 {
		return
	}
	if streamLimit.NumStreams >= streamLimit.MaxStreams {
		return
	}
	// maybeUnblockOpenSync is called both from OpenStreamSync and from UpdateStreamLimit.
	// It's sufficient to only unblock OpenStreamSync once.
	select {
	case streamLimit.OpenQueue[0] <- struct{}{}:
	default:
	}
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
	for _, c := range s.bidiLimit.OpenQueue {
		close(c)
	}
	s.bidiLimit.OpenQueue = nil
	for _, c := range s.uniLimit.OpenQueue {
		close(c)
	}
	s.uniLimit.OpenQueue = nil

	for _, cl := range s.m {
		cl(err)
	}
	s.m = nil
}
