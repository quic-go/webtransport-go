package webtransport

// SettingsWTMaxSessions is the SETTINGS parameter for WebTransport draft-14.
// It indicates the maximum number of concurrent WebTransport sessions supported.
// RFC Section 3.1, line 328.
const SettingsWTMaxSessions = 0x14e9cd29

const protocolHeader = "webtransport"

// Capsule types for WebTransport draft-14 flow control.
const (
	// CapsuleWTMaxData announces the maximum amount of data that can be sent.
	CapsuleWTMaxData uint64 = 0x190B4D3D

	// CapsuleWTMaxStreamsBidi announces the maximum number of bidirectional streams.
	CapsuleWTMaxStreamsBidi uint64 = 0x190B4D3F

	// CapsuleWTMaxStreamsUni announces the maximum number of unidirectional streams.
	CapsuleWTMaxStreamsUni uint64 = 0x190B4D40

	// CapsuleWTDataBlocked signals that sending is blocked due to data limits.
	CapsuleWTDataBlocked uint64 = 0x190B4D41

	// CapsuleWTStreamsBlockedBidi signals that stream creation is blocked for bidirectional streams.
	CapsuleWTStreamsBlockedBidi uint64 = 0x190B4D43

	// CapsuleWTStreamsBlockedUni signals that stream creation is blocked for unidirectional streams.
	CapsuleWTStreamsBlockedUni uint64 = 0x190B4D44
)
