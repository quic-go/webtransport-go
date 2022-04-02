package webtransport

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/lucas-clemente/quic-go"
	"github.com/lucas-clemente/quic-go/http3"
	"github.com/lucas-clemente/quic-go/quicvarint"
)

type queueEntry struct {
	Time       time.Time
	SessionID  sessionID
	Connection quic.Connection
	Stream     quic.Stream
}

type Dialer struct {
	TLSClientConf *tls.Config

	ctx       context.Context
	ctxCancel context.CancelFunc

	initOnce     sync.Once
	roundTripper *http3.RoundTripper

	connMutex sync.Mutex
	conns     map[http3.StreamCreator]map[sessionID]*Conn

	streamMutex      sync.Mutex
	streamQueueAdded chan struct{}
	streamQueue      []queueEntry
}

func (d *Dialer) Dial(ctx context.Context, urlStr string, reqHdr http.Header) (*http.Response, *Conn, error) {
	d.initOnce.Do(func() {
		d.ctx, d.ctxCancel = context.WithCancel(context.Background())
		d.conns = make(map[http3.StreamCreator]map[sessionID]*Conn)
		d.streamQueueAdded = make(chan struct{}, 1)
		d.roundTripper = &http3.RoundTripper{
			TLSClientConfig:    d.TLSClientConf,
			QuicConfig:         &quic.Config{MaxIncomingStreams: 100, MaxIncomingUniStreams: 100},
			EnableDatagrams:    true,
			AdditionalSettings: map[uint64]uint64{settingsEnableWebtransport: 1},
			StreamHijacker: func(ft http3.FrameType, conn quic.Connection, str quic.Stream) (hijacked bool, err error) {
				if ft != webTransportFrameType {
					return false, nil
				}
				sID, err := quicvarint.Read(quicvarint.NewReader(str))
				if err != nil {
					return false, err
				}
				d.streamMutex.Lock()
				d.streamQueue = append(d.streamQueue, queueEntry{
					Time:       time.Now(),
					Connection: conn,
					SessionID:  sessionID(sID),
					Stream:     str,
				})
				d.streamMutex.Unlock()
				select {
				case d.streamQueueAdded <- struct{}{}:
				default:
				}
				return true, nil
			},
		}
		go func() {
			if err := d.run(); err != nil {
				log.Fatal(err)
			}
		}()
	})

	u, err := url.Parse(urlStr)
	if err != nil {
		return nil, nil, err
	}
	if reqHdr == nil {
		reqHdr = http.Header{}
	}
	reqHdr.Add(webTransportDraftOfferHeaderKey, "1")
	req := &http.Request{
		Method: http.MethodConnect,
		Header: reqHdr,
		Proto:  "webtransport",
		Host:   u.Host,
		URL:    u,
	}
	req = req.WithContext(ctx)

	rsp, err := d.roundTripper.RoundTripOpt(req, http3.RoundTripOpt{})
	if err != nil {
		return nil, nil, err
	}
	if rsp.StatusCode < 200 || rsp.StatusCode >= 300 {
		return rsp, nil, fmt.Errorf("received status %d", rsp.StatusCode)
	}
	sess := rsp.Body.(http3.Hijacker).StreamCreator()
	conns, ok := d.conns[sess]
	if !ok {
		conns = make(map[sessionID]*Conn)
		d.conns[sess] = conns
	}
	sID := sessionID(rsp.Body.(streamIDGetter).StreamID())
	conn := newConn(sID, sess, rsp.Body)

	d.connMutex.Lock()
	conns[sID] = conn
	d.connMutex.Unlock()
	return rsp, conn, nil
}

func (d *Dialer) Close() error {
	d.ctxCancel()
	return nil
}

func (d *Dialer) run() error {
	var hasMore bool
	for {
		if !hasMore {
			select {
			case <-d.streamQueueAdded:
			case <-d.ctx.Done():
				return nil
			}
		}

		d.streamMutex.Lock()
		e := d.streamQueue[0]
		d.streamQueue = d.streamQueue[1:]
		hasMore = len(d.streamQueue) > 0
		d.streamMutex.Unlock()

		d.connMutex.Lock()
		sessions, ok := d.conns[e.Connection]
		if !ok {
			log.Println("no connection found")
		}
		d.connMutex.Unlock()
		// The client establishes sessions.
		// We should never be in a situation where we receive a session ID that we don't know.
		// TODO: check if this holds true once we start cleaning up sessions.
		conn, ok := sessions[e.SessionID]
		if !ok {
			return fmt.Errorf("no session with ID %d found", e.SessionID)
		}
		conn.addStream(e.Stream)
	}
}
