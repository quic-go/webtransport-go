package webtransport

// settingsEnableWebtransportDraft06 is the value for ENABLE_WEBTRANSPORT
// that was used up until draft-ietf-webtrans-http3-06.
const settingsEnableWebtransportDraft06 = 0x2b603742

// settingsWebTransportEnabled is the value for SETTINGS_WT_ENABLED
const settingsWebTransportEnabled = 0x2c7cf000

const (
	// protocolHeader is the Extended-CONNECT :protocol value per
	// draft-ietf-webtrans-http3-15 §3.2, §9.1.
	protocolHeader = "webtransport-h3"
	// protocolHeaderLegacy is the pre-draft-15 value. Accepted by the server
	// (but not sent by the client) for compatibility with peers that have not yet
	// migrated.
	protocolHeaderLegacy = "webtransport"
)

func isWebTransportProtocol(s string) bool {
	return s == protocolHeader || s == protocolHeaderLegacy
}
