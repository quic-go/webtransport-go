package webtransport

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpgradeFailures(t *testing.T) {
	var s Server

	t.Run("wrong request method", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/webtransport", nil)
		_, err := s.Upgrade(httptest.NewRecorder(), req)
		require.EqualError(t, err, "expected CONNECT request, got GET")
	})

	t.Run("wrong protocol", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodConnect, "/webtransport", nil)
		_, err := s.Upgrade(httptest.NewRecorder(), req)
		require.EqualError(t, err, "unexpected protocol: HTTP/1.1")
	})

	t.Run("missing WebTransport header", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodConnect, "/webtransport", nil)
		req.Proto = protocolHeader
		_, err := s.Upgrade(httptest.NewRecorder(), req)
		require.EqualError(t, err, "missing or invalid Sec-Webtransport-Http3-Draft02 header")
	})
}
