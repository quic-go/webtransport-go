package webtransport

import (
	"errors"
	"github.com/lucas-clemente/quic-go"
)

type ErrorCode uint8

const (
	firstErrorCode = 0x52e4a40fa8db
	lastErrorCode  = 0x52e4a40fa9e2
)

func webtransportCodeToHTTPCode(n ErrorCode) quic.StreamErrorCode {
	return quic.StreamErrorCode(firstErrorCode) + quic.StreamErrorCode(n) + quic.StreamErrorCode(n/0x1e)
}

func httpCodeToWebtransportCode(h quic.StreamErrorCode) (ErrorCode, error) {
	if h < firstErrorCode || h > lastErrorCode {
		return 0, errors.New("error code outside of expected range")
	}
	if (h-0x21)%0x1f == 0 {
		return 0, errors.New("invalid error code")
	}
	shifted := h - firstErrorCode
	return ErrorCode(shifted - shifted/0x1f), nil
}
