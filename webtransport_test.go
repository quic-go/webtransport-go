package webtransport_test

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/lucas-clemente/quic-go/http3"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marten-seemann/webtransport-go"

	"github.com/stretchr/testify/require"
)

func runServer(t *testing.T, s *webtransport.Server) (addr *net.UDPAddr, close func()) {
	laddr, err := net.ResolveUDPAddr("udp", "localhost:0")
	require.NoError(t, err)
	udpConn, err := net.ListenUDP("udp", laddr)
	require.NoError(t, err)

	servErr := make(chan error, 1)
	go func() {
		servErr <- s.Serve(udpConn)
	}()

	return udpConn.LocalAddr().(*net.UDPAddr), func() {
		require.NoError(t, s.Close())
		<-servErr
		udpConn.Close()
	}
}

func establishConn(t *testing.T, handler func(*webtransport.Conn)) (conn *webtransport.Conn, close func()) {
	s := &webtransport.Server{
		H3: http3.Server{TLSConfig: tlsConf},
	}
	addHandler(t, s, handler)

	addr, closeServer := runServer(t, s)
	d := webtransport.Dialer{TLSClientConf: &tls.Config{RootCAs: certPool}}
	defer d.Close()
	url := fmt.Sprintf("https://localhost:%d/webtransport", addr.Port)
	rsp, conn, err := d.Dial(context.Background(), url, nil)
	require.NoError(t, err)
	require.Equal(t, 200, rsp.StatusCode)
	return conn, func() {
		closeServer()
		s.Close()
	}
}

// opens a new stream on the connection,
// sends data and checks the echoed data.
func sendDataAndCheckEcho(t *testing.T, conn *webtransport.Conn) {
	t.Helper()
	data := getRandomData(5 * 1024)
	str, err := conn.OpenStream()
	require.NoError(t, err)
	str.SetDeadline(time.Now().Add(time.Second))
	_, err = str.Write(data)
	require.NoError(t, err)
	require.NoError(t, str.Close())
	reply, err := io.ReadAll(str)
	require.NoError(t, err)
	require.Equal(t, data, reply)
}

func addHandler(t *testing.T, s *webtransport.Server, connHandler func(*webtransport.Conn)) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/webtransport", func(w http.ResponseWriter, r *http.Request) {
		conn, err := s.Upgrade(w, r)
		if err != nil {
			t.Logf("upgrading failed: %s", err)
			w.WriteHeader(404) // TODO: better error code
			return
		}
		connHandler(conn)
	})
	s.H3.Handler = mux
}

func newEchoHandler(t *testing.T) func(*webtransport.Conn) {
	return func(conn *webtransport.Conn) {
		for {
			str, err := conn.AcceptStream(context.Background())
			if err != nil {
				break
			}
			_, err = io.CopyBuffer(str, str, make([]byte, 100))
			require.NoError(t, err)
			require.NoError(t, str.Close())
		}
	}
}

func getRandomData(l int) []byte {
	data := make([]byte, l)
	rand.Read(data)
	return data
}

func TestBidirectionalStreams(t *testing.T) {
	t.Run("client-initiated", func(t *testing.T) {
		conn, closeServer := establishConn(t, newEchoHandler(t))
		defer closeServer()

		sendDataAndCheckEcho(t, conn)
	})

	t.Run("server-initiated", func(t *testing.T) {
		done := make(chan struct{})
		conn, closeServer := establishConn(t, func(conn *webtransport.Conn) {
			defer close(done)
			sendDataAndCheckEcho(t, conn)
		})
		defer closeServer()

		go newEchoHandler(t)(conn)
		<-done
	})
}

func TestUnidirectionalStreams(t *testing.T) {
	conn, closeServer := establishConn(t, func(conn *webtransport.Conn) {
		// Accept a unidirectional stream, read all of its contents,
		// and echo it on a newly opened unidirectional stream.
		str, err := conn.AcceptUniStream(context.Background())
		require.NoError(t, err)
		data, err := io.ReadAll(str)
		require.NoError(t, err)
		rstr, err := conn.OpenUniStream()
		require.NoError(t, err)
		_, err = rstr.Write(data)
		require.NoError(t, err)
		require.NoError(t, rstr.Close())
		<-conn.Context().Done()
	})
	defer closeServer()

	str, err := conn.OpenUniStream()
	require.NoError(t, err)
	data := getRandomData(10 * 1024)
	_, err = str.Write(data)
	require.NoError(t, err)
	require.NoError(t, str.Close())
	rstr, err := conn.AcceptUniStream(context.Background())
	require.NoError(t, err)
	rdata, err := io.ReadAll(rstr)
	require.NoError(t, err)
	require.Equal(t, data, rdata)
}

func TestMultipleClients(t *testing.T) {
	const numClients = 5
	s := &webtransport.Server{
		H3: http3.Server{TLSConfig: tlsConf},
	}
	defer s.Close()
	addHandler(t, s, newEchoHandler(t))

	addr, closeServer := runServer(t, s)
	defer closeServer()

	var wg sync.WaitGroup
	wg.Add(numClients)
	for i := 0; i < numClients; i++ {
		go func() {
			defer wg.Done()
			d := webtransport.Dialer{
				TLSClientConf: &tls.Config{RootCAs: certPool},
			}
			defer d.Close()
			url := fmt.Sprintf("https://localhost:%d/webtransport", addr.Port)
			rsp, conn, err := d.Dial(context.Background(), url, nil)
			require.NoError(t, err)
			require.Equal(t, 200, rsp.StatusCode)
			sendDataAndCheckEcho(t, conn)
		}()
	}
	wg.Wait()
}

func TestStreamResetError(t *testing.T) {
	const errorCode webtransport.ErrorCode = 127
	conn, closeServer := establishConn(t, func(conn *webtransport.Conn) {
		for {
			str, err := conn.AcceptStream(context.Background())
			if err != nil {
				return
			}
			str.CancelRead(errorCode)
			str.CancelWrite(errorCode)
		}
	})
	defer closeServer()

	str, err := conn.OpenStream()
	require.NoError(t, err)
	_, err = str.Write([]byte("foobar"))
	require.NoError(t, err)
	_, err = str.Read([]byte{0})
	require.Error(t, err)
	var strErr *webtransport.StreamError
	require.True(t, errors.As(err, &strErr))
	require.Equal(t, strErr.ErrorCode, errorCode)
}

func TestShutdown(t *testing.T) {
	conn, closeServer := establishConn(t, func(conn *webtransport.Conn) {
		conn.Close()
	})
	defer closeServer()

	_, err := conn.AcceptStream(context.Background())
	require.Error(t, err)
	_, err = conn.AcceptUniStream(context.Background())
	require.Error(t, err)
}

func TestOpenStreamSyncShutdown(t *testing.T) {
	runTest := func(t *testing.T, openStream, openStreamSync func() error, done chan struct{}) {
		t.Helper()

		// open as many streams as the server lets us
		for {
			if err := openStream(); err != nil {
				break
			}
		}

		const num = 3
		errChan := make(chan error, num)
		for i := 0; i < num; i++ {
			go func() { errChan <- openStreamSync() }()
		}

		// make sure the 3 calls to OpenStreamSync are actually blocked
		require.Never(t, func() bool { return len(errChan) > 0 }, 100*time.Millisecond, 10*time.Millisecond)
		close(done)
		require.Eventually(t, func() bool { return len(errChan) == 3 }, scaleDuration(100*time.Millisecond), 10*time.Millisecond)
		for i := 0; i < 3; i++ {
			err := <-errChan
			var connErr *webtransport.ConnectionError
			require.ErrorAs(t, err, &connErr)
		}
	}

	t.Run("bidirectional streams", func(t *testing.T) {
		done := make(chan struct{})
		conn, closeServer := establishConn(t, func(conn *webtransport.Conn) {
			<-done
			conn.Close()
		})
		defer closeServer()

		runTest(t,
			func() error { _, err := conn.OpenStream(); return err },
			func() error { _, err := conn.OpenStreamSync(context.Background()); return err },
			done,
		)
	})

	t.Run("unidirectional streams", func(t *testing.T) {
		done := make(chan struct{})
		conn, closeServer := establishConn(t, func(conn *webtransport.Conn) {
			<-done
			conn.Close()
		})
		defer closeServer()

		runTest(t,
			func() error { _, err := conn.OpenUniStream(); return err },
			func() error { _, err := conn.OpenUniStreamSync(context.Background()); return err },
			done,
		)
	})
}

func TestCheckOrigin(t *testing.T) {
	type tc struct {
		Name        string
		CheckOrigin func(*http.Request) bool
		Origin      string
		Result      bool
	}

	tcs := []tc{
		{
			Name:   "using default CheckOrigin, no Origin header",
			Result: true,
		},
		{
			Name:   "using default CheckOrigin, Origin: localhost",
			Origin: "https://localhost:%port%",
			Result: true,
		},
		{
			Name:   "using default CheckOrigin, Origin: google.com",
			Origin: "google.com",
			Result: false,
		},
		{
			Name:        "using custom CheckOrigin, always correct",
			CheckOrigin: func(r *http.Request) bool { return true },
			Origin:      "google.com",
			Result:      true,
		},
		{
			Name:        "using custom CheckOrigin, always incorrect",
			CheckOrigin: func(r *http.Request) bool { return false },
			Origin:      "google.com",
			Result:      false,
		},
	}

	for _, tc := range tcs {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			s := &webtransport.Server{
				H3:          http3.Server{TLSConfig: tlsConf},
				CheckOrigin: tc.CheckOrigin,
			}
			defer s.Close()
			addHandler(t, s, newEchoHandler(t))

			addr, closeServer := runServer(t, s)
			defer closeServer()

			d := webtransport.Dialer{
				TLSClientConf: &tls.Config{RootCAs: certPool},
			}
			defer d.Close()
			url := fmt.Sprintf("https://localhost:%d/webtransport", addr.Port)
			hdr := make(http.Header)
			hdr.Add("Origin", strings.ReplaceAll(tc.Origin, "%port%", strconv.Itoa(addr.Port)))
			rsp, conn, err := d.Dial(context.Background(), url, hdr)
			if tc.Result {
				require.NoError(t, err)
				require.Equal(t, 200, rsp.StatusCode)
				defer conn.Close()
			} else {
				require.Equal(t, 404, rsp.StatusCode)
			}
		})
	}
}
