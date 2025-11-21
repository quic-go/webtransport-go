package redirect

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTooManyRedirectsError(t *testing.T) {
	t.Run("Error message format", func(t *testing.T) {
		via := []*http.Request{
			{URL: mustParseURL("https://example.com/a")},
			{URL: mustParseURL("https://example.com/b")},
			{URL: mustParseURL("https://example.com/c")},
		}
		err := &TooManyRedirectsError{
			Limit: 3,
			Via:   via,
		}

		msg := err.Error()
		require.Contains(t, msg, "3 redirects")
		require.Contains(t, msg, "limit: 3")
	})

	t.Run("Empty via chain", func(t *testing.T) {
		err := &TooManyRedirectsError{
			Limit: 5,
			Via:   []*http.Request{},
		}

		msg := err.Error()
		require.Contains(t, msg, "0 redirects")
		require.Contains(t, msg, "limit: 5")
	})

	t.Run("Via chain length matches limit", func(t *testing.T) {
		via := make([]*http.Request, 10)
		for i := range via {
			via[i] = &http.Request{URL: mustParseURL("https://example.com/path")}
		}

		err := &TooManyRedirectsError{
			Limit: 10,
			Via:   via,
		}

		require.Len(t, err.Via, 10)
		require.Equal(t, 10, err.Limit)
	})
}

func TestRedirectLoopError(t *testing.T) {
	t.Run("Error message format", func(t *testing.T) {
		via := []*http.Request{
			{URL: mustParseURL("https://example.com/a")},
			{URL: mustParseURL("https://example.com/b")},
			{URL: mustParseURL("https://example.com/a")}, // Loop back to /a
		}
		err := &RedirectLoopError{
			URL: "https://example.com/a",
			Via: via,
		}

		msg := err.Error()
		require.Contains(t, msg, "redirect loop detected")
		require.Contains(t, msg, "https://example.com/a")
		require.Contains(t, msg, "3 redirects")
	})

	t.Run("Via chain preserves redirect history", func(t *testing.T) {
		via := []*http.Request{
			{URL: mustParseURL("https://example.com/1")},
			{URL: mustParseURL("https://example.com/2")},
		}
		err := &RedirectLoopError{
			URL: "https://example.com/1",
			Via: via,
		}

		require.Len(t, err.Via, 2)
		require.Equal(t, "https://example.com/1", err.Via[0].URL.String())
		require.Equal(t, "https://example.com/2", err.Via[1].URL.String())
	})

	t.Run("Single redirect loop", func(t *testing.T) {
		via := []*http.Request{
			{URL: mustParseURL("https://example.com/loop")},
		}
		err := &RedirectLoopError{
			URL: "https://example.com/loop",
			Via: via,
		}

		msg := err.Error()
		require.Contains(t, msg, "https://example.com/loop")
	})
}

func TestErrUseLastResponse(t *testing.T) {
	t.Run("Is a sentinel error", func(t *testing.T) {
		require.NotNil(t, ErrUseLastResponse)
		require.Equal(t, "redirect: use last response", ErrUseLastResponse.Error())
	})

	t.Run("Can be compared with errors.Is", func(t *testing.T) {
		err := ErrUseLastResponse
		require.ErrorIs(t, err, ErrUseLastResponse)
	})

	t.Run("Different instances are equal", func(t *testing.T) {
		// Sentinel errors should be package-level variables
		err1 := ErrUseLastResponse
		err2 := ErrUseLastResponse
		require.Equal(t, err1, err2)
	})
}

// mustParseURL is a test helper that panics on URL parse errors
func mustParseURL(rawURL string) *url.URL {
	u, err := url.Parse(rawURL)
	if err != nil {
		panic(err)
	}
	return u
}
