package webtransport

import (
	"encoding/binary"
	"io"

	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/quicvarint"
)

const closeSessionCapsuleType http3.CapsuleType = 0x2843

const maxCloseCapsuleErrorMsgLen = 1024

// parseNextCapsule parses the next Capsule sent on the request stream.
// It returns a SessionError, if the capsule received is a WT_CLOSE_SESSION Capsule.
func parseNextCapsule(r io.Reader) error {
	for {
		typ, capsuleReader, err := http3.ParseCapsule(quicvarint.NewReader(r))
		if err != nil {
			return err
		}
		switch typ {
		case closeSessionCapsuleType:
			var b [4]byte
			if _, err := io.ReadFull(capsuleReader, b[:]); err != nil {
				return err
			}
			appErrCode := binary.BigEndian.Uint32(b[:])
			// the length of the error message is limited to 1024 bytes
			appErrMsg, err := io.ReadAll(io.LimitReader(capsuleReader, maxCloseCapsuleErrorMsgLen))
			if err != nil {
				return err
			}
			return &SessionError{
				Remote:    true,
				ErrorCode: SessionErrorCode(appErrCode),
				Message:   string(appErrMsg),
			}
		default:
			// unknown capsule, skip it
			if _, err := io.Copy(io.Discard, capsuleReader); err != nil {
				return err
			}
		}
	}
}
