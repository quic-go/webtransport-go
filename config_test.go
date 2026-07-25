package webtransport

import (
	"testing"

	"github.com/quic-go/quic-go/http3"

	"github.com/stretchr/testify/require"
)

func TestConfigAddsFlowControlSettings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config Config
		want   map[uint64]uint64
	}{
		{
			name: "zero values",
			want: map[uint64]uint64{},
		},
		{
			name:   "explicit zero",
			config: Config{MaxIncomingStreams: -1, MaxIncomingUniStreams: -1, MaxIncomingData: -1},
			want: map[uint64]uint64{
				settingsWebTransportInitialMaxStreamsBidi: 0,
				settingsWebTransportInitialMaxStreamsUni:  0,
				settingsWebTransportInitialMaxData:        0,
			},
		},
		{
			name:   "positive",
			config: Config{MaxIncomingStreams: 10, MaxIncomingUniStreams: 20, MaxIncomingData: 30},
			want: map[uint64]uint64{
				settingsWebTransportInitialMaxStreamsBidi: 10,
				settingsWebTransportInitialMaxStreamsUni:  20,
				settingsWebTransportInitialMaxData:        30,
			},
		},
		{
			name:   "clipped",
			config: Config{MaxIncomingStreams: 1<<60 + 1, MaxIncomingUniStreams: 1<<60 + 2, MaxIncomingData: 1 << 62},
			want: map[uint64]uint64{
				settingsWebTransportInitialMaxStreamsBidi: maxStreamsLimit,
				settingsWebTransportInitialMaxStreamsUni:  maxStreamsLimit,
				settingsWebTransportInitialMaxData:        1<<62 - 1,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := map[uint64]uint64{
				settingsWebTransportInitialMaxStreamsBidi: 42,
				settingsWebTransportInitialMaxStreamsUni:  42,
				settingsWebTransportInitialMaxData:        42,
			}
			tc.config.addSettings(settings)
			require.Equal(t, tc.want, settings)
		})
	}
}

func TestConfigNegotiatesFlowControl(t *testing.T) {
	config := Config{MaxIncomingStreams: 10, MaxIncomingUniStreams: -1, MaxIncomingData: 20}
	remote := &http3.Settings{Other: map[uint64]uint64{
		settingsWebTransportInitialMaxStreamsBidi: 3,
		settingsWebTransportInitialMaxStreamsUni:  4,
		settingsWebTransportInitialMaxData:        5,
	}}
	require.Equal(t, sessionFlowControl{
		Enabled:               true,
		MaxIncomingStreams:    10,
		MaxIncomingUniStreams: 0,
		MaxOutgoingStreams:    3,
		MaxOutgoingUniStreams: 4,
		MaxIncomingData:       20,
		MaxOutgoingData:       5,
	}, config.sessionFlowControl(remote))

	for _, tc := range []struct {
		name    string
		config  Config
		remote  *http3.Settings
		enabled bool
	}{
		{
			name:   "disabled when local streams disabled",
			config: Config{MaxIncomingStreams: -1},
			remote: &http3.Settings{Other: map[uint64]uint64{
				settingsWebTransportInitialMaxStreamsBidi: 3,
			}},
			enabled: false,
		},
		{
			name:    "disabled when peer sends no flow control",
			config:  config,
			remote:  &http3.Settings{},
			enabled: false,
		},
		{
			name:   "enabled via max data",
			config: config,
			remote: &http3.Settings{Other: map[uint64]uint64{
				settingsWebTransportInitialMaxData: 1,
			}},
			enabled: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.enabled, tc.config.sessionFlowControl(tc.remote).Enabled)
		})
	}
}
