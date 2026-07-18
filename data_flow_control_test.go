package webtransport

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/quicvarint"

	"github.com/stretchr/testify/require"
)

func TestIncomingStreamHeaderLength(t *testing.T) {
	clientConn, serverConn := newConnPair(t, newUDPConnLocalhost(t), newUDPConnLocalhost(t))
	serverSession := make(chan *Session, 1)
	server := &Server{H3: &http3.Server{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/webtransport", func(w http.ResponseWriter, r *http.Request) {
		sess, err := server.Upgrade(w, r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		serverSession <- sess
	})
	server.H3.Handler = mux
	t.Cleanup(func() { server.Close() })
	go server.ServeQUICConn(serverConn)

	additionalSettings := map[uint64]uint64{settingsWebTransportEnabled: 1}
	tr := &http3.Transport{EnableDatagrams: true, AdditionalSettings: additionalSettings}
	cc := tr.NewClientConn(clientConn)
	ctx, cancel := context.WithTimeout(context.Background(), scaleDuration(5*time.Second))
	defer cancel()
	select {
	case <-cc.ReceivedSettings():
	case <-ctx.Done():
		t.Fatal("client didn't receive the server settings")
	}

	reqStr, err := cc.OpenRequestStream(ctx)
	require.NoError(t, err)
	require.NoError(t, reqStr.SendRequestHeader(NewWebTransportRequest(t, "https://localhost/webtransport")))
	rsp, err := reqStr.ReadResponse()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rsp.StatusCode)
	clientSession := newSession(
		context.Background(),
		sessionID(reqStr.StreamID()),
		clientConn,
		reqStr,
		"",
		sessionFlowControl{},
	)
	defer clientSession.CloseWithError(0, "")
	var peerSession *Session
	select {
	case peerSession = <-serverSession:
	case <-ctx.Done():
		t.Fatal("server didn't establish the WebTransport session")
	}

	rawStream, err := clientConn.OpenUniStream()
	require.NoError(t, err)
	header := quicvarint.AppendWithLen(nil, webTransportUniStreamType, 8)
	header = quicvarint.AppendWithLen(header, uint64(peerSession.sessionID), 8)
	_, err = rawStream.Write(header)
	require.NoError(t, err)
	require.NoError(t, rawStream.Close())
	str, err := peerSession.AcceptUniStream(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(len(header)), str.bytesRead)
	_, err = io.ReadAll(str)
	require.NoError(t, err)
}
