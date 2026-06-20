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
