package webtransport

import (
	"errors"
	"math"
	"testing"

	"github.com/lucas-clemente/quic-go"
	"github.com/stretchr/testify/require"
)

func TestErrorCodeRoundTrip(t *testing.T) {
	for i := 0; i < math.MaxUint8; i++ {
		httpCode := webtransportCodeToHTTPCode(StreamErrorCode(i))
		errorCode, err := httpCodeToWebtransportCode(httpCode)
		require.NoError(t, err)
		require.Equal(t, StreamErrorCode(i), errorCode)
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

func TestErrorDetection(t *testing.T) {
	is := []error{
		&quic.StreamError{ErrorCode: webtransportCodeToHTTPCode(42)},
		&quic.StreamError{ErrorCode: sessionCloseErrorCode},
	}
	for _, i := range is {
		require.True(t, isWebTransportError(i))
	}

	isNot := []error{
		errors.New("foo"),
		&quic.StreamError{ErrorCode: sessionCloseErrorCode + 1},
	}
	for _, i := range isNot {
		require.False(t, isWebTransportError(i))
	}
}
