package webtransport

import (
	"bytes"
	"context"
	"math/rand"
	"net"
	"sync"

	"github.com/lucas-clemente/quic-go"
	"github.com/lucas-clemente/quic-go/http3"
	"github.com/lucas-clemente/quic-go/quicvarint"
)

// sessionID is the WebTransport Session ID
type sessionID uint64

type acceptQueue[T any] struct {
	mx sync.Mutex
	c  chan struct{}
	// Contains all the streams waiting to be accepted.
	// There's no explicit limit to the length of the queue, but it is implicitly
	// limited by the stream flow control provided by QUIC.
	queue []T
}

func newAcceptQueue[T any]() *acceptQueue[T] {
	return &acceptQueue[T]{c: make(chan struct{})}
}

func (q *acceptQueue[T]) Add(str T) {
	q.mx.Lock()
	q.queue = append(q.queue, str)
	q.mx.Unlock()

	select {
	case q.c <- struct{}{}:
	default:
	}
}

func (q *acceptQueue[T]) Next() T {
	q.mx.Lock()
	defer q.mx.Unlock()

	if len(q.queue) == 0 {
		return *new(T)
	}
	str := q.queue[0]
	q.queue = q.queue[1:]
	return str
}

func (q *acceptQueue[T]) Chan() <-chan struct{} { return q.c }

type Session struct {
	sessionID  sessionID
	qconn      http3.StreamCreator
	requestStr quic.Stream

	streamHdr    []byte
	uniStreamHdr []byte

	ctx        context.Context
	closeMx    sync.Mutex
	closeErr   error // not nil once the session is closed
	streamCtxs map[int]context.CancelFunc

	bidiAcceptQueue acceptQueue[Stream]
	uniAcceptQueue  acceptQueue[ReceiveStream]

	// TODO: garbage collect streams from when they are closed
	streams streamsMap
}

func newSession(sessionID sessionID, qconn http3.StreamCreator, requestStr quic.Stream) *Session {
	ctx, ctxCancel := context.WithCancel(context.Background())
	c := &Session{
		sessionID:       sessionID,
		qconn:           qconn,
		requestStr:      requestStr,
		ctx:             ctx,
		streamCtxs:      make(map[int]context.CancelFunc),
		bidiAcceptQueue: *newAcceptQueue[Stream](),
		uniAcceptQueue:  *newAcceptQueue[ReceiveStream](),
		streams:         *newStreamsMap(),
	}
	// precompute the headers for unidirectional streams
	buf := bytes.NewBuffer(make([]byte, 0, 2+quicvarint.Len(uint64(c.sessionID))))
	quicvarint.Write(buf, webTransportUniStreamType)
	quicvarint.Write(buf, uint64(c.sessionID))
	c.uniStreamHdr = buf.Bytes()
	// precompute the headers for bidirectional streams
	buf = bytes.NewBuffer(make([]byte, 0, 2+quicvarint.Len(uint64(c.sessionID))))
	quicvarint.Write(buf, webTransportFrameType)
	quicvarint.Write(buf, uint64(c.sessionID))
	c.streamHdr = buf.Bytes()

	go func() {
		defer ctxCancel()
		c.handleConn()
	}()
	return c
}

func (c *Session) handleConn() {
	var closeErr error
	for {
		// TODO: parse capsules sent on the request stream
		b := make([]byte, 100)
		if _, err := c.requestStr.Read(b); err != nil {
			closeErr = &ConnectionError{
				Remote:  true,
				Message: err.Error(),
			}
			break
		}
	}

	c.closeMx.Lock()
	defer c.closeMx.Unlock()
	// If we closed the connection, the closeErr will be set in Close.
	if c.closeErr == nil {
		c.closeErr = closeErr
	}
	for _, cancel := range c.streamCtxs {
		cancel()
	}
}

func (c *Session) addStream(qstr quic.Stream, addStreamHeader bool) Stream {
	var hdr []byte
	if addStreamHeader {
		hdr = c.streamHdr
	}
	str := newStream(qstr, hdr, func() { c.streams.RemoveStream(qstr.StreamID()) })
	c.streams.AddStream(qstr.StreamID(), str.closeWithSession)
	return str
}

func (c *Session) addReceiveStream(qstr quic.ReceiveStream) ReceiveStream {
	str := newReceiveStream(qstr, func() { c.streams.RemoveStream(qstr.StreamID()) })
	c.streams.AddStream(qstr.StreamID(), func() {
		str.closeWithSession()
	})
	return str
}

func (c *Session) addSendStream(qstr quic.SendStream) SendStream {
	str := newSendStream(qstr, c.uniStreamHdr, func() { c.streams.RemoveStream(qstr.StreamID()) })
	c.streams.AddStream(qstr.StreamID(), str.closeWithSession)
	return str
}

// addIncomingStream adds a bidirectional stream that the remote peer opened
func (c *Session) addIncomingStream(qstr quic.Stream) {
	c.closeMx.Lock()
	closeErr := c.closeErr
	if closeErr != nil {
		c.closeMx.Unlock()
		qstr.CancelRead(sessionCloseErrorCode)
		qstr.CancelWrite(sessionCloseErrorCode)
		return
	}
	str := c.addStream(qstr, false)
	c.closeMx.Unlock()

	c.bidiAcceptQueue.Add(str)
}

// addIncomingUniStream adds a unidirectional stream that the remote peer opened
func (c *Session) addIncomingUniStream(qstr quic.ReceiveStream) {
	c.closeMx.Lock()
	closeErr := c.closeErr
	if closeErr != nil {
		c.closeMx.Unlock()
		qstr.CancelRead(sessionCloseErrorCode)
		return
	}
	str := c.addReceiveStream(qstr)
	c.closeMx.Unlock()

	c.uniAcceptQueue.Add(str)
}

// Context returns a context that is closed when the session is closed.
func (c *Session) Context() context.Context {
	return c.ctx
}

func (c *Session) AcceptStream(ctx context.Context) (Stream, error) {
	c.closeMx.Lock()
	closeErr := c.closeErr
	c.closeMx.Unlock()
	if closeErr != nil {
		return nil, closeErr
	}

	for {
		// If there's a stream in the accept queue, return it immediately.
		if str := c.bidiAcceptQueue.Next(); str != nil {
			return str, nil
		}
		// No stream in the accept queue. Wait until we accept one.
		select {
		case <-c.ctx.Done():
			return nil, c.closeErr
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.bidiAcceptQueue.Chan():
		}
	}
}

func (c *Session) AcceptUniStream(ctx context.Context) (ReceiveStream, error) {
	c.closeMx.Lock()
	closeErr := c.closeErr
	c.closeMx.Unlock()
	if closeErr != nil {
		return nil, c.closeErr
	}

	for {
		// If there's a stream in the accept queue, return it immediately.
		if str := c.uniAcceptQueue.Next(); str != nil {
			return str, nil
		}
		// No stream in the accept queue. Wait until we accept one.
		select {
		case <-c.ctx.Done():
			return nil, c.closeErr
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.uniAcceptQueue.Chan():
		}
	}
}

func (c *Session) OpenStream() (Stream, error) {
	c.closeMx.Lock()
	defer c.closeMx.Unlock()

	if c.closeErr != nil {
		return nil, c.closeErr
	}

	qstr, err := c.qconn.OpenStream()
	if err != nil {
		return nil, err
	}
	return c.addStream(qstr, true), nil
}

func (c *Session) addStreamCtxCancel(cancel context.CancelFunc) (id int) {
rand:
	id = rand.Int()
	if _, ok := c.streamCtxs[id]; ok {
		goto rand
	}
	c.streamCtxs[id] = cancel
	return id
}

func (c *Session) OpenStreamSync(ctx context.Context) (str Stream, err error) {
	c.closeMx.Lock()
	if c.closeErr != nil {
		c.closeMx.Unlock()
		return nil, c.closeErr
	}
	ctx, cancel := context.WithCancel(ctx)
	id := c.addStreamCtxCancel(cancel)
	c.closeMx.Unlock()

	defer func() {
		c.closeMx.Lock()
		closeErr := c.closeErr
		delete(c.streamCtxs, id)
		c.closeMx.Unlock()
		if err != nil {
			err = closeErr
		}
	}()

	var qstr quic.Stream
	qstr, err = c.qconn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	return c.addStream(qstr, true), nil
}

func (c *Session) OpenUniStream() (SendStream, error) {
	c.closeMx.Lock()
	defer c.closeMx.Unlock()

	if c.closeErr != nil {
		return nil, c.closeErr
	}
	qstr, err := c.qconn.OpenUniStream()
	if err != nil {
		return nil, err
	}
	return c.addSendStream(qstr), nil
}

func (c *Session) OpenUniStreamSync(ctx context.Context) (str SendStream, err error) {
	c.closeMx.Lock()
	if c.closeErr != nil {
		c.closeMx.Unlock()
		return nil, c.closeErr
	}
	ctx, cancel := context.WithCancel(ctx)
	id := c.addStreamCtxCancel(cancel)
	c.closeMx.Unlock()

	defer func() {
		c.closeMx.Lock()
		closeErr := c.closeErr
		delete(c.streamCtxs, id)
		c.closeMx.Unlock()
		if err != nil {
			err = closeErr
		}
	}()

	var qstr quic.SendStream
	qstr, err = c.qconn.OpenUniStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	return c.addSendStream(qstr), nil
}

func (c *Session) LocalAddr() net.Addr {
	return c.qconn.LocalAddr()
}

func (c *Session) RemoteAddr() net.Addr {
	return c.qconn.RemoteAddr()
}

func (c *Session) Close() error {
	// TODO: send CLOSE_WEBTRANSPORT_SESSION capsule
	c.closeMx.Lock()
	c.closeErr = &ConnectionError{Message: "session closed"}
	c.streams.CloseSession()
	c.closeMx.Unlock()
	c.requestStr.CancelRead(1337)
	err := c.requestStr.Close()
	<-c.ctx.Done()
	return err
}
