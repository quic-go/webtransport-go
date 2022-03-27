package webtransport

import (
	"bytes"
	"context"
	"io"
	"sync"

	"github.com/lucas-clemente/quic-go"
	"github.com/lucas-clemente/quic-go/http3"
	"github.com/lucas-clemente/quic-go/quicvarint"
)

// sessionID is the WebTransport Session ID
type sessionID uint64

type Conn struct {
	sessionID  sessionID
	qconn      http3.StreamCreator
	requestStr io.Reader // TODO: this needs to be an io.ReadWriteCloser so we can close the stream

	acceptMx   sync.Mutex
	acceptChan chan struct{}
	// Contains all the streams waiting to be accepted.
	// There's no explicit limit to the length of the queue, but it is implicitly
	// limited by the stream flow control provided by QUIC.
	acceptQueue []quic.Stream
}

func newConn(sessionID sessionID, qconn http3.StreamCreator, requestStr io.Reader) *Conn {
	c := &Conn{
		sessionID:  sessionID,
		qconn:      qconn,
		requestStr: requestStr,
		acceptChan: make(chan struct{}, 1),
	}
	return c
}

func (c *Conn) addStream(str quic.Stream) {
	c.acceptMx.Lock()
	defer c.acceptMx.Unlock()

	c.acceptQueue = append(c.acceptQueue, str)
	select {
	case c.acceptChan <- struct{}{}:
	default:
	}
}

// Context returns a context that is closed when the connection is closed.
func (c *Conn) Context() context.Context {
	return context.Background() // TODO: fix
}

func (c *Conn) AcceptStream(ctx context.Context) (Stream, error) {
	var str quic.Stream
	c.acceptMx.Lock()
	if len(c.acceptQueue) > 0 {
		str = c.acceptQueue[0]
		c.acceptQueue = c.acceptQueue[1:]
	}
	c.acceptMx.Unlock()
	if str != nil {
		return &stream{str}, nil
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.acceptChan:
		return c.AcceptStream(ctx)
	}
}

func (c *Conn) OpenStream() (Stream, error) {
	str, err := c.qconn.OpenStream()
	if err != nil {
		return nil, err
	}
	if err := c.writeStreamHeader(str); err != nil {
		return nil, err
	}
	return &stream{str: str}, nil
}

func (c *Conn) OpenStreamSync(ctx context.Context) (Stream, error) {
	str, err := c.qconn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	// TODO: this should probably respect the context
	if err := c.writeStreamHeader(str); err != nil {
		return nil, err
	}
	return &stream{str: str}, nil
}

func (c *Conn) writeStreamHeader(str quic.Stream) error {
	buf := bytes.NewBuffer(make([]byte, 0, 9)) // 1 byte for the frame type, up to 8 bytes for the session ID
	quicvarint.Write(buf, webTransportFrameType)
	quicvarint.Write(buf, uint64(c.sessionID))
	_, err := str.Write(buf.Bytes())
	return err
}

func (c *Conn) Close() error {
	return nil
}
