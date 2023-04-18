package webtransport

import (
	"errors"
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/stretchr/testify/require"
)

var random = rand.New(rand.NewSource(time.Now().UnixNano()))

func TestErrorCodeRoundTrip(t *testing.T) {
	for i := 0; i < 1e4; i++ {
		n := StreamErrorCode(random.Int63())
		httpCode := webtransportCodeToHTTPCode(n)
		errorCode, err := httpCodeToWebtransportCode(httpCode)
		require.NoError(t, err)
		require.Equal(t, n, errorCode)
	}
}

func TestErrorCodeConversionErrors(t *testing.T) {
	t.Run("too small", func(t *testing.T) {
		first, err := httpCodeToWebtransportCode(firstErrorCode)
		require.NoError(t, err)
		require.Zero(t, first)
		_, err = httpCodeToWebtransportCode(firstErrorCode - 1)
		require.EqualError(t, err, "error code outside of expected range")
	})

	t.Run("too large", func(t *testing.T) {
		last, err := httpCodeToWebtransportCode(lastErrorCode)
		require.NoError(t, err)
		require.Equal(t, StreamErrorCode(math.MaxUint32), last)
		_, err = httpCodeToWebtransportCode(lastErrorCode + 1)
		require.EqualError(t, err, "error code outside of expected range")
	})

	t.Run("greased value", func(t *testing.T) {
		var counter int
		for i := 0; i < 1e4; i++ {
			c := firstErrorCode + uint64(uint32(random.Int63()))
			if (c-0x21)%0x1f != 0 {
				continue
			}
			counter++
			_, err := httpCodeToWebtransportCode(quic.StreamErrorCode(c))
			require.EqualError(t, err, "invalid error code")
		}
		t.Logf("checked %d greased values", counter)
		require.NotZero(t, counter)
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
