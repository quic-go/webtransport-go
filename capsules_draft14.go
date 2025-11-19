package webtransport

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/quicvarint"
)

// Capsule type constants as defined in draft-ietf-webtrans-http3-14
const (
	drainSessionCapsuleType       http3.CapsuleType = 0x78ae
	maxStreamsBidiCapsuleType     http3.CapsuleType = 0x190B4D3F
	maxStreamsUniCapsuleType      http3.CapsuleType = 0x190B4D40
	streamsBlockedBidiCapsuleType http3.CapsuleType = 0x190B4D43
	streamsBlockedUniCapsuleType  http3.CapsuleType = 0x190B4D44
	maxDataCapsuleType            http3.CapsuleType = 0x190B4D3D
	dataBlockedCapsuleType        http3.CapsuleType = 0x190B4D41
)

// StreamType represents the type of WebTransport stream.
type StreamType uint8

const (
	// StreamTypeBidi represents a bidirectional stream.
	StreamTypeBidi StreamType = iota
	// StreamTypeUni represents a unidirectional stream.
	StreamTypeUni
)

// Errors for capsule operations
var (
	ErrNonEmptyPayload    = errors.New("drain session capsule must have empty payload")
	ErrMaxStreamsExceeded = errors.New("maxStreams value exceeds 2^60 limit")
	ErrInvalidCapsuleType = errors.New("invalid capsule type")
)

// maxStreamsLimit is the maximum allowed value for maxStreams as per RFC Section 5.6.2
const maxStreamsLimit uint64 = 1 << 60

// EncodeDrainSession encodes a WT_DRAIN_SESSION capsule payload.
// This capsule has type 0x78ae and a zero-length payload.
// It signals that the sender is initiating graceful shutdown.
//
// Returns an empty byte slice since the payload is zero-length per RFC Section 4.7.
func EncodeDrainSession() []byte {
	return []byte{}
}

// DecodeDrainSession decodes a WT_DRAIN_SESSION capsule payload.
// The payload must be empty (zero length) per RFC Section 4.7.
//
// Returns an error if the payload is non-empty.
func DecodeDrainSession(payload []byte) error {
	if len(payload) != 0 {
		return ErrNonEmptyPayload
	}
	return nil
}

// EncodeMaxStreams encodes a WT_MAX_STREAMS capsule payload.
// The capsule type should be 0x190B4D3F for bidirectional streams or 0x190B4D40
// for unidirectional streams (determined by the caller based on streamType).
//
// maxStreams is validated to be ≤ 2^60 per RFC Section 5.6.2, line 1132.
// This limit exists because stream IDs are limited to 2^62-1 and cumulative counts must fit.
//
// Returns the encoded payload as a varint or an error if validation fails.
func EncodeMaxStreams(streamType StreamType, maxStreams uint64) ([]byte, error) {
	// Validate maxStreams ≤ 2^60
	if maxStreams > maxStreamsLimit {
		return nil, fmt.Errorf("%w: %d > %d", ErrMaxStreamsExceeded, maxStreams, maxStreamsLimit)
	}

	// Validate stream type
	if streamType != StreamTypeBidi && streamType != StreamTypeUni {
		return nil, fmt.Errorf("%w: unknown stream type %d", ErrInvalidCapsuleType, streamType)
	}

	return quicvarint.Append(nil, maxStreams), nil
}

// DecodeMaxStreams decodes a WT_MAX_STREAMS capsule payload.
// It parses the varint-encoded maxStreams value from the payload and determines
// the stream type based on the capsule type.
//
// The maxStreams value is validated to be ≤ 2^60 per RFC Section 5.6.2.
//
// Returns the stream type, maxStreams value, and an error if validation or parsing fails.
func DecodeMaxStreams(capsuleType http3.CapsuleType, payload []byte) (StreamType, uint64, error) {
	// Determine stream type from capsule type
	var streamType StreamType
	switch capsuleType {
	case maxStreamsBidiCapsuleType:
		streamType = StreamTypeBidi
	case maxStreamsUniCapsuleType:
		streamType = StreamTypeUni
	default:
		return 0, 0, fmt.Errorf("%w: expected 0x%x or 0x%x, got 0x%x",
			ErrInvalidCapsuleType, maxStreamsBidiCapsuleType, maxStreamsUniCapsuleType, capsuleType)
	}

	// Parse maxStreams from payload
	r := bytes.NewReader(payload)
	maxStreams, err := quicvarint.Read(quicvarint.NewReader(r))
	if err != nil {
		return 0, 0, fmt.Errorf("failed to decode maxStreams: %w", err)
	}

	// RFC Section 5.6.2: maxStreams MUST NOT exceed 2^60
	if maxStreams > maxStreamsLimit {
		return 0, 0, fmt.Errorf("%w: %d > %d", ErrMaxStreamsExceeded, maxStreams, maxStreamsLimit)
	}

	// Ensure all bytes were consumed
	if r.Len() > 0 {
		return 0, 0, fmt.Errorf("unexpected trailing bytes in WT_MAX_STREAMS payload")
	}

	return streamType, maxStreams, nil
}

// EncodeMaxData encodes a WT_MAX_DATA capsule payload.
// maxData is the maximum amount of data that can be sent.
// Returns the encoded payload as a byte slice.
func EncodeMaxData(maxData uint64) []byte {
	return quicvarint.Append(nil, maxData)
}

// DecodeMaxData decodes a WT_MAX_DATA capsule payload.
// Returns the maxData value from the payload.
func DecodeMaxData(payload []byte) (uint64, error) {
	r := bytes.NewReader(payload)
	maxData, err := quicvarint.Read(quicvarint.NewReader(r))
	if err != nil {
		return 0, fmt.Errorf("failed to decode maxData: %w", err)
	}
	// Ensure all bytes were consumed
	if r.Len() > 0 {
		return 0, fmt.Errorf("unexpected trailing bytes in WT_MAX_DATA payload")
	}
	return maxData, nil
}

// EncodeStreamsBlocked encodes a WT_STREAMS_BLOCKED capsule payload.
// streamType should be either StreamTypeBidi or StreamTypeUni to indicate
// which type of streams is blocked.
// limit is the stream limit that prevented opening a new stream.
// Returns the encoded payload as a byte slice or an error if validation fails.
func EncodeStreamsBlocked(streamType StreamType, limit uint64) ([]byte, error) {
	// Validate stream type
	if streamType != StreamTypeBidi && streamType != StreamTypeUni {
		return nil, fmt.Errorf("%w: unknown stream type %d", ErrInvalidCapsuleType, streamType)
	}

	return quicvarint.Append(nil, limit), nil
}

// DecodeStreamsBlocked decodes a WT_STREAMS_BLOCKED capsule payload.
// It determines the stream type based on the capsule type and parses the limit value.
// Returns the stream type, limit value, and an error if parsing fails.
func DecodeStreamsBlocked(capsuleType http3.CapsuleType, payload []byte) (StreamType, uint64, error) {
	// Determine stream type from capsule type
	var streamType StreamType
	switch capsuleType {
	case streamsBlockedBidiCapsuleType:
		streamType = StreamTypeBidi
	case streamsBlockedUniCapsuleType:
		streamType = StreamTypeUni
	default:
		return 0, 0, fmt.Errorf("%w: expected 0x%x or 0x%x, got 0x%x",
			ErrInvalidCapsuleType, streamsBlockedBidiCapsuleType, streamsBlockedUniCapsuleType, capsuleType)
	}

	// Parse limit from payload
	r := bytes.NewReader(payload)
	limit, err := quicvarint.Read(quicvarint.NewReader(r))
	if err != nil {
		return 0, 0, fmt.Errorf("failed to decode streams blocked limit: %w", err)
	}

	// Ensure all bytes were consumed
	if r.Len() > 0 {
		return 0, 0, fmt.Errorf("unexpected trailing bytes in WT_STREAMS_BLOCKED payload")
	}

	return streamType, limit, nil
}

// EncodeDataBlocked encodes a WT_DATA_BLOCKED capsule payload.
// limit is the data limit that prevented sending data.
// Returns the encoded payload as a byte slice.
func EncodeDataBlocked(limit uint64) []byte {
	return quicvarint.Append(nil, limit)
}

// DecodeDataBlocked decodes a WT_DATA_BLOCKED capsule payload.
// Returns the data limit value from the payload.
func DecodeDataBlocked(payload []byte) (uint64, error) {
	r := bytes.NewReader(payload)
	limit, err := quicvarint.Read(quicvarint.NewReader(r))
	if err != nil {
		return 0, fmt.Errorf("failed to decode data blocked limit: %w", err)
	}
	// Ensure all bytes were consumed
	if r.Len() > 0 {
		return 0, fmt.Errorf("unexpected trailing bytes in WT_DATA_BLOCKED payload")
	}
	return limit, nil
}
