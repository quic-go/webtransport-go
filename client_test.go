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

	"github.com/quic-go/webtransport-go"

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

const (
	// Extended CONNECT, RFC 9220
	settingExtendedConnect = 0x8
	// HTTP Datagrams, RFC 9297
	settingDatagram = 0x33
	// WebTransport
	settingsEnableWebtransport = 0x2b603742
)

// appendSettingsFrame serializes an HTTP/3 SETTINGS frame
// It reimplements the function in the http3 package, in a slightly simplified way.
func appendSettingsFrame(b []byte, values map[uint64]uint64) []byte {
	b = quicvarint.Append(b, 0x4)
	var l uint64
	for k, val := range values {
		l += uint64(quicvarint.Len(k)) + uint64(quicvarint.Len(val))
	}
	b = quicvarint.Append(b, l)
	for id, val := range values {
		b = quicvarint.Append(b, id)
		b = quicvarint.Append(b, val)
	}
	return b
}

func TestClientInvalidResponseHandling(t *testing.T) {
	tlsConf := tlsConf.Clone()
	tlsConf.NextProtos = []string{"h3"}
	s, err := quic.ListenAddr("localhost:0", tlsConf, &quic.Config{EnableDatagrams: true})
	require.NoError(t, err)
	errChan := make(chan error)
	go func() {
		conn, err := s.Accept(context.Background())
		require.NoError(t, err)
		// send the SETTINGS frame
		settingsStr, err := conn.OpenUniStream()
		require.NoError(t, err)
		_, err = settingsStr.Write(appendSettingsFrame([]byte{0} /* stream type */, map[uint64]uint64{
			settingDatagram:            1,
			settingExtendedConnect:     1,
			settingsEnableWebtransport: 1,
		}))
		require.NoError(t, err)

		str, err := conn.AcceptStream(context.Background())
		require.NoError(t, err)
		// write an HTTP/3 data frame. This will cause an error, since a HEADERS frame is expected
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
	var sErr error
	select {
	case sErr = <-errChan:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}
	require.Error(t, sErr)
	var appErr *quic.ApplicationError
	require.True(t, errors.As(sErr, &appErr))
	require.Equal(t, http3.ErrCodeFrameUnexpected, http3.ErrCode(appErr.ErrorCode))
}

func TestClientInvalidSettingsHandling(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings map[uint64]uint64
		errorStr string
	}{
		{
			name: "Extended CONNECT disabled",
			settings: map[uint64]uint64{
				settingDatagram:            1,
				settingExtendedConnect:     0,
				settingsEnableWebtransport: 1,
			},
			errorStr: "server didn't enable Extended CONNECT",
		},
		{
			name: "HTTP/3 DATAGRAMs disabled",
			settings: map[uint64]uint64{
				settingDatagram:            0,
				settingExtendedConnect:     1,
				settingsEnableWebtransport: 1,
			},
			errorStr: "server didn't enable HTTP/3 datagram support",
		},
		{
			name: "WebTransport disabled",
			settings: map[uint64]uint64{
				settingDatagram:            1,
				settingExtendedConnect:     1,
				settingsEnableWebtransport: 0,
			},
			errorStr: "server didn't enable WebTransport",
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			tlsConf := tlsConf.Clone()
			tlsConf.NextProtos = []string{"h3"}
			s, err := quic.ListenAddr("localhost:0", tlsConf, &quic.Config{EnableDatagrams: true})
			require.NoError(t, err)
			go func() {
				conn, err := s.Accept(context.Background())
				require.NoError(t, err)
				// send the SETTINGS frame
				settingsStr, err := conn.OpenUniStream()
				require.NoError(t, err)
				_, err = settingsStr.Write(appendSettingsFrame([]byte{0} /* stream type */, tc.settings))
				require.NoError(t, err)
			}()

			d := webtransport.Dialer{
				RoundTripper: &http3.RoundTripper{
					TLSClientConfig: &tls.Config{RootCAs: certPool},
				},
			}
			_, _, err = d.Dial(context.Background(), fmt.Sprintf("https://localhost:%d", s.Addr().(*net.UDPAddr).Port), nil)
			require.Error(t, err)
			require.ErrorContains(t, err, tc.errorStr)
		})

	}
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
				conn, err := quic.DialAddrEarly(ctx, addr, tlsCfg, cfg)
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
