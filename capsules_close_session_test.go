package webtransport

import (
	"encoding/binary"
	"testing"

	"github.com/quic-go/quic-go/http3"
	"github.com/stretchr/testify/require"
)

func TestEncodeCloseSession(t *testing.T) {
	testCases := []struct {
		name        string
		errorCode   uint32
		message     string
		expectError bool
	}{
		{"zero error code empty message", 0, "", false},
		{"zero error code with message", 0, "clean shutdown", false},
		{"non-zero error code", 42, "application error", false},
		{"max uint32 error code", 0xFFFFFFFF, "maximum error code", false},
		{"UTF-8 characters", 1, "Error: 日本語 🎉", false},
		{"max length message", 0, string(make([]byte, 1024)), false},
		{"message too long", 0, string(make([]byte, 1025)), true},
		{"invalid UTF-8", 0, string([]byte{0xFF, 0xFE, 0xFD}), true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := EncodeCloseSession(tc.errorCode, tc.message)

			if tc.expectError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.NotEmpty(t, payload)
			require.GreaterOrEqual(t, len(payload), 4)

			// Verify error code encoding
			decodedErrorCode := binary.BigEndian.Uint32(payload[:4])
			require.Equal(t, tc.errorCode, decodedErrorCode)

			// Verify message encoding
			decodedMessage := string(payload[4:])
			require.Equal(t, tc.message, decodedMessage)
		})
	}
}

func TestDecodeCloseSession(t *testing.T) {
	testCases := []struct {
		name              string
		payload           []byte
		expectedErrorCode uint32
		expectedMessage   string
		expectError       bool
	}{
		{
			name:              "empty message",
			payload:           []byte{0x00, 0x00, 0x00, 0x00},
			expectedErrorCode: 0,
			expectedMessage:   "",
			expectError:       false,
		},
		{
			name:              "simple message",
			payload:           []byte{0x00, 0x00, 0x00, 0x2A, 'h', 'e', 'l', 'l', 'o'},
			expectedErrorCode: 42,
			expectedMessage:   "hello",
			expectError:       false,
		},
		{
			name:              "max error code",
			payload:           []byte{0xFF, 0xFF, 0xFF, 0xFF, 't', 'e', 's', 't'},
			expectedErrorCode: 0xFFFFFFFF,
			expectedMessage:   "test",
			expectError:       false,
		},
		{
			name:              "UTF-8 characters",
			payload:           append([]byte{0x00, 0x00, 0x00, 0x01}, []byte("Error: 日本語 🎉")...),
			expectedErrorCode: 1,
			expectedMessage:   "Error: 日本語 🎉",
			expectError:       false,
		},
		{
			name:              "max length message",
			payload:           append([]byte{0x00, 0x00, 0x00, 0x00}, make([]byte, 1024)...),
			expectedErrorCode: 0,
			expectedMessage:   string(make([]byte, 1024)),
			expectError:       false,
		},
		{
			name:              "payload too short",
			payload:           []byte{0x00, 0x00},
			expectedErrorCode: 0,
			expectedMessage:   "",
			expectError:       true,
		},
		{
			name:              "message too long",
			payload:           append([]byte{0x00, 0x00, 0x00, 0x00}, make([]byte, 1025)...),
			expectedErrorCode: 0,
			expectedMessage:   "",
			expectError:       true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			errorCode, message, err := DecodeCloseSession(tc.payload)

			if tc.expectError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.expectedErrorCode, errorCode)
			require.Equal(t, tc.expectedMessage, message)
		})
	}
}

func TestEncodeDecodeCloseSessionRoundTrip(t *testing.T) {
	testCases := []struct {
		errorCode uint32
		message   string
	}{
		{0, ""},
		{0, "clean shutdown"},
		{1, "application error"},
		{42, "test error"},
		{0xFFFFFFFF, "max error code"},
		{100, "Error with UTF-8: 日本語 🎉"},
		{0, string(make([]byte, 1024))},
	}

	for _, tc := range testCases {
		t.Run("", func(t *testing.T) {
			// Encode
			payload, err := EncodeCloseSession(tc.errorCode, tc.message)
			require.NoError(t, err)

			// Decode
			decodedErrorCode, decodedMessage, err := DecodeCloseSession(payload)
			require.NoError(t, err)

			// Verify
			require.Equal(t, tc.errorCode, decodedErrorCode)
			require.Equal(t, tc.message, decodedMessage)
		})
	}
}

func TestCapsuleMessageStruct(t *testing.T) {
	// Verify CapsuleMessage struct can be instantiated
	cm := CapsuleMessage{
		capsuleType: http3.CapsuleType(0x2843),
		payload:     []byte{0x00, 0x00, 0x00, 0x00},
	}

	require.Equal(t, http3.CapsuleType(0x2843), cm.capsuleType)
	require.Equal(t, []byte{0x00, 0x00, 0x00, 0x00}, cm.payload)
}
