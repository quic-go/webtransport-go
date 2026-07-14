package webtransport

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"
)

func newUniStreamPair(t *testing.T) (*quic.SendStream, *quic.ReceiveStream) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	client, server := newConnPair(t, newUDPConnLocalhost(t), newUDPConnLocalhost(t))
	t.Cleanup(func() { client.CloseWithError(0, "") })

	clientStr, err := client.OpenUniStreamSync(ctx)
	require.NoError(t, err)
	clientStr.Write([]byte("foo"))

	serverStr, err := server.AcceptUniStream(ctx)
	require.NoError(t, err)
	_, err = io.ReadFull(serverStr, make([]byte, 3))
	require.NoError(t, err)

	return clientStr, serverStr
}
