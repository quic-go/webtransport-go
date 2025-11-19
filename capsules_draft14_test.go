package webtransport

import (
	"testing"

	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/quicvarint"
	"github.com/stretchr/testify/require"
)

func TestEncodeDecodeMaxData(t *testing.T) {
	testCases := []struct {
		name    string
		maxData uint64
	}{
		{"zero", 0},
		{"small value", 100},
		{"1 MB", 1_000_000},
		{"10 MB", 10_000_000},
		{"1 GB", 1_000_000_000},
		{"max varint", 1<<62 - 1},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Encode
			encoded := EncodeMaxData(tc.maxData)
			require.NotEmpty(t, encoded)

			// Decode
			decoded, err := DecodeMaxData(encoded)
			require.NoError(t, err)
			require.Equal(t, tc.maxData, decoded)
		})
	}
}

func TestDecodeMaxDataErrors(t *testing.T) {
	t.Run("empty payload", func(t *testing.T) {
		_, err := DecodeMaxData([]byte{})
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to decode maxData")
	})

	t.Run("trailing bytes", func(t *testing.T) {
		payload := quicvarint.Append(nil, 1000)
		payload = append(payload, 0xFF) // extra byte
		_, err := DecodeMaxData(payload)
		require.Error(t, err)
		require.Contains(t, err.Error(), "unexpected trailing bytes")
	})

	t.Run("truncated varint", func(t *testing.T) {
		payload := []byte{0xFF} // incomplete varint
		_, err := DecodeMaxData(payload)
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to decode maxData")
	})
}

func TestEncodeDecodeMaxStreams(t *testing.T) {
	testCases := []struct {
		name       string
		streamType StreamType
		maxStreams uint64
		shouldFail bool
	}{
		{"bidi zero", StreamTypeBidi, 0, false},
		{"uni zero", StreamTypeUni, 0, false},
		{"bidi small value", StreamTypeBidi, 10, false},
		{"uni small value", StreamTypeUni, 10, false},
		{"bidi medium value", StreamTypeBidi, 1000, false},
		{"uni large value", StreamTypeUni, 1_000_000, false},
		{"bidi at 2^60 limit", StreamTypeBidi, 1 << 60, false},
		{"uni exceeds 2^60 limit", StreamTypeUni, (1 << 60) + 1, true},
		{"bidi max uint64", StreamTypeBidi, ^uint64(0), true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Encode
			encoded, err := EncodeMaxStreams(tc.streamType, tc.maxStreams)
			if tc.shouldFail {
				require.Error(t, err)
				require.Contains(t, err.Error(), "exceeds 2^60 limit")
				return
			}
			require.NoError(t, err)
			require.NotEmpty(t, encoded)

			// Decode - determine capsule type based on stream type
			capsuleType := maxStreamsBidiCapsuleType
			if tc.streamType == StreamTypeUni {
				capsuleType = maxStreamsUniCapsuleType
			}

			streamType, decoded, err := DecodeMaxStreams(capsuleType, encoded)
			require.NoError(t, err)
			require.Equal(t, tc.streamType, streamType)
			require.Equal(t, tc.maxStreams, decoded)
		})
	}
}

func TestDecodeMaxStreamsErrors(t *testing.T) {
	t.Run("empty payload", func(t *testing.T) {
		_, _, err := DecodeMaxStreams(maxStreamsBidiCapsuleType, []byte{})
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to decode maxStreams")
	})

	t.Run("trailing bytes", func(t *testing.T) {
		payload := quicvarint.Append(nil, 100)
		payload = append(payload, 0xAA, 0xBB) // extra bytes
		_, _, err := DecodeMaxStreams(maxStreamsUniCapsuleType, payload)
		require.Error(t, err)
		require.Contains(t, err.Error(), "unexpected trailing bytes")
	})

	t.Run("invalid capsule type", func(t *testing.T) {
		payload := quicvarint.Append(nil, 100)
		_, _, err := DecodeMaxStreams(http3.CapsuleType(0x9999), payload)
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid capsule type")
	})

	t.Run("truncated varint", func(t *testing.T) {
		payload := []byte{0xFF, 0xFF} // incomplete varint
		_, _, err := DecodeMaxStreams(maxStreamsBidiCapsuleType, payload)
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to decode maxStreams")
	})
}

func TestEncodeDecodeStreamsBlocked(t *testing.T) {
	testCases := []struct {
		name       string
		streamType StreamType
		limit      uint64
	}{
		{"bidi zero", StreamTypeBidi, 0},
		{"uni zero", StreamTypeUni, 0},
		{"bidi small limit", StreamTypeBidi, 5},
		{"uni small limit", StreamTypeUni, 5},
		{"bidi medium limit", StreamTypeBidi, 500},
		{"uni large limit", StreamTypeUni, 1_000_000},
		{"bidi very large limit", StreamTypeBidi, 1 << 50},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Encode
			encoded, err := EncodeStreamsBlocked(tc.streamType, tc.limit)
			require.NoError(t, err)
			require.NotEmpty(t, encoded)

			// Decode - determine capsule type based on stream type
			capsuleType := streamsBlockedBidiCapsuleType
			if tc.streamType == StreamTypeUni {
				capsuleType = streamsBlockedUniCapsuleType
			}

			streamType, decoded, err := DecodeStreamsBlocked(capsuleType, encoded)
			require.NoError(t, err)
			require.Equal(t, tc.streamType, streamType)
			require.Equal(t, tc.limit, decoded)
		})
	}
}

func TestDecodeStreamsBlockedErrors(t *testing.T) {
	t.Run("empty payload", func(t *testing.T) {
		_, _, err := DecodeStreamsBlocked(streamsBlockedBidiCapsuleType, []byte{})
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to decode streams blocked limit")
	})

	t.Run("trailing bytes", func(t *testing.T) {
		payload := quicvarint.Append(nil, 42)
		payload = append(payload, 0x00) // extra byte
		_, _, err := DecodeStreamsBlocked(streamsBlockedUniCapsuleType, payload)
		require.Error(t, err)
		require.Contains(t, err.Error(), "unexpected trailing bytes")
	})

	t.Run("invalid capsule type", func(t *testing.T) {
		payload := quicvarint.Append(nil, 42)
		_, _, err := DecodeStreamsBlocked(http3.CapsuleType(0x1234), payload)
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid capsule type")
	})

	t.Run("truncated varint", func(t *testing.T) {
		payload := []byte{0x80} // incomplete 2-byte varint
		_, _, err := DecodeStreamsBlocked(streamsBlockedBidiCapsuleType, payload)
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to decode streams blocked limit")
	})
}

func TestEncodeDecodeDataBlocked(t *testing.T) {
	testCases := []struct {
		name  string
		limit uint64
	}{
		{"zero", 0},
		{"1 KB", 1024},
		{"1 MB", 1_048_576},
		{"100 MB", 100_000_000},
		{"1 GB", 1_073_741_824},
		{"max varint", 1<<62 - 1},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Encode
			encoded := EncodeDataBlocked(tc.limit)
			require.NotEmpty(t, encoded)

			// Decode
			decoded, err := DecodeDataBlocked(encoded)
			require.NoError(t, err)
			require.Equal(t, tc.limit, decoded)
		})
	}
}

func TestDecodeDataBlockedErrors(t *testing.T) {
	t.Run("empty payload", func(t *testing.T) {
		_, err := DecodeDataBlocked([]byte{})
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to decode data blocked limit")
	})

	t.Run("trailing bytes", func(t *testing.T) {
		payload := quicvarint.Append(nil, 12345)
		payload = append(payload, 0x01, 0x02, 0x03) // extra bytes
		_, err := DecodeDataBlocked(payload)
		require.Error(t, err)
		require.Contains(t, err.Error(), "unexpected trailing bytes")
	})

	t.Run("truncated varint", func(t *testing.T) {
		payload := []byte{0xC0, 0x00} // incomplete 4-byte varint
		_, err := DecodeDataBlocked(payload)
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to decode data blocked limit")
	})
}

// TestVarintEncodingSizes verifies that varints are encoded efficiently.
func TestVarintEncodingSizes(t *testing.T) {
	testCases := []struct {
		value        uint64
		expectedSize int
	}{
		{0, 1},
		{63, 1},
		{64, 2},
		{16383, 2},
		{16384, 4},
		{1073741823, 4},
		{1073741824, 8},
	}

	for _, tc := range testCases {
		t.Run("", func(t *testing.T) {
			encoded := EncodeMaxData(tc.value)
			require.Len(t, encoded, tc.expectedSize)
		})
	}
}

// TestRoundTripWithAllFunctions tests encode/decode round trips for all capsule types.
func TestRoundTripWithAllFunctions(t *testing.T) {
	values := []uint64{0, 1, 100, 1000, 10000, 1 << 20, 1 << 40, 1 << 60}

	for _, val := range values {
		t.Run("MaxData", func(t *testing.T) {
			decoded, err := DecodeMaxData(EncodeMaxData(val))
			require.NoError(t, err)
			require.Equal(t, val, decoded)
		})

		t.Run("DataBlocked", func(t *testing.T) {
			decoded, err := DecodeDataBlocked(EncodeDataBlocked(val))
			require.NoError(t, err)
			require.Equal(t, val, decoded)
		})

		// MaxStreams and StreamsBlocked with bidi/uni
		for _, streamType := range []StreamType{StreamTypeBidi, StreamTypeUni} {
			t.Run("MaxStreams", func(t *testing.T) {
				encoded, err := EncodeMaxStreams(streamType, val)
				require.NoError(t, err)

				capsuleType := maxStreamsBidiCapsuleType
				if streamType == StreamTypeUni {
					capsuleType = maxStreamsUniCapsuleType
				}

				decodedStreamType, decoded, err := DecodeMaxStreams(capsuleType, encoded)
				require.NoError(t, err)
				require.Equal(t, streamType, decodedStreamType)
				require.Equal(t, val, decoded)
			})

			t.Run("StreamsBlocked", func(t *testing.T) {
				encoded, err := EncodeStreamsBlocked(streamType, val)
				require.NoError(t, err)

				capsuleType := streamsBlockedBidiCapsuleType
				if streamType == StreamTypeUni {
					capsuleType = streamsBlockedUniCapsuleType
				}

				decodedStreamType, decoded, err := DecodeStreamsBlocked(capsuleType, encoded)
				require.NoError(t, err)
				require.Equal(t, streamType, decodedStreamType)
				require.Equal(t, val, decoded)
			})
		}
	}
}
