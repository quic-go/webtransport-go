package webtransport

import (
	"encoding/binary"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/quic-go/quic-go/http3"
)

// CapsuleMessage represents an HTTP Datagram Capsule per RFC 9297.
// It provides an internal abstraction for encoding and decoding WebTransport
// control messages sent over the CONNECT stream.
type CapsuleMessage struct {
	capsuleType http3.CapsuleType // The capsule type identifier (e.g., 0x2843 for CLOSE_SESSION)
	payload     []byte            // The raw capsule payload (variable length)
}

const (
	// maxCloseSessionMessageLen is the maximum allowed length for a CLOSE_SESSION message
	// per RFC Section 6, line 1331
	maxCloseSessionMessageLen = 1024
)

var (
	// ErrInvalidPayloadLength is returned when the payload length doesn't match type requirements
	ErrInvalidPayloadLength = errors.New("invalid payload length")

	// ErrMessageTooLong is returned when a CLOSE_SESSION message exceeds 1024 bytes
	ErrMessageTooLong = errors.New("close session message exceeds 1024 bytes")

	// ErrInvalidUTF8 is returned when a CLOSE_SESSION message contains invalid UTF-8
	ErrInvalidUTF8 = errors.New("message contains invalid UTF-8")
)

// NOTE: Flow control capsule encode/decode functions (EncodeMaxData, DecodeMaxData,
// EncodeStreamsBlocked, DecodeStreamsBlocked, EncodeDataBlocked, DecodeDataBlocked)
// are implemented in capsules_draft14.go along with other draft-14 capsule types.

// EncodeCloseSession encodes a WT_CLOSE_SESSION capsule payload.
// The capsule type is 0x2843.
// Format: uint32 error code (big-endian) + UTF-8 message string
//
// Parameters:
//   - errorCode: The application error code (0x00000000 to 0xffffffff)
//   - message: UTF-8 error message (max 1024 bytes)
//
// Returns the encoded payload bytes or an error if the message is invalid.
func EncodeCloseSession(errorCode uint32, message string) ([]byte, error) {
	// Validate message length
	if len(message) > maxCloseSessionMessageLen {
		return nil, fmt.Errorf("%w: %d bytes (max %d)", ErrMessageTooLong, len(message), maxCloseSessionMessageLen)
	}

	// Validate UTF-8 encoding
	if !utf8.ValidString(message) {
		return nil, ErrInvalidUTF8
	}

	// Encode: 4 bytes error code + message bytes
	payload := make([]byte, 4, 4+len(message))
	binary.BigEndian.PutUint32(payload, errorCode)
	payload = append(payload, []byte(message)...)

	return payload, nil
}

// DecodeCloseSession decodes a WT_CLOSE_SESSION capsule payload.
// Format: uint32 error code (big-endian) + UTF-8 message string
//
// Parameters:
//   - payload: The raw capsule payload bytes
//
// Returns the error code, message string, and any decoding error.
// Invalid UTF-8 sequences are handled by replacement with U+FFFD.
func DecodeCloseSession(payload []byte) (errorCode uint32, message string, err error) {
	// Validate minimum payload length (need at least 4 bytes for error code)
	if len(payload) < 4 {
		return 0, "", fmt.Errorf("%w: need at least 4 bytes, got %d", ErrInvalidPayloadLength, len(payload))
	}

	// Decode error code (first 4 bytes, big-endian)
	errorCode = binary.BigEndian.Uint32(payload[:4])

	// Decode message (remaining bytes)
	messageBytes := payload[4:]

	// Validate message length
	if len(messageBytes) > maxCloseSessionMessageLen {
		return 0, "", fmt.Errorf("%w: %d bytes (max %d)", ErrMessageTooLong, len(messageBytes), maxCloseSessionMessageLen)
	}

	// Convert to string - Go automatically handles invalid UTF-8 by replacing with U+FFFD
	message = string(messageBytes)

	return errorCode, message, nil
}
