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

	"github.com/stretchr/testify/assert"
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
	ln, err := quic.ListenAddr("localhost:0", webtransport.TLSConf, &quic.Config{EnableDatagrams: true})
	require.NoError(t, err)
	defer ln.Close()

	errChan := make(chan error, 1)
	go func() {
		conn, err := ln.Accept(context.Background())
		if err != nil {
			errChan <- err
			return
		}
		// send the SETTINGS frame
		settingsStr, err := conn.OpenUniStream()
		if err != nil {
			errChan <- err
			return
		}
		_, err = settingsStr.Write(appendSettingsFrame([]byte{0} /* stream type */, map[uint64]uint64{
			settingDatagram:            1,
			settingExtendedConnect:     1,
			settingsEnableWebtransport: 1,
		}))
		if err != nil {
			errChan <- err
			return
		}

		str, err := conn.AcceptStream(context.Background())
		if err != nil {
			errChan <- err
			return
		}
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
	_, _, err = d.Dial(context.Background(), fmt.Sprintf("https://localhost:%d", ln.Addr().(*net.UDPAddr).Port), nil)
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

func TestClientWaitForSettingsTimeout(t *testing.T) {
	ln, err := quic.ListenAddr("localhost:0", webtransport.TLSConf, &quic.Config{EnableDatagrams: true})
	require.NoError(t, err)
	defer ln.Close()

	connChan := make(chan *quic.Conn, 1)
	go func() {
		conn, err := ln.Accept(context.Background())
		if err != nil {
			return
		}
		connChan <- conn
	}()

	ctx, cancel := context.WithCancelCause(context.Background())
	errChan := make(chan error)
	go func() {
		d := webtransport.Dialer{TLSClientConfig: &tls.Config{RootCAs: webtransport.CertPool}}
		_, _, err := d.Dial(ctx, fmt.Sprintf("https://localhost:%d", ln.Addr().(*net.UDPAddr).Port), nil)
		errChan <- err
	}()

	var serverConn *quic.Conn
	select {
	case serverConn = <-connChan:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for connection")
	}

	cancel(assert.AnError)

	select {
	case err := <-errChan:
		require.Error(t, err)
		require.ErrorContains(t, err, "error waiting for HTTP/3 settings")
		require.ErrorIs(t, err, assert.AnError)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for dial to complete")
	}

	// the client should close the underlying QUIC connection
	select {
	case <-serverConn.Context().Done():
		require.ErrorIs(t,
			context.Cause(serverConn.Context()),
			&quic.ApplicationError{ErrorCode: quic.ApplicationErrorCode(http3.ErrCodeNoError), Remote: true},
		)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for client to close connection")
	}
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

			connChan := make(chan *quic.Conn, 1)
			go func() {
				conn, err := ln.Accept(context.Background())
				if err != nil {
					t.Errorf("failed to accept connection: %v", err)
					return
				}
				// send the SETTINGS frame
				settingsStr, err := conn.OpenUniStream()
				if err != nil {
					t.Errorf("failed to open uni stream: %v", err)
					return
				}
				if _, err := settingsStr.Write(appendSettingsFrame([]byte{0} /* stream type */, tc.settings)); err != nil {
					t.Errorf("failed to write settings frame: %v", err)
					return
				}
				connChan <- conn
			}()

			d := webtransport.Dialer{TLSClientConfig: &tls.Config{RootCAs: webtransport.CertPool}}
			_, _, err = d.Dial(context.Background(), fmt.Sprintf("https://localhost:%d", ln.Addr().(*net.UDPAddr).Port), nil)
			require.Error(t, err)
			require.ErrorContains(t, err, tc.errorStr)

			var serverConn *quic.Conn
			select {
			case serverConn = <-connChan:
			case <-time.After(5 * time.Second):
				t.Fatal("timeout")
			}

			// the client should close the underlying QUIC connection
			select {
			case <-serverConn.Context().Done():
				require.ErrorIs(t,
					context.Cause(serverConn.Context()),
					&quic.ApplicationError{ErrorCode: quic.ApplicationErrorCode(http3.ErrCodeNoError), Remote: true},
				)
			case <-time.After(time.Second):
				t.Fatal("timeout waiting for client to close connection")
			}
		})
	}
}
