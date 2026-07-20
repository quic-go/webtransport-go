package webtransport

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/quic-go/webtransport-go/internal/testdata"
	"github.com/stretchr/testify/require"
)

var (
	TLSConf  = testdata.TLSConf
	CertPool = testdata.CertPool
)

func NewWebTransportRequest(t *testing.T, addr string) *http.Request {
	t.Helper()

	u, err := url.Parse(addr)
	require.NoError(t, err)
	hdr := make(http.Header)
	hdr.Add("Sec-Webtransport-Http3-Draft02", "1")
	return &http.Request{
		Method: http.MethodConnect,
		Header: hdr,
		Proto:  protocolHeader,
		Host:   u.Host,
		URL:    u,
	}
}
