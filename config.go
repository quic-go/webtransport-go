package webtransport

import "github.com/quic-go/quic-go/http3"

// Config contains configuration for a WebTransport client or server.
type Config struct {
	// MaxIncomingStreams is the maximum number of concurrent bidirectional streams
	// that the peer is allowed to open in a WebTransport session.
	// If zero, SETTINGS_WT_INITIAL_MAX_STREAMS_BIDI is not sent.
	// If negative, the setting is sent with a value of zero.
	// Values larger than 2^60 are clipped to 2^60.
	MaxIncomingStreams int64

	// MaxIncomingUniStreams is the maximum number of concurrent unidirectional streams
	// that the peer is allowed to open in a WebTransport session.
	// If zero, SETTINGS_WT_INITIAL_MAX_STREAMS_UNI is not sent.
	// If negative, the setting is sent with a value of zero.
	// Values larger than 2^60 are clipped to 2^60.
	MaxIncomingUniStreams int64
}

type sessionFlowControl struct {
	Enabled bool

	MaxIncomingStreams    uint64
	MaxIncomingUniStreams uint64
	MaxOutgoingStreams    uint64
	MaxOutgoingUniStreams uint64
}

func streamLimit(limit int64) uint64 {
	if limit <= 0 {
		return 0
	}
	return min(uint64(limit), uint64(maxStreamsLimit))
}

func (c Config) addSettings(settings map[uint64]uint64) {
	delete(settings, settingsWebTransportInitialMaxStreamsBidi)
	delete(settings, settingsWebTransportInitialMaxStreamsUni)
	if c.MaxIncomingStreams != 0 {
		settings[settingsWebTransportInitialMaxStreamsBidi] = streamLimit(c.MaxIncomingStreams)
	}
	if c.MaxIncomingUniStreams != 0 {
		settings[settingsWebTransportInitialMaxStreamsUni] = streamLimit(c.MaxIncomingUniStreams)
	}
}

func (c Config) sessionFlowControl(remote *http3.Settings) sessionFlowControl {
	localEnabled := c.MaxIncomingStreams > 0 || c.MaxIncomingUniStreams > 0
	if !localEnabled || remote == nil {
		return sessionFlowControl{}
	}
	peerSettings := remote.Other
	peerEnabled := peerSettings[settingsWebTransportInitialMaxStreamsBidi] > 0 ||
		peerSettings[settingsWebTransportInitialMaxStreamsUni] > 0 ||
		peerSettings[settingsWebTransportInitialMaxData] > 0
	if !peerEnabled {
		return sessionFlowControl{}
	}
	return sessionFlowControl{
		Enabled:               true,
		MaxIncomingStreams:    streamLimit(c.MaxIncomingStreams),
		MaxIncomingUniStreams: streamLimit(c.MaxIncomingUniStreams),
		MaxOutgoingStreams:    peerSettings[settingsWebTransportInitialMaxStreamsBidi],
		MaxOutgoingUniStreams: peerSettings[settingsWebTransportInitialMaxStreamsUni],
	}
}
