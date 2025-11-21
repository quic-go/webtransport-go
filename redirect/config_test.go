package redirect

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultConfig(t *testing.T) {
	t.Run("Returns config with sensible defaults", func(t *testing.T) {
		cfg := DefaultConfig()

		require.Equal(t, 10, cfg.MaxRedirects)
		require.Nil(t, cfg.CheckRedirect)
	})

	t.Run("Multiple calls return independent configs", func(t *testing.T) {
		cfg1 := DefaultConfig()
		cfg2 := DefaultConfig()

		// Modify cfg1
		cfg1.MaxRedirects = 5

		// cfg2 should not be affected
		require.Equal(t, 10, cfg2.MaxRedirects)
	})
}

func TestConfigApplyDefaults(t *testing.T) {
	t.Run("Zero MaxRedirects becomes 10", func(t *testing.T) {
		cfg := Config{}
		cfg = cfg.applyDefaults()

		require.Equal(t, 10, cfg.MaxRedirects)
	})

	t.Run("Explicit MaxRedirects is preserved", func(t *testing.T) {
		cfg := Config{MaxRedirects: 5}
		cfg = cfg.applyDefaults()

		require.Equal(t, 5, cfg.MaxRedirects)
	})

	t.Run("Negative MaxRedirects is preserved", func(t *testing.T) {
		cfg := Config{MaxRedirects: -1}
		cfg = cfg.applyDefaults()

		require.Equal(t, -1, cfg.MaxRedirects)
	})
}

func TestConfigValidate(t *testing.T) {
	t.Run("Valid default config passes", func(t *testing.T) {
		cfg := DefaultConfig()
		err := cfg.validate()
		require.NoError(t, err)
	})

	t.Run("MaxRedirects -1 is valid (unlimited)", func(t *testing.T) {
		cfg := Config{MaxRedirects: -1}
		err := cfg.validate()
		require.NoError(t, err)
	})

	t.Run("MaxRedirects < -1 is invalid", func(t *testing.T) {
		cfg := Config{MaxRedirects: -2}
		err := cfg.validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "MaxRedirects")
	})
}

func TestConfigCheckRedirectCallback(t *testing.T) {
	t.Run("CheckRedirect can be nil", func(t *testing.T) {
		cfg := Config{
			MaxRedirects:  5,
			CheckRedirect: nil,
		}

		require.Nil(t, cfg.CheckRedirect)
	})

	t.Run("CheckRedirect receives correct parameters", func(t *testing.T) {
		var receivedReq *http.Request
		var receivedVia []*http.Request

		cfg := Config{
			MaxRedirects: 5,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				receivedReq = req
				receivedVia = via
				return nil
			},
		}

		req := &http.Request{URL: mustParseURL("https://example.com/new")}
		via := []*http.Request{
			{URL: mustParseURL("https://example.com/old")},
		}

		err := cfg.CheckRedirect(req, via)
		require.NoError(t, err)
		require.Equal(t, req, receivedReq)
		require.Equal(t, via, receivedVia)
	})

	t.Run("CheckRedirect can return ErrUseLastResponse", func(t *testing.T) {
		cfg := Config{
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return ErrUseLastResponse
			},
		}

		err := cfg.CheckRedirect(nil, nil)
		require.ErrorIs(t, err, ErrUseLastResponse)
	})

	t.Run("CheckRedirect can return custom error", func(t *testing.T) {
		customErr := errors.New("custom redirect policy violation")
		cfg := Config{
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return customErr
			},
		}

		err := cfg.CheckRedirect(nil, nil)
		require.Equal(t, customErr, err)
	})
}

func TestConfigImmutability(t *testing.T) {
	t.Run("Modifying returned DefaultConfig does not affect future calls", func(t *testing.T) {
		cfg1 := DefaultConfig()
		cfg1.MaxRedirects = 999

		cfg2 := DefaultConfig()
		require.Equal(t, 10, cfg2.MaxRedirects)
	})

	t.Run("Config is value type", func(t *testing.T) {
		cfg1 := Config{MaxRedirects: 5}
		cfg2 := cfg1
		cfg2.MaxRedirects = 10

		require.Equal(t, 5, cfg1.MaxRedirects)
		require.Equal(t, 10, cfg2.MaxRedirects)
	})
}
