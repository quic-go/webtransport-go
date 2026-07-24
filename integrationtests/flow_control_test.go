package integrationtests

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
	"github.com/quic-go/webtransport-go/internal/testdata"
	"github.com/stretchr/testify/require"
)

func TestStreamFlowControl(t *testing.T) {
	for _, tc := range []struct {
		name           string
		clientOpens    bool
		unidirectional bool
	}{
		{name: "client/bidirectional", clientOpens: true},
		{name: "client/unidirectional", clientOpens: true, unidirectional: true},
		{name: "server/bidirectional"},
		{name: "server/unidirectional", unidirectional: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testStreamFlowControl(t, tc.clientOpens, tc.unidirectional)
		})
	}
}

func testStreamFlowControl(t *testing.T, clientOpens, unidirectional bool) {
	const streamLimit = 2

	config := &webtransport.Config{}
	if unidirectional {
		config.MaxIncomingUniStreams = streamLimit
	} else {
		config.MaxIncomingStreams = streamLimit
	}

	serverSessionChan := make(chan *webtransport.Session, 1)
	server := &webtransport.Server{
		H3:     &http3.Server{TLSConfig: testdata.TLSConf},
		Config: config,
	}
	addHandler(t, server, func(sess *webtransport.Session) { serverSessionChan <- sess })
	addr, closeServer := runServer(t, server)

	dialer := &webtransport.Dialer{
		TLSClientConfig: &tls.Config{RootCAs: testdata.CertPool},
		Config:          config,
	}
	defer func() {
		require.NoError(t, dialer.Close())
		closeServer()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), scaleDuration(5*time.Second))
	defer cancel()
	rsp, clientSession, err := dialer.Dial(ctx, fmt.Sprintf("https://localhost:%d/webtransport", addr.Port), nil)
	require.NoError(t, err)
	require.Equal(t, 200, rsp.StatusCode)

	var serverSession *webtransport.Session
	select {
	case serverSession = <-serverSessionChan:
	case <-ctx.Done():
		t.Fatal("server didn't establish the WebTransport session")
	}

	opener, receiver := serverSession, clientSession
	if clientOpens {
		opener, receiver = clientSession, serverSession
	}
	if unidirectional {
		testUnidirectionalStreamFlowControl(t, ctx, opener, receiver, streamLimit)
	} else {
		testBidirectionalStreamFlowControl(t, ctx, opener, receiver, streamLimit)
	}
}

func testBidirectionalStreamFlowControl(t *testing.T, ctx context.Context, opener, receiver *webtransport.Session, streamLimit int) {
	streams := make([]*webtransport.Stream, streamLimit)
	var err error
	for i := range streams {
		streams[i], err = opener.OpenStream()
		require.NoError(t, err)
	}
	_, err = opener.OpenStream()
	var limitErr *webtransport.StreamLimitReachedError
	require.ErrorAs(t, err, &limitErr)

	openResult := make(chan error, 1)
	go func() {
		_, err := opener.OpenStreamSync(ctx)
		openResult <- err
	}()
	requireStreamOpeningBlocked(t, openResult, "opening a stream unexpectedly completed")

	const payload = "flow control"
	_, err = io.WriteString(streams[0], payload)
	require.NoError(t, err)
	require.NoError(t, streams[0].Close())
	requireStreamOpeningBlocked(t, openResult, "opening a stream completed before the peer consumed it")

	str, err := receiver.AcceptStream(ctx)
	require.NoError(t, err)
	// reading the EOF releases and closing the stream releases the stream credit
	data, err := io.ReadAll(str)
	require.NoError(t, err)
	require.Equal(t, payload, string(data))
	requireStreamOpeningBlocked(t, openResult, "opening a stream completed before both sides were closed")
	require.NoError(t, str.Close())

	select {
	case err := <-openResult:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("opening a stream didn't unblock after the peer closed the previous stream")
	}
}

func testUnidirectionalStreamFlowControl(t *testing.T, ctx context.Context, opener, receiver *webtransport.Session, streamLimit int) {
	streams := make([]*webtransport.SendStream, streamLimit)
	var err error
	for i := range streams {
		streams[i], err = opener.OpenUniStream()
		require.NoError(t, err)
	}
	_, err = opener.OpenUniStream()
	var limitErr *webtransport.StreamLimitReachedError
	require.ErrorAs(t, err, &limitErr)

	openResult := make(chan error, 1)
	go func() {
		_, err := opener.OpenUniStreamSync(ctx)
		openResult <- err
	}()
	requireStreamOpeningBlocked(t, openResult, "opening a stream unexpectedly completed")

	_, err = io.WriteString(streams[0], "lorem ipsum dolor sit amet")
	require.NoError(t, err)
	require.NoError(t, streams[0].Close())
	requireStreamOpeningBlocked(t, openResult, "opening a stream completed before the peer consumed it")

	str, err := receiver.AcceptUniStream(ctx)
	require.NoError(t, err)
	// reading the EOF releases the stream credit
	data, err := io.ReadAll(str)
	require.NoError(t, err)
	require.Equal(t, "lorem ipsum dolor sit amet", string(data))

	select {
	case err := <-openResult:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("opening a stream didn't unblock after the peer closed the previous stream")
	}
}

func requireStreamOpeningBlocked(t *testing.T, result <-chan error, message string) {
	t.Helper()

	select {
	case err := <-result:
		t.Fatalf("%s: %v", message, err)
	case <-time.After(scaleDuration(20 * time.Millisecond)):
	}
}
