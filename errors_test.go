package webtransport

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/quic-go/quic-go"

	"github.com/stretchr/testify/require"
)

func TestErrorCodeRoundTrip(t *testing.T) {
	for i := 0; i < 1e4; i++ {
		n := StreamErrorCode(rand.Int64())
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
			c := firstErrorCode + uint64(rand.Uint32())
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

func TestStreamError(t *testing.T) {
	require.ErrorIs(t, &StreamError{ErrorCode: 2, Remote: true}, &StreamError{ErrorCode: 2, Remote: true})
	require.NotErrorIs(t, &StreamError{ErrorCode: 2, Remote: true}, &StreamError{ErrorCode: 2, Remote: false})
	require.NotErrorIs(t, &StreamError{ErrorCode: 1}, &StreamError{ErrorCode: 2})
	require.Equal(t, "stream canceled with error code 2", (&StreamError{ErrorCode: 2, Remote: true}).Error())
	require.Equal(t, "stream canceled with error code 1337", (&StreamError{ErrorCode: 1337, Remote: false}).Error())
}

func TestSessionError(t *testing.T) {
	require.ErrorIs(t, &SessionError{ErrorCode: 2, Remote: true}, &SessionError{ErrorCode: 2, Remote: true})
	require.ErrorIs(t,
		&SessionError{ErrorCode: 2, Remote: true, Message: "foo"},
		&SessionError{ErrorCode: 2, Remote: true, Message: "bar"},
	)
	require.NotErrorIs(t, &SessionError{ErrorCode: 2, Remote: true}, &SessionError{ErrorCode: 2, Remote: false})
	require.NotErrorIs(t, &SessionError{ErrorCode: 1}, &SessionError{ErrorCode: 2})
	require.Equal(t, "foo", (&SessionError{ErrorCode: 2, Remote: true, Message: "foo"}).Error())
	require.Equal(t, "bar", (&SessionError{ErrorCode: 1337, Remote: false, Message: "bar"}).Error())
}
