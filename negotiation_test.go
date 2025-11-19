package webtransport

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseAvailableProtocols(t *testing.T) {
	tests := []struct {
		name        string
		header      string
		want        []string
		wantErr     bool
		errContains string
	}{
		{
			name:   "empty header",
			header: "",
			want:   nil,
		},
		{
			name:   "single protocol",
			header: `"proto1"`,
			want:   []string{"proto1"},
		},
		{
			name:   "multiple protocols",
			header: `"proto2", "proto1"`,
			want:   []string{"proto2", "proto1"},
		},
		{
			name:   "protocols with preference order",
			header: `"v2", "v1", "experimental"`,
			want:   []string{"v2", "v1", "experimental"},
		},
		{
			name:   "protocol with parameters (ignored per RFC)",
			header: `"proto1";param=value, "proto2"`,
			want:   []string{"proto1", "proto2"},
		},
		{
			name:        "invalid - non-string value (integer)",
			header:      `123, "proto1"`,
			wantErr:     true,
			errContains: "invalid WT-Available-Protocols",
		},
		{
			name:        "invalid - non-string value (boolean)",
			header:      `?1, "proto1"`,
			wantErr:     true,
			errContains: "invalid WT-Available-Protocols",
		},
		{
			name:        "invalid - malformed list",
			header:      `"proto1", invalid`,
			wantErr:     true,
			errContains: "invalid WT-Available-Protocols",
		},
		{
			name:   "valid - complex protocol names",
			header: `"webtransport-quic-v1", "webtransport-experimental"`,
			want:   []string{"webtransport-quic-v1", "webtransport-experimental"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseAvailableProtocols(tt.header)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestMarshalAvailableProtocols(t *testing.T) {
	tests := []struct {
		name      string
		protocols []string
		wantEmpty bool
	}{
		{
			name:      "empty list",
			protocols: nil,
			wantEmpty: true,
		},
		{
			name:      "single protocol",
			protocols: []string{"proto1"},
		},
		{
			name:      "multiple protocols",
			protocols: []string{"v2", "v1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			marshaled, err := MarshalAvailableProtocols(tt.protocols)
			require.NoError(t, err)

			if tt.wantEmpty {
				assert.Empty(t, marshaled)
				return
			}

			// Round-trip test: parse what we marshaled
			parsed, err := ParseAvailableProtocols(marshaled)
			require.NoError(t, err)
			assert.Equal(t, tt.protocols, parsed)
		})
	}
}

func TestParseProtocol(t *testing.T) {
	tests := []struct {
		name        string
		header      string
		want        string
		wantErr     bool
		errContains string
	}{
		{
			name:   "empty header",
			header: "",
			want:   "",
		},
		{
			name:   "valid protocol",
			header: `"proto1"`,
			want:   "proto1",
		},
		{
			name:   "protocol with parameters (ignored per RFC)",
			header: `"proto1";param=value`,
			want:   "proto1",
		},
		{
			name:        "invalid - non-string value",
			header:      `123`,
			wantErr:     true,
			errContains: "invalid WT-Protocol",
		},
		{
			name:        "invalid - malformed item",
			header:      `invalid`,
			wantErr:     true,
			errContains: "invalid WT-Protocol",
		},
		{
			name:   "complex protocol name",
			header: `"webtransport-quic-v1"`,
			want:   "webtransport-quic-v1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseProtocol(tt.header)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestMarshalProtocol(t *testing.T) {
	tests := []struct {
		name      string
		protocol  string
		wantEmpty bool
	}{
		{
			name:      "empty protocol",
			protocol:  "",
			wantEmpty: true,
		},
		{
			name:     "valid protocol",
			protocol: "proto1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			marshaled, err := MarshalProtocol(tt.protocol)
			require.NoError(t, err)

			if tt.wantEmpty {
				assert.Empty(t, marshaled)
				return
			}

			// Round-trip test: parse what we marshaled
			parsed, err := ParseProtocol(marshaled)
			require.NoError(t, err)
			assert.Equal(t, tt.protocol, parsed)
		})
	}
}

func TestSelectProtocol(t *testing.T) {
	tests := []struct {
		name      string
		available []string
		preferred []string
		want      string
	}{
		{
			name:      "no available protocols",
			available: nil,
			preferred: []string{"proto1"},
			want:      "",
		},
		{
			name:      "no preferred protocols",
			available: []string{"proto1"},
			preferred: nil,
			want:      "",
		},
		{
			name:      "exact match - single protocol",
			available: []string{"proto1"},
			preferred: []string{"proto1"},
			want:      "proto1",
		},
		{
			name:      "no match",
			available: []string{"proto1"},
			preferred: []string{"proto2"},
			want:      "",
		},
		{
			name:      "multiple - client prefers v2, server has both",
			available: []string{"v2", "v1"},
			preferred: []string{"v2", "v1"},
			want:      "v2",
		},
		{
			name:      "multiple - client prefers v2, server only has v1",
			available: []string{"v2", "v1"},
			preferred: []string{"v1"},
			want:      "v1",
		},
		{
			name:      "server preference wins - first in available list",
			available: []string{"v2", "v1"},
			preferred: []string{"v1", "v2"},
			want:      "v1", // First match in preferred list
		},
		{
			name:      "complex negotiation - finds common protocol",
			available: []string{"v3", "v2", "v1"},
			preferred: []string{"v2", "v1"},
			want:      "v2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SelectProtocol(tt.available, tt.preferred)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidateSelectedProtocol(t *testing.T) {
	tests := []struct {
		name      string
		selected  string
		available []string
		wantErr   bool
	}{
		{
			name:      "empty selection is valid",
			selected:  "",
			available: []string{"proto1"},
			wantErr:   false,
		},
		{
			name:      "valid selection",
			selected:  "proto1",
			available: []string{"proto1", "proto2"},
			wantErr:   false,
		},
		{
			name:      "invalid - not in available list",
			selected:  "proto3",
			available: []string{"proto1", "proto2"},
			wantErr:   true,
		},
		{
			name:      "invalid - empty available list",
			selected:  "proto1",
			available: []string{},
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSelectedProtocol(tt.selected, tt.available)
			if tt.wantErr {
				assert.Error(t, err)
				assert.ErrorIs(t, err, ErrProtocolNotAvailable)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestRFC9651Compliance verifies compliance with RFC 9651 Structured Fields.
func TestRFC9651Compliance(t *testing.T) {
	t.Run("List format compliance", func(t *testing.T) {
		// RFC 9651: List format is comma-separated Items
		header := `"proto1", "proto2", "proto3"`
		protocols, err := ParseAvailableProtocols(header)
		require.NoError(t, err)
		assert.Equal(t, []string{"proto1", "proto2", "proto3"}, protocols)
	})

	t.Run("Item format compliance", func(t *testing.T) {
		// RFC 9651: Item format is a single value
		header := `"proto1"`
		protocol, err := ParseProtocol(header)
		require.NoError(t, err)
		assert.Equal(t, "proto1", protocol)
	})

	t.Run("Parameters ignored per RFC Section 3.3", func(t *testing.T) {
		// RFC Section 3.3, line 523: "parameters MUST be ignored"
		header := `"proto1";version=2;experimental=true, "proto2";stable=true`
		protocols, err := ParseAvailableProtocols(header)
		require.NoError(t, err)
		// Parameters should be stripped
		assert.Equal(t, []string{"proto1", "proto2"}, protocols)
	})

	t.Run("Non-string values rejected per RFC Section 3.3", func(t *testing.T) {
		// RFC Section 3.3, line 521-522: "Any value type other than String
		// MUST be treated as an error that causes the entire field to be ignored."
		headers := []string{
			`123`,         // Integer
			`?1`,          // Boolean
			`123.45`,      // Decimal
			`@1234567890`, // Date
			`:aGVsbG8=:`,  // Binary
		}

		for _, header := range headers {
			_, err := ParseAvailableProtocols(header)
			assert.Error(t, err, "should reject non-string: %s", header)
		}
	})
}
