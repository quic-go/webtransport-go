package webtransport

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/quicvarint"

	"github.com/stretchr/testify/require"
)

func TestParseCloseSessionCapsuleMessageTruncation(t *testing.T) {
	longMsg := strings.Repeat("b", maxCloseCapsuleErrorMsgLen+500)
	payload := make([]byte, 4+len(longMsg))
	binary.BigEndian.PutUint32(payload[:4], uint32(1337))
	copy(payload[4:], longMsg)

	var b bytes.Buffer
	require.NoError(t, http3.WriteCapsule(quicvarint.NewWriter(&b), closeSessionCapsuleType, payload))

	err := parseNextCapsule(&b)
	var sessErr *SessionError
	require.ErrorAs(t, err, &sessErr)
	require.True(t, sessErr.Remote)
	require.Equal(t, SessionErrorCode(1337), sessErr.ErrorCode)
	require.Len(t, sessErr.Message, maxCloseCapsuleErrorMsgLen)
	require.Equal(t, strings.Repeat("b", maxCloseCapsuleErrorMsgLen), sessErr.Message)
}

func TestAppendCloseSessionCapsulePayloadMessageTruncation(t *testing.T) {
	payload := appendCloseSessionCapsulePayload(nil, 42, strings.Repeat("a", maxCloseCapsuleErrorMsgLen+500))

	require.Len(t, payload, 4+maxCloseCapsuleErrorMsgLen)
	require.Equal(t, uint32(42), binary.BigEndian.Uint32(payload[:4]))
	require.Equal(t, strings.Repeat("a", maxCloseCapsuleErrorMsgLen), string(payload[4:]))
}

func TestCloseSessionCapsuleRoundTrip(t *testing.T) {
	var b bytes.Buffer
	payload := appendCloseSessionCapsulePayload(nil, 42, "all good")
	require.NoError(t, http3.WriteCapsule(quicvarint.NewWriter(&b), closeSessionCapsuleType, payload))

	err := parseNextCapsule(&b)
	var sessErr *SessionError
	require.ErrorAs(t, err, &sessErr)
	require.True(t, sessErr.Remote)
	require.Equal(t, SessionErrorCode(42), sessErr.ErrorCode)
	require.Equal(t, "all good", sessErr.Message)
}

func TestTruncateUTF8(t *testing.T) {
	input := "Go 🚀"
	require.Len(t, input, 7)

	require.Equal(t, "Go 🚀", truncateUTF8(input, 100))
	require.Equal(t, "Go 🚀", truncateUTF8(input, 7))
	require.Equal(t, "Go ", truncateUTF8(input, 6))
	require.Equal(t, "Go ", truncateUTF8(input, 5))
	require.Equal(t, "Go ", truncateUTF8(input, 4))
	require.Equal(t, "Go ", truncateUTF8(input, 3))
	require.Equal(t, "Go", truncateUTF8(input, 2))
	require.Equal(t, "G", truncateUTF8(input, 1))
	require.Equal(t, "", truncateUTF8(input, 0))
}
