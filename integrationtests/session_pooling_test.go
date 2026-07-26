package integrationtests

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/quic-go/webtransport-go"
	"github.com/quic-go/webtransport-go/internal/testdata"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	"github.com/stretchr/testify/require"
)

func TestSessionPooling(t *testing.T) {
	const numSessions = 3

	config := &webtransport.Config{
		MaxIncomingStreams: 2,
		MaxIncomingData:    1 << 20,
	}
	acceptedSessions := make(chan *webtransport.Session, numSessions)
	server := &webtransport.Server{
		H3:     &http3.Server{TLSConfig: testdata.TLSConf},
		Config: config,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/webtransport", func(w http.ResponseWriter, r *http.Request) {
		sess, err := server.Upgrade(w, r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		acceptedSessions <- sess
	})
	mux.HandleFunc("/hello", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "Hello, world!")
	})
	server.H3.Handler = mux
	addr, closeServer := runServer(t, server)
	defer closeServer()

	ctx, cancel := context.WithTimeout(t.Context(), scaleDuration(5*time.Second))
	defer cancel()
	qconn, err := quic.DialAddr(
		ctx,
		fmt.Sprintf("localhost:%d", addr.Port),
		&tls.Config{RootCAs: testdata.CertPool, NextProtos: []string{http3.NextProtoH3}},
		&quic.Config{EnableDatagrams: true, EnableStreamResetPartialDelivery: true},
	)
	require.NoError(t, err)
	defer qconn.CloseWithError(0, "")

	tr := &webtransport.Transport{Config: config}
	defer tr.Close()
	conn, err := tr.NewClientConn(qconn)
	require.NoError(t, err)
	url := fmt.Sprintf("https://localhost:%d", addr.Port)

	clientSessions := make([]*webtransport.Session, numSessions)
	serverSessions := make([]*webtransport.Session, numSessions)
	for i := range numSessions {
		rsp, sess, err := conn.Dial(ctx, url+"/webtransport", nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, rsp.StatusCode)
		defer sess.CloseWithError(0, "")
		clientSessions[i] = sess
		serverSessions[i] = <-acceptedSessions
	}

	exchange := func(sender, receiver *webtransport.Session, payload string) {
		t.Helper()

		sendStr, err := sender.OpenStream()
		require.NoError(t, err)
		_, err = io.WriteString(sendStr, payload)
		require.NoError(t, err)
		require.NoError(t, sendStr.Close())

		receiveStr, err := receiver.AcceptStream(ctx)
		require.NoError(t, err)
		data, err := io.ReadAll(receiveStr)
		require.NoError(t, err)
		require.Equal(t, payload, string(data))
		_, err = io.WriteString(receiveStr, "echo: "+payload)
		require.NoError(t, err)
		require.NoError(t, receiveStr.Close())

		data, err = io.ReadAll(sendStr)
		require.NoError(t, err)
		require.Equal(t, "echo: "+payload, string(data))
	}

	for i := range numSessions {
		exchange(clientSessions[i], serverSessions[i], fmt.Sprintf("client session %d", i))
		exchange(serverSessions[i], clientSessions[i], fmt.Sprintf("server session %d", i))
	}

	// the connection can also be used for regular HTTP requests
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/hello", nil)
	require.NoError(t, err)
	rsp, err := conn.RoundTrip(req)
	require.NoError(t, err)
	body, err := io.ReadAll(rsp.Body)
	require.NoError(t, err)
	require.Equal(t, "Hello, world!", string(body))
	require.NoError(t, rsp.Body.Close())

	require.NoError(t, clientSessions[1].CloseWithError(0, ""))
	select {
	case <-serverSessions[1].Context().Done():
	case <-ctx.Done():
		t.Fatal("server didn't close the middle session")
	}
	exchange(clientSessions[0], serverSessions[0], "first session still works")
	exchange(clientSessions[2], serverSessions[2], "third session still works")
}
