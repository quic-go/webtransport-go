package webtransport

import (
	"errors"
	"fmt"

	"github.com/dunglas/httpsfv"
)

const (
	// HeaderWTAvailableProtocols is the header key for protocol negotiation.
	// RFC Section 3.3, lines 509-510.
	HeaderWTAvailableProtocols = "WT-Available-Protocols"

	// HeaderWTProtocol is the header key for the selected protocol.
	// RFC Section 3.3, lines 513-514.
	HeaderWTProtocol = "WT-Protocol"
)

var (
	// ErrInvalidProtocolList is returned when the WT-Available-Protocols header
	// cannot be parsed as a valid RFC 9651 List.
	ErrInvalidProtocolList = errors.New("webtransport: invalid WT-Available-Protocols header")

	// ErrInvalidProtocolItem is returned when the WT-Protocol header
	// cannot be parsed as a valid RFC 9651 Item.
	ErrInvalidProtocolItem = errors.New("webtransport: invalid WT-Protocol header")

	// ErrProtocolNotAvailable is returned when the selected protocol
	// is not in the available protocols list.
	ErrProtocolNotAvailable = errors.New("webtransport: selected protocol not in available list")

	// ErrNoProtocolMatch is returned when no protocol match is found.
	ErrNoProtocolMatch = errors.New("webtransport: no protocol match found")
)

// ParseAvailableProtocols parses the WT-Available-Protocols header value
// according to RFC 9651 Structured Fields (List format).
//
// Per RFC Section 3.3, lines 518-523:
// - WT-Available-Protocols is a List of Items
// - Only String values are valid; other types are ignored
// - Parameters on items MUST be ignored
//
// Returns a slice of protocol strings in preference order (most preferred first).
// Invalid items (non-strings) are silently skipped per RFC requirements.
func ParseAvailableProtocols(header string) ([]string, error) {
	if header == "" {
		return nil, nil
	}

	// Parse as RFC 9651 List
	list, err := httpsfv.UnmarshalList([]string{header})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidProtocolList, err)
	}

	protocols := make([]string, 0, len(list))
	for _, member := range list {
		// List members should be Items
		item, ok := member.(httpsfv.Item)
		if !ok {
			// Unexpected type - ignore entire field per RFC
			return nil, ErrInvalidProtocolList
		}

		// RFC Section 3.3, line 521: "the only valid value type is a String"
		// "Any value type other than String MUST be treated as an error that
		// causes the entire field to be ignored."
		str, ok := item.Value.(string)
		if !ok {
			// Invalid type - ignore entire field per RFC
			return nil, ErrInvalidProtocolList
		}

		// Extract string value and add to protocols list
		// Parameters are ignored per RFC Section 3.3, line 523
		protocols = append(protocols, str)
	}

	return protocols, nil
}

// MarshalAvailableProtocols creates a WT-Available-Protocols header value
// from a list of protocol strings, formatted as an RFC 9651 Structured Fields List.
func MarshalAvailableProtocols(protocols []string) (string, error) {
	if len(protocols) == 0 {
		return "", nil
	}

	var list httpsfv.List
	for _, proto := range protocols {
		list = append(list, httpsfv.NewItem(proto))
	}

	return httpsfv.Marshal(list)
}

// ParseProtocol parses the WT-Protocol header value according to RFC 9651
// Structured Fields (Item format).
//
// Per RFC Section 3.3, lines 518-523:
// - WT-Protocol is an Item
// - Only String values are valid
// - Parameters MUST be ignored
//
// Returns the selected protocol string.
func ParseProtocol(header string) (string, error) {
	if header == "" {
		return "", nil
	}

	// Parse as RFC 9651 Item
	item, err := httpsfv.UnmarshalItem([]string{header})
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidProtocolItem, err)
	}

	// RFC Section 3.3, line 521: "the only valid value type is a String"
	str, ok := item.Value.(string)
	if !ok {
		return "", ErrInvalidProtocolItem
	}

	// Parameters are ignored per RFC Section 3.3, line 523
	return str, nil
}

// MarshalProtocol creates a WT-Protocol header value from a protocol string,
// formatted as an RFC 9651 Structured Fields Item.
func MarshalProtocol(protocol string) (string, error) {
	if protocol == "" {
		return "", nil
	}

	item := httpsfv.NewItem(protocol)
	return httpsfv.Marshal(item)
}

// SelectProtocol selects the first protocol from the preferred list
// that appears in the available list.
//
// This implements a simple "first match wins" strategy where the client's
// preference order (in available) is respected.
//
// Returns the selected protocol, or an empty string if no match is found.
func SelectProtocol(available []string, preferred []string) string {
	// Build a map of available protocols for O(1) lookup
	availableMap := make(map[string]bool, len(available))
	for _, proto := range available {
		availableMap[proto] = true
	}

	// Find first preferred protocol that's available
	for _, proto := range preferred {
		if availableMap[proto] {
			return proto
		}
	}

	return ""
}

// ValidateSelectedProtocol ensures that the selected protocol is in the
// available protocols list, as required by RFC Section 3.3, lines 525-527:
// "The value in the WT-Protocol response header field MUST be one of the
// values listed in WT-Available-Protocols of the request."
func ValidateSelectedProtocol(selected string, available []string) error {
	if selected == "" {
		return nil
	}

	for _, proto := range available {
		if proto == selected {
			return nil
		}
	}

	return ErrProtocolNotAvailable
}
