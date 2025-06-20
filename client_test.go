package webtransport_test

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/quic-go/webtransport-go"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/quicvarint"

	"github.com/stretchr/testify/require"
)

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
	s, err := quic.ListenAddr("localhost:0", webtransport.TLSConf, &quic.Config{EnableDatagrams: true})
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

	d := webtransport.Dialer{TLSClientConfig: &tls.Config{RootCAs: webtransport.CertPool}}
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
		t.Run(tc.name, func(t *testing.T) {
			ln, err := quic.ListenAddr("localhost:0", webtransport.TLSConf, &quic.Config{EnableDatagrams: true})
			require.NoError(t, err)
			defer ln.Close()

			done := make(chan struct{})
			ctx, cancel := context.WithCancel(context.Background())
			go func() {
				defer close(done)
				conn, err := ln.Accept(context.Background())
				require.NoError(t, err)
				// send the SETTINGS frame
				settingsStr, err := conn.OpenUniStream()
				require.NoError(t, err)
				_, err = settingsStr.Write(appendSettingsFrame([]byte{0} /* stream type */, tc.settings))
				require.NoError(t, err)
				if _, err := conn.AcceptStream(ctx); err == nil || !errors.Is(err, context.Canceled) {
					require.Fail(t, "didn't expect any stream to be accepted")
				}
			}()

			d := webtransport.Dialer{TLSClientConfig: &tls.Config{RootCAs: webtransport.CertPool}}
			_, _, err = d.Dial(context.Background(), fmt.Sprintf("https://localhost:%d", ln.Addr().(*net.UDPAddr).Port), nil)
			require.Error(t, err)
			require.ErrorContains(t, err, tc.errorStr)
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("timeout")
			}
		})
	}
}
