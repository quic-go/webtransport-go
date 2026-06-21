package webtransport

import (
	"encoding/binary"
	"errors"
	"io"
	"unicode/utf8"

	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/quicvarint"
)

const (
	closeSessionCapsuleType   http3.CapsuleType = 0x2843
	maxStreamsBidiCapsuleType http3.CapsuleType = 0x190b4d3f
	maxStreamsUniCapsuleType  http3.CapsuleType = 0x190b4d40
)

const maxCloseCapsuleErrorMsgLen = 1024

const maxStreamsLimit = 1 << 60

type capsule interface {
	Append([]byte) []byte
}

// parseNextCapsule parses Capsules sent on the request stream.
// It returns the next known Capsule, skipping unknown Capsules.
func parseNextCapsule(r io.Reader) (capsule, error) {
	for {
		typ, capsuleReader, err := http3.ParseCapsule(quicvarint.NewReader(r))
		if err != nil {
			return nil, err
		}
		switch typ {
		case closeSessionCapsuleType:
			capsule, err := parseCloseSessionCapsule(capsuleReader)
			if err != nil {
				return nil, err
			}
			return capsule, nil
		case maxStreamsBidiCapsuleType:
			maxStreams, err := parseMaxStreamsCapsule(capsuleReader)
			if err != nil {
				return nil, err
			}
			return maxStreamsBidiCapsule{MaximumStreams: maxStreams}, nil
		case maxStreamsUniCapsuleType:
			maxStreams, err := parseMaxStreamsCapsule(capsuleReader)
			if err != nil {
				return nil, err
			}
			return maxStreamsUniCapsule{MaximumStreams: maxStreams}, nil
		default:
			// unknown capsule, skip it
			if _, err := io.Copy(io.Discard, capsuleReader); err != nil {
				return nil, err
			}
		}
	}
}

type closeSessionCapsule struct {
	ErrorCode SessionErrorCode
	Message   string
}

func parseCloseSessionCapsule(r io.Reader) (closeSessionCapsule, error) {
	var b [4]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return closeSessionCapsule{}, err
	}
	msg, err := io.ReadAll(io.LimitReader(r, maxCloseCapsuleErrorMsgLen))
	if err != nil {
		return closeSessionCapsule{}, err
	}
	return closeSessionCapsule{
		ErrorCode: SessionErrorCode(binary.BigEndian.Uint32(b[:])),
		Message:   string(msg),
	}, nil
}

func (c closeSessionCapsule) Append(b []byte) []byte {
	msg := c.Message
	if len(msg) > maxCloseCapsuleErrorMsgLen {
		msg = truncateUTF8(msg, maxCloseCapsuleErrorMsgLen)
	}

	b = quicvarint.Append(b, uint64(closeSessionCapsuleType))
	b = quicvarint.Append(b, uint64(4+len(msg)))
	payloadStart := len(b)
	b = append(b, 0, 0, 0, 0)
	binary.BigEndian.PutUint32(b[payloadStart:], uint32(c.ErrorCode))
	return append(b, msg...)
}

func (c closeSessionCapsule) ToSessionError() *SessionError {
	return &SessionError{
		Remote:    true,
		ErrorCode: c.ErrorCode,
		Message:   c.Message,
	}
}

type maxStreamsBidiCapsule struct {
	MaximumStreams uint64
}

func (c maxStreamsBidiCapsule) Append(b []byte) []byte {
	b = quicvarint.Append(b, uint64(maxStreamsBidiCapsuleType))
	b = quicvarint.Append(b, uint64(quicvarint.Len(c.MaximumStreams)))
	return quicvarint.Append(b, c.MaximumStreams)
}

type maxStreamsUniCapsule struct {
	MaximumStreams uint64
}

func (c maxStreamsUniCapsule) Append(b []byte) []byte {
	b = quicvarint.Append(b, uint64(maxStreamsUniCapsuleType))
	b = quicvarint.Append(b, uint64(quicvarint.Len(c.MaximumStreams)))
	return quicvarint.Append(b, c.MaximumStreams)
}

func parseMaxStreamsCapsule(r io.Reader) (uint64, error) {
	maxStreams, err := quicvarint.Read(quicvarint.NewReader(r))
	if err != nil {
		return 0, err
	}
	var extra [1]byte
	if _, err := io.ReadFull(r, extra[:]); err == nil {
		return 0, errors.New("WT_MAX_STREAMS capsule has trailing data")
	} else if err != io.EOF {
		return 0, err
	}
	if maxStreams > maxStreamsLimit {
		return 0, &http3.Error{ErrorCode: http3.ErrCodeDatagramError, ErrorMessage: "WT_MAX_STREAMS value too large"}
	}
	return maxStreams, nil
}

// truncateUTF8 cuts a string to max n bytes without breaking UTF-8 characters.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
