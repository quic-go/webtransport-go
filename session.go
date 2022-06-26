package webtransport

import (
	"bytes"
	"context"
	"io"
	"math/rand"
	"net"
	"sync"

	"github.com/lucas-clemente/quic-go"
	"github.com/lucas-clemente/quic-go/http3"
	"github.com/lucas-clemente/quic-go/quicvarint"
)

// sessionID is the WebTransport Session ID
type sessionID uint64

// TODO: use the correct error code here
// See https://github.com/ietf-wg-webtrans/draft-ietf-webtrans-http3/issues/72
const sessionCloseErrorCode = 42

type Session struct {
	sessionID  sessionID
	qconn      http3.StreamCreator
	requestStr io.ReadWriteCloser

	streamHdr    []byte
	uniStreamHdr []byte

	ctx        context.Context
	closeMx    sync.Mutex
	closeErr   error // not nil once the session is closed
	streamCtxs map[int]context.CancelFunc

	// for bidirectional streams
	acceptMx   sync.Mutex
	acceptChan chan struct{}
	// Contains all the streams waiting to be accepted.
	// There's no explicit limit to the length of the queue, but it is implicitly
	// limited by the stream flow control provided by QUIC.
	acceptQueue []Stream

	// for unidirectional streams
	acceptUniMx   sync.Mutex
	acceptUniChan chan struct{}
	// Contains all the streams waiting to be accepted.
	// There's no explicit limit to the length of the queue, but it is implicitly
	// limited by the stream flow control provided by QUIC.
	acceptUniQueue []ReceiveStream

	// TODO: garbage collect streams from when they are closed
	streams streamsMap
}

func newSession(sessionID sessionID, qconn http3.StreamCreator, requestStr io.ReadWriteCloser) *Session {
	ctx, ctxCancel := context.WithCancel(context.Background())
	c := &Session{
		sessionID:     sessionID,
		qconn:         qconn,
		requestStr:    requestStr,
		ctx:           ctx,
		streamCtxs:    make(map[int]context.CancelFunc),
		acceptChan:    make(chan struct{}, 1),
		acceptUniChan: make(chan struct{}, 1),
		streams:       *newStreamsMap(),
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
	c.closeErr = closeErr
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
	c.streams.AddStream(qstr.StreamID(), func() {
		str.cancelRead(sessionCloseErrorCode)
		str.cancelWrite(sessionCloseErrorCode)
	})
	return str
}

func (c *Session) addReceiveStream(qstr quic.ReceiveStream) ReceiveStream {
	str := newReceiveStream(qstr, func() { c.streams.RemoveStream(qstr.StreamID()) })
	c.streams.AddStream(qstr.StreamID(), func() {
		str.cancelRead(sessionCloseErrorCode)
	})
	return str
}

func (c *Session) addSendStream(qstr quic.SendStream) SendStream {
	str := newSendStream(qstr, c.uniStreamHdr, func() { c.streams.RemoveStream(qstr.StreamID()) })
	c.streams.AddStream(qstr.StreamID(), func() {
		str.cancelWrite(sessionCloseErrorCode)
	})
	return str
}

// addIncomingStream adds a bidirectional stream that the remote peer opened
func (c *Session) addIncomingStream(qstr quic.Stream) {
	str := c.addStream(qstr, false)

	c.acceptMx.Lock()
	defer c.acceptMx.Unlock()

	c.acceptQueue = append(c.acceptQueue, str)
	select {
	case c.acceptChan <- struct{}{}:
	default:
	}
}

// addIncomingUniStream adds a unidirectional stream that the remote peer opened
func (c *Session) addIncomingUniStream(qstr quic.ReceiveStream) {
	str := c.addReceiveStream(qstr)

	c.acceptUniMx.Lock()
	defer c.acceptUniMx.Unlock()

	c.acceptUniQueue = append(c.acceptUniQueue, str)
	select {
	case c.acceptUniChan <- struct{}{}:
	default:
	}
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
		var str Stream
		// If there's a stream in the accept queue, return it immediately.
		c.acceptMx.Lock()
		if len(c.acceptQueue) > 0 {
			str = c.acceptQueue[0]
			c.acceptQueue = c.acceptQueue[1:]
		}
		c.acceptMx.Unlock()
		if str != nil {
			return str, nil
		}

		// No stream in the accept queue. Wait until we accept one.
		select {
		case <-c.ctx.Done():
			return nil, c.closeErr
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.acceptChan:
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
		var str ReceiveStream
		// If there's a stream in the accept queue, return it immediately.
		c.acceptUniMx.Lock()
		if len(c.acceptUniQueue) > 0 {
			str = c.acceptUniQueue[0]
			c.acceptUniQueue = c.acceptUniQueue[1:]
		}
		c.acceptUniMx.Unlock()
		if str != nil {
			return str, nil
		}

		// No stream in the accept queue. Wait until we accept one.
		select {
		case <-c.ctx.Done():
			return nil, c.closeErr
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.acceptUniChan:
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
	c.streams.CloseSession()
	return c.requestStr.Close()
}
