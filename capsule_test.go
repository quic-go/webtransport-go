package webtransport

import (
	"bytes"
	"encoding/binary"
	"io"
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

	c, err := parseNextCapsule(&b)
	require.NoError(t, err)
	closeCapsule, ok := c.(closeSessionCapsule)
	require.True(t, ok)
	require.Equal(t, SessionErrorCode(1337), closeCapsule.ErrorCode)
	require.Len(t, closeCapsule.Message, maxCloseCapsuleErrorMsgLen)
	require.Equal(t, strings.Repeat("b", maxCloseCapsuleErrorMsgLen), closeCapsule.Message)
}

func TestAppendCloseSessionCapsuleMessageTruncation(t *testing.T) {
	var b bytes.Buffer
	b.Write((closeSessionCapsule{ErrorCode: 42, Message: strings.Repeat("a", maxCloseCapsuleErrorMsgLen+500)}).Append(nil))

	typ, r, err := http3.ParseCapsule(quicvarint.NewReader(&b))
	require.NoError(t, err)
	require.Equal(t, closeSessionCapsuleType, typ)

	payload, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Len(t, payload, 4+maxCloseCapsuleErrorMsgLen)
	require.Equal(t, uint32(42), binary.BigEndian.Uint32(payload[:4]))
	require.Equal(t, strings.Repeat("a", maxCloseCapsuleErrorMsgLen), string(payload[4:]))
}

func TestCloseSessionCapsuleRoundTrip(t *testing.T) {
	var b bytes.Buffer
	b.Write((closeSessionCapsule{ErrorCode: 42, Message: "all good"}).Append(nil))

	c, err := parseNextCapsule(&b)
	require.NoError(t, err)
	require.Equal(t, closeSessionCapsule{ErrorCode: 42, Message: "all good"}, c)
}

func TestMaxStreamsCapsuleRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    capsule
		typ  http3.CapsuleType
		max  uint64
	}{
		{
			name: "bidirectional",
			c:    maxStreamsBidiCapsule{MaximumStreams: 42},
			typ:  maxStreamsBidiCapsuleType,
			max:  42,
		},
		{
			name: "unidirectional",
			c:    maxStreamsUniCapsule{MaximumStreams: 1337},
			typ:  maxStreamsUniCapsuleType,
			max:  1337,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			b.Write(tc.c.Append(nil))

			typ, r, err := http3.ParseCapsule(quicvarint.NewReader(&b))
			require.NoError(t, err)
			require.Equal(t, tc.typ, typ)
			maxStreams, err := quicvarint.Read(quicvarint.NewReader(r))
			require.NoError(t, err)
			require.Equal(t, tc.max, maxStreams)

			c, err := parseNextCapsule(bytes.NewReader(tc.c.Append(nil)))
			require.NoError(t, err)
			require.Equal(t, tc.c, c)
		})
	}
}

func TestStreamsBlockedCapsuleRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    capsule
		typ  http3.CapsuleType
		max  uint64
	}{
		{
			name: "bidirectional",
			c:    streamsBlockedBidiCapsule{MaximumStreams: 42},
			typ:  streamsBlockedBidiCapsuleType,
			max:  42,
		},
		{
			name: "unidirectional",
			c:    streamsBlockedUniCapsule{MaximumStreams: 1337},
			typ:  streamsBlockedUniCapsuleType,
			max:  1337,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			b.Write(tc.c.Append(nil))

			typ, r, err := http3.ParseCapsule(quicvarint.NewReader(&b))
			require.NoError(t, err)
			require.Equal(t, tc.typ, typ)
			maxStreams, err := quicvarint.Read(quicvarint.NewReader(r))
			require.NoError(t, err)
			require.Equal(t, tc.max, maxStreams)

			c, err := parseNextCapsule(bytes.NewReader(tc.c.Append(nil)))
			require.NoError(t, err)
			require.Equal(t, tc.c, c)
		})
	}
}

func TestParseMaxStreamsCapsuleTooLarge(t *testing.T) {
	var b bytes.Buffer
	b.Write((maxStreamsBidiCapsule{MaximumStreams: maxStreamsLimit + 1}).Append(nil))

	_, err := parseNextCapsule(&b)
	require.ErrorContains(t, err, "value too large")
}

func TestParseStreamsBlockedCapsuleTooLarge(t *testing.T) {
	var b bytes.Buffer
	b.Write((streamsBlockedBidiCapsule{MaximumStreams: maxStreamsLimit + 1}).Append(nil))

	_, err := parseNextCapsule(&b)
	require.ErrorContains(t, err, "WT_STREAMS_BLOCKED value too large")
}

func TestParseMaxStreamsCapsuleTrailingData(t *testing.T) {
	b := quicvarint.Append(nil, uint64(maxStreamsBidiCapsuleType))
	b = quicvarint.Append(b, uint64(quicvarint.Len(42)+1))
	b = quicvarint.Append(b, 42)
	b = append(b, 0)

	_, err := parseNextCapsule(bytes.NewReader(b))
	require.ErrorContains(t, err, "trailing data")
}

func TestParseStreamsBlockedCapsuleTrailingData(t *testing.T) {
	b := quicvarint.Append(nil, uint64(streamsBlockedUniCapsuleType))
	b = quicvarint.Append(b, uint64(quicvarint.Len(42)+1))
	b = quicvarint.Append(b, 42)
	b = append(b, 0)

	_, err := parseNextCapsule(bytes.NewReader(b))
	require.ErrorContains(t, err, "WT_STREAMS_BLOCKED capsule has trailing data")
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
