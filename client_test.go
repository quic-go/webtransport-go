package webtransport_test

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marten-seemann/webtransport-go"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/quicvarint"

	"github.com/stretchr/testify/require"
)

type delayedStream struct {
	done <-chan struct{}
	quic.Stream
}

func (s *delayedStream) Read(b []byte) (int, error) {
	<-s.done
	return s.Stream.Read(b)
}

type requestStreamDelayingConn struct {
	done    <-chan struct{}
	counter int32
	quic.EarlyConnection
}

func (c *requestStreamDelayingConn) OpenStreamSync(ctx context.Context) (quic.Stream, error) {
	str, err := c.EarlyConnection.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	if atomic.CompareAndSwapInt32(&c.counter, 0, 1) {
		return &delayedStream{done: c.done, Stream: str}, nil
	}
	return str, nil
}

func TestClientInvalidResponseHandling(t *testing.T) {
	tlsConf := tlsConf.Clone()
	tlsConf.NextProtos = []string{"h3"}
	s, err := quic.ListenAddr("localhost:0", tlsConf, nil)
	require.NoError(t, err)
	errChan := make(chan error)
	go func() {
		conn, err := s.Accept(context.Background())
		require.NoError(t, err)
		str, err := conn.AcceptStream(context.Background())
		require.NoError(t, err)
		// write a HTTP3 data frame. This will cause an error, since a HEADERS frame is expected
		var b []byte
		b = quicvarint.Append(b, 0x0)
		b = quicvarint.Append(b, 1337)
		_, err = str.Write(b)
		require.NoError(t, err)
		for {
			if _, err := str.Read(make([]byte, 64)); err != nil {
				errChan <- err
				return
			}
		}
	}()

	d := webtransport.Dialer{
		RoundTripper: &http3.RoundTripper{
			TLSClientConfig: &tls.Config{RootCAs: certPool},
		},
	}
	_, _, err = d.Dial(context.Background(), fmt.Sprintf("https://localhost:%d", s.Addr().(*net.UDPAddr).Port), nil)
	require.Error(t, err)
	sErr := <-errChan
	require.Error(t, sErr)
	var appErr *quic.ApplicationError
	require.True(t, errors.As(sErr, &appErr))
	require.Equal(t, quic.ApplicationErrorCode(0x105), appErr.ErrorCode) // H3_FRAME_UNEXPECTED
}

func TestClientReorderedUpgrade(t *testing.T) {
	timeout := scaleDuration(100 * time.Millisecond)
	blockUpgrade := make(chan struct{})
	s := webtransport.Server{
		H3: http3.Server{TLSConfig: tlsConf},
	}
	addHandler(t, &s, func(c *webtransport.Session) {
		str, err := c.OpenStream()
		require.NoError(t, err)
		_, err = str.Write([]byte("foobar"))
		require.NoError(t, err)
		require.NoError(t, str.Close())
	})
	udpConn, err := net.ListenUDP("udp", nil)
	require.NoError(t, err)
	port := udpConn.LocalAddr().(*net.UDPAddr).Port
	go s.Serve(udpConn)

	d := webtransport.Dialer{
		RoundTripper: &http3.RoundTripper{
			TLSClientConfig: &tls.Config{RootCAs: certPool},
			Dial: func(ctx context.Context, addr string, tlsCfg *tls.Config, cfg *quic.Config) (quic.EarlyConnection, error) {
				conn, err := quic.DialAddrEarlyContext(ctx, addr, tlsCfg, cfg)
				if err != nil {
					return nil, err
				}
				return &requestStreamDelayingConn{done: blockUpgrade, EarlyConnection: conn}, nil
			},
		},
	}
	connChan := make(chan *webtransport.Session)
	go func() {
		// This will block until blockUpgrade is closed.
		rsp, conn, err := d.Dial(context.Background(), fmt.Sprintf("https://localhost:%d/webtransport", port), nil)
		require.NoError(t, err)
		require.Equal(t, 200, rsp.StatusCode)
		connChan <- conn
	}()

	time.Sleep(timeout)
	close(blockUpgrade)
	conn := <-connChan
	defer conn.CloseWithError(0, "")
	ctx, cancel := context.WithTimeout(context.Background(), scaleDuration(100*time.Millisecond))
	defer cancel()
	str, err := conn.AcceptStream(ctx)
	require.NoError(t, err)
	data, err := io.ReadAll(str)
	require.NoError(t, err)
	require.Equal(t, []byte("foobar"), data)
}
