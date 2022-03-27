package webtransport

import (
	"github.com/lucas-clemente/quic-go"
	"github.com/stretchr/testify/require"
	"math"
	"testing"
)

func TestErrorCodeRoundTrip(t *testing.T) {
	for i := 0; i < math.MaxUint8; i++ {
		httpCode := webtransportCodeToHTTPCode(ErrorCode(i))
		errorCode, err := httpCodeToWebtransportCode(httpCode)
		require.NoError(t, err)
		require.Equal(t, ErrorCode(i), errorCode)
	}
}

func TestErrorCodeConversionErrors(t *testing.T) {
	t.Run("too small", func(t *testing.T) {
		_, err := httpCodeToWebtransportCode(firstErrorCode - 1)
		require.EqualError(t, err, "error code outside of expected range")
	})

	t.Run("too large", func(t *testing.T) {
		_, err := httpCodeToWebtransportCode(lastErrorCode + 1)
		require.EqualError(t, err, "error code outside of expected range")
	})

	t.Run("greased value", func(t *testing.T) {
		invalids := []quic.StreamErrorCode{0x52e4a40fa8f9, 0x52e4a40fa918, 0x52e4a40fa937, 0x52e4a40fa956, 0x52e4a40fa975, 0x52e4a40fa994, 0x52e4a40fa9b3, 0x52e4a40fa9d2}
		for _, c := range invalids {
			_, err := httpCodeToWebtransportCode(c)
			require.EqualError(t, err, "invalid error code")
		}
	})
}
