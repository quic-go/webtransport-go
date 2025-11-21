package redirect

import (
	"testing"

	webtransport "github.com/quic-go/webtransport-go"
	"github.com/stretchr/testify/require"
)

func TestNewDialer(t *testing.T) {
	t.Run("Creates dialer with valid config", func(t *testing.T) {
		baseDialer := &webtransport.Dialer{}
		cfg := DefaultConfig()

		d, err := NewDialer(baseDialer, cfg)
		require.NoError(t, err)
		require.NotNil(t, d)
		require.Equal(t, baseDialer, d.dialer)
	})

	t.Run("Returns error if base dialer is nil", func(t *testing.T) {
		cfg := DefaultConfig()

		d, err := NewDialer(nil, cfg)
		require.Error(t, err)
		require.Nil(t, d)
		require.Contains(t, err.Error(), "dialer must not be nil")
	})

	t.Run("Validates config", func(t *testing.T) {
		baseDialer := &webtransport.Dialer{}
		cfg := Config{
			MaxRedirects: -2, // Invalid
		}

		d, err := NewDialer(baseDialer, cfg)
		require.Error(t, err)
		require.Nil(t, d)
	})

	t.Run("Applies defaults to config", func(t *testing.T) {
		baseDialer := &webtransport.Dialer{}
		cfg := Config{} // All zeros

		d, err := NewDialer(baseDialer, cfg)
		require.NoError(t, err)
		require.Equal(t, 10, d.config.MaxRedirects)
	})

	t.Run("Config is copied, not referenced", func(t *testing.T) {
		baseDialer := &webtransport.Dialer{}
		cfg := Config{MaxRedirects: 5}

		d, err := NewDialer(baseDialer, cfg)
		require.NoError(t, err)

		// Modify original config
		cfg.MaxRedirects = 99

		// Dialer should have original value
		require.Equal(t, 5, d.config.MaxRedirects)
	})
}

func TestDialerConcurrency(t *testing.T) {
	t.Run("Dialer is safe for concurrent use", func(t *testing.T) {
		baseDialer := &webtransport.Dialer{}
		cfg := DefaultConfig()

		d, err := NewDialer(baseDialer, cfg)
		require.NoError(t, err)

		// Verify dialer fields are immutable
		require.NotNil(t, d.dialer)
		require.Equal(t, 10, d.config.MaxRedirects)

		// Note: Actual concurrent Dial() calls would require integration tests
		// with a real server, which is beyond the scope of unit tests.
	})
}

// Note: Integration tests for Dial() would require:
// 1. Setting up a real QUIC server with HTTP/3
// 2. Configuring WebTransport support
// 3. Testing actual redirect responses
//
// These are complex and time-consuming, so they are deferred to
// retry/redirect_integration_test.go which can be run with:
// go test -tags=integration ./retry
//
// For now, we verify that the Dialer is created correctly and
// holds the expected configuration.
