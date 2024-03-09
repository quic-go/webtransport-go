package webtransport_test

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/webtransport-go"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/logging"
	"github.com/quic-go/quic-go/qlog"

	"github.com/stretchr/testify/require"
)

// create a qlog file in QLOGDIR, if that environment variable is set
func getQlogger(t *testing.T) func(context.Context, logging.Perspective, quic.ConnectionID) *logging.ConnectionTracer {
	tracer := func(ctx context.Context, p logging.Perspective, connID quic.ConnectionID) *logging.ConnectionTracer {
		qlogDir := os.Getenv("QLOGDIR")
		if qlogDir == "" {
			return nil
		}
		if _, err := os.Stat(qlogDir); os.IsNotExist(err) {
			require.NoError(t, os.MkdirAll(qlogDir, 0755))
		}
		role := "server"
		if p == logging.PerspectiveClient {
			role = "client"
		}
		filename := fmt.Sprintf("./%s/log_%x_%s.qlog", qlogDir, connID, role)
		t.Log("creating", filename)
		f, err := os.Create(filename)
		require.NoError(t, err)
		return qlog.NewConnectionTracer(f, p, connID)
	}
	return tracer
}

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

func establishSession(t *testing.T, handler func(*webtransport.Session)) (sess *webtransport.Session, close func()) {
	s := &webtransport.Server{
		H3: http3.Server{
			TLSConfig:  tlsConf,
			QuicConfig: &quic.Config{Tracer: getQlogger(t), EnableDatagrams: true},
		},
	}
	addHandler(t, s, handler)

	addr, closeServer := runServer(t, s)
	d := webtransport.Dialer{
		RoundTripper: &http3.RoundTripper{
			TLSClientConfig: &tls.Config{RootCAs: certPool},
			QuicConfig:      &quic.Config{Tracer: getQlogger(t), EnableDatagrams: true},
		},
	}
	defer d.Close()
	url := fmt.Sprintf("https://localhost:%d/webtransport", addr.Port)
	rsp, sess, err := d.Dial(context.Background(), url, nil)
	require.NoError(t, err)
	require.Equal(t, 200, rsp.StatusCode)
	return sess, func() {
		closeServer()
		s.Close()
		d.RoundTripper.Close()
	}
}

// opens a new stream on the connection,
// sends data and checks the echoed data.
func sendDataAndCheckEcho(t *testing.T, sess *webtransport.Session) {
	t.Helper()
	data := getRandomData(5 * 1024)
	str, err := sess.OpenStream()
	require.NoError(t, err)
	str.SetDeadline(time.Now().Add(time.Second))
	_, err = str.Write(data)
	require.NoError(t, err)
	require.NoError(t, str.Close())
	reply, err := io.ReadAll(str)
	require.NoError(t, err)
	require.Equal(t, data, reply)
}

func addHandler(t *testing.T, s *webtransport.Server, connHandler func(*webtransport.Session)) {
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

func newEchoHandler(t *testing.T) func(*webtransport.Session) {
	return func(sess *webtransport.Session) {
		for {
			str, err := sess.AcceptStream(context.Background())
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

func TestBidirectionalStreamsDataTransfer(t *testing.T) {
	t.Run("client-initiated", func(t *testing.T) {
		conn, closeServer := establishSession(t, newEchoHandler(t))
		defer closeServer()
		defer conn.CloseWithError(0, "")

		sendDataAndCheckEcho(t, conn)
	})

	t.Run("server-initiated", func(t *testing.T) {
		done := make(chan struct{})
		conn, closeServer := establishSession(t, func(sess *webtransport.Session) {
			sendDataAndCheckEcho(t, sess)
			close(done) // don't defer this, the HTTP handler catches panics
		})
		defer closeServer()
		defer conn.CloseWithError(0, "")

		go newEchoHandler(t)(conn)
		<-done
	})
}

func TestStreamsImmediateClose(t *testing.T) {
	t.Run("bidirectional streams", func(t *testing.T) {
		t.Run("client-initiated", func(t *testing.T) {
			done := make(chan struct{})
			conn, closeServer := establishSession(t, func(c *webtransport.Session) {
				str, err := c.AcceptStream(context.Background())
				require.NoError(t, err)
				n, err := str.Read([]byte{0})
				require.Zero(t, n)
				require.ErrorIs(t, err, io.EOF)
				require.NoError(t, str.Close())
				close(done) // don't defer this, the HTTP handler catches panics
			})
			defer closeServer()
			defer conn.CloseWithError(0, "")

			str, err := conn.OpenStream()
			require.NoError(t, err)
			require.NoError(t, str.Close())
			n, err := str.Read([]byte{0})
			require.Zero(t, n)
			require.ErrorIs(t, err, io.EOF)
			<-done
		})

		t.Run("server-initiated", func(t *testing.T) {
			done := make(chan struct{})
			conn, closeServer := establishSession(t, func(c *webtransport.Session) {
				str, err := c.OpenStream()
				require.NoError(t, err)
				require.NoError(t, str.Close())
				n, err := str.Read([]byte{0})
				require.Zero(t, n)
				require.ErrorIs(t, err, io.EOF)
				require.NoError(t, c.CloseWithError(0, ""))
				close(done) // don't defer this, the HTTP handler catches panics
			})
			defer closeServer()
			defer conn.CloseWithError(0, "")

			str, err := conn.AcceptStream(context.Background())
			require.NoError(t, err)
			n, err := str.Read([]byte{0})
			require.Zero(t, n)
			require.ErrorIs(t, err, io.EOF)
			require.NoError(t, str.Close())
			<-done
		})
	})

	t.Run("unidirectional", func(t *testing.T) {
		t.Run("client-initiated", func(t *testing.T) {
			sess, closeServer := establishSession(t, func(c *webtransport.Session) {
				defer c.CloseWithError(0, "")
				str, err := c.AcceptUniStream(context.Background())
				require.NoError(t, err)
				n, err := str.Read([]byte{0})
				require.Zero(t, n)
				require.ErrorIs(t, err, io.EOF)
			})
			defer closeServer()

			str, err := sess.OpenUniStream()
			require.NoError(t, err)
			require.NoError(t, str.Close())
			<-sess.Context().Done()
		})

		t.Run("server-initiated", func(t *testing.T) {
			sess, closeServer := establishSession(t, func(c *webtransport.Session) {
				str, err := c.OpenUniStream()
				require.NoError(t, err)
				require.NoError(t, str.Close())
			})
			defer closeServer()
			defer sess.CloseWithError(0, "")

			str, err := sess.AcceptUniStream(context.Background())
			require.NoError(t, err)
			n, err := str.Read([]byte{0})
			require.Zero(t, n)
			require.ErrorIs(t, err, io.EOF)
		})
	})
}

func TestStreamsImmediateReset(t *testing.T) {
	// This tests ensures that we correctly process the error code that occurs when quic-go reads the frame type.
	// If we don't process the error code correctly and fail to hijack the stream,
	// quic-go will see a bidirectional stream opened by the server, which is a connection error.
	done := make(chan struct{})
	defer close(done)
	sess, closeServer := establishSession(t, func(c *webtransport.Session) {
		for i := 0; i < 50; i++ {
			str, err := c.OpenStream()
			require.NoError(t, err)

			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				str.CancelWrite(42)
			}()

			go func() {
				defer wg.Done()
				str.Write([]byte("foobar"))
			}()

			wg.Wait()
		}
	})
	defer closeServer()
	defer sess.CloseWithError(0, "")

	ctx, cancel := context.WithTimeout(context.Background(), scaleDuration(100*time.Millisecond))
	defer cancel()
	for {
		_, err := sess.AcceptStream(ctx)
		if err == context.DeadlineExceeded {
			break
		}
		require.NoError(t, err)
	}
}

func TestUnidirectionalStreams(t *testing.T) {
	sess, closeServer := establishSession(t, func(sess *webtransport.Session) {
		// Accept a unidirectional stream, read all of its contents,
		// and echo it on a newly opened unidirectional stream.
		str, err := sess.AcceptUniStream(context.Background())
		require.NoError(t, err)
		data, err := io.ReadAll(str)
		require.NoError(t, err)
		rstr, err := sess.OpenUniStream()
		require.NoError(t, err)
		_, err = rstr.Write(data)
		require.NoError(t, err)
		require.NoError(t, rstr.Close())
	})
	defer closeServer()
	defer sess.CloseWithError(0, "")

	str, err := sess.OpenUniStream()
	require.NoError(t, err)
	data := getRandomData(10 * 1024)
	_, err = str.Write(data)
	require.NoError(t, err)
	require.NoError(t, str.Close())
	rstr, err := sess.AcceptUniStream(context.Background())
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
				RoundTripper: &http3.RoundTripper{
					TLSClientConfig: &tls.Config{RootCAs: certPool},
					QuicConfig:      &quic.Config{Tracer: getQlogger(t), EnableDatagrams: true},
				},
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
	const errorCode webtransport.StreamErrorCode = 127
	sess, closeServer := establishSession(t, func(sess *webtransport.Session) {
		for {
			str, err := sess.AcceptStream(context.Background())
			if err != nil {
				return
			}
			str.CancelRead(errorCode)
			str.CancelWrite(errorCode)
		}
	})
	defer closeServer()

	str, err := sess.OpenStream()
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
	done := make(chan struct{})
	sess, closeServer := establishSession(t, func(sess *webtransport.Session) {
		sess.CloseWithError(1337, "foobar")
		var connErr *webtransport.ConnectionError
		_, err := sess.OpenStream()
		require.True(t, errors.As(err, &connErr))
		require.False(t, connErr.Remote)
		require.Equal(t, webtransport.SessionErrorCode(1337), connErr.ErrorCode)
		require.Equal(t, "foobar", connErr.Message)
		_, err = sess.OpenUniStream()
		require.True(t, errors.As(err, &connErr))
		require.False(t, connErr.Remote)

		close(done) // don't defer this, the HTTP handler catches panics
	})
	defer closeServer()

	var connErr *webtransport.ConnectionError
	_, err := sess.AcceptStream(context.Background())
	require.True(t, errors.As(err, &connErr))
	require.True(t, connErr.Remote)
	require.Equal(t, webtransport.SessionErrorCode(1337), connErr.ErrorCode)
	require.Equal(t, "foobar", connErr.Message)
	_, err = sess.AcceptUniStream(context.Background())
	require.Error(t, err)
	<-done
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
		require.Eventually(t, func() bool { return len(errChan) == num }, scaleDuration(100*time.Millisecond), 10*time.Millisecond)
		for i := 0; i < num; i++ {
			err := <-errChan
			var connErr *webtransport.ConnectionError
			require.ErrorAs(t, err, &connErr)
		}
	}

	t.Run("bidirectional streams", func(t *testing.T) {
		done := make(chan struct{})
		sess, closeServer := establishSession(t, func(sess *webtransport.Session) {
			<-done
			sess.CloseWithError(0, "")
		})
		defer closeServer()

		runTest(t,
			func() error { _, err := sess.OpenStream(); return err },
			func() error { _, err := sess.OpenStreamSync(context.Background()); return err },
			done,
		)
	})

	t.Run("unidirectional streams", func(t *testing.T) {
		done := make(chan struct{})
		sess, closeServer := establishSession(t, func(sess *webtransport.Session) {
			<-done
			sess.CloseWithError(0, "")
		})
		defer closeServer()

		runTest(t,
			func() error { _, err := sess.OpenUniStream(); return err },
			func() error { _, err := sess.OpenUniStreamSync(context.Background()); return err },
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
				RoundTripper: &http3.RoundTripper{
					TLSClientConfig: &tls.Config{RootCAs: certPool},
					QuicConfig:      &quic.Config{Tracer: getQlogger(t), EnableDatagrams: true},
				},
			}
			defer d.Close()
			url := fmt.Sprintf("https://localhost:%d/webtransport", addr.Port)
			hdr := make(http.Header)
			hdr.Add("Origin", strings.ReplaceAll(tc.Origin, "%port%", strconv.Itoa(addr.Port)))
			rsp, conn, err := d.Dial(context.Background(), url, hdr)
			if tc.Result {
				require.NoError(t, err)
				require.Equal(t, 200, rsp.StatusCode)
				defer conn.CloseWithError(0, "")
			} else {
				require.Equal(t, 404, rsp.StatusCode)
			}
		})
	}
}

func TestCloseStreamsOnSessionClose(t *testing.T) {
	accepted := make(chan struct{})
	sess, closeServer := establishSession(t, func(sess *webtransport.Session) {
		str, err := sess.OpenStream()
		require.NoError(t, err)
		_, err = str.Write([]byte("foobar"))
		require.NoError(t, err)
		ustr, err := sess.OpenUniStream()
		require.NoError(t, err)
		_, err = ustr.Write([]byte("foobar"))
		require.NoError(t, err)
		<-accepted
		sess.CloseWithError(0, "")
		_, err = str.Read([]byte{0})
		require.Error(t, err)
		_, err = ustr.Write([]byte{0})
		require.Error(t, err)
		_, err = ustr.Write([]byte{0})
		require.Error(t, err)
	})
	defer closeServer()

	str, err := sess.AcceptStream(context.Background())
	require.NoError(t, err)
	ustr, err := sess.AcceptUniStream(context.Background())
	require.NoError(t, err)
	close(accepted)
	str.Read(make([]byte, 6)) // read the foobar
	_, err = str.Read([]byte{0})
	require.Error(t, err)
	ustr.Read(make([]byte, 6)) // read the foobar
	_, err = ustr.Read([]byte{0})
	require.Error(t, err)
}

func TestWriteCloseRace(t *testing.T) {
	ch := make(chan struct{})
	sess, closeServer := establishSession(t, func(sess *webtransport.Session) {
		str, err := sess.AcceptStream(context.Background())
		if err != nil {
			return
		}
		defer str.Close()
		<-ch
	})
	defer closeServer()
	str, err := sess.OpenStream()
	require.NoError(t, err)
	ready := make(chan struct{}, 2)
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		ready <- struct{}{}
		wg.Wait()
		str.Write([]byte("foobar"))
		ready <- struct{}{}
	}()
	go func() {
		ready <- struct{}{}
		wg.Wait()
		str.Close()
		ready <- struct{}{}
	}()
	<-ready
	<-ready
	wg.Add(-2)
	<-ready
	<-ready
	close(ch)
}
