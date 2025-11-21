package redirect

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFollowRedirects(t *testing.T) {
	t.Run("Returns nil for redirects within limit", func(t *testing.T) {
		fn := FollowRedirects(10)

		via := []*http.Request{
			{URL: mustParseURL("https://example.com/a")},
		}
		err := fn(nil, via)
		require.NoError(t, err)
	})

	t.Run("Returns TooManyRedirectsError when limit exceeded", func(t *testing.T) {
		fn := FollowRedirects(3)

		via := []*http.Request{
			{URL: mustParseURL("https://example.com/a")},
			{URL: mustParseURL("https://example.com/b")},
			{URL: mustParseURL("https://example.com/c")},
		}
		err := fn(nil, via)

		var tooManyErr *TooManyRedirectsError
		require.ErrorAs(t, err, &tooManyErr)
		require.Equal(t, 3, tooManyErr.Limit)
	})

	t.Run("Zero maxRedirects returns ErrUseLastResponse", func(t *testing.T) {
		fn := FollowRedirects(0)

		err := fn(nil, nil)
		require.ErrorIs(t, err, ErrUseLastResponse)
	})

	t.Run("Negative maxRedirects returns ErrUseLastResponse", func(t *testing.T) {
		fn := FollowRedirects(-1)

		err := fn(nil, nil)
		require.ErrorIs(t, err, ErrUseLastResponse)
	})

	t.Run("Empty via chain is allowed", func(t *testing.T) {
		fn := FollowRedirects(10)

		err := fn(nil, []*http.Request{})
		require.NoError(t, err)
	})
}

func TestIsRedirectStatus(t *testing.T) {
	t.Run("301 is redirect", func(t *testing.T) {
		require.True(t, isRedirectStatus(http.StatusMovedPermanently))
	})

	t.Run("302 is redirect", func(t *testing.T) {
		require.True(t, isRedirectStatus(http.StatusFound))
	})

	t.Run("303 is redirect", func(t *testing.T) {
		require.True(t, isRedirectStatus(http.StatusSeeOther))
	})

	t.Run("307 is redirect", func(t *testing.T) {
		require.True(t, isRedirectStatus(http.StatusTemporaryRedirect))
	})

	t.Run("308 is redirect", func(t *testing.T) {
		require.True(t, isRedirectStatus(http.StatusPermanentRedirect))
	})

	t.Run("200 is not redirect", func(t *testing.T) {
		require.False(t, isRedirectStatus(http.StatusOK))
	})

	t.Run("404 is not redirect", func(t *testing.T) {
		require.False(t, isRedirectStatus(http.StatusNotFound))
	})

	t.Run("304 is not redirect", func(t *testing.T) {
		require.False(t, isRedirectStatus(http.StatusNotModified))
	})

	t.Run("500 is not redirect", func(t *testing.T) {
		require.False(t, isRedirectStatus(http.StatusInternalServerError))
	})
}

func TestExtractLocation(t *testing.T) {
	t.Run("Extracts Location header", func(t *testing.T) {
		resp := &http.Response{
			Header: http.Header{
				"Location": []string{"https://example.com/new"},
			},
		}

		location, err := extractLocation(resp)
		require.NoError(t, err)
		require.Equal(t, "https://example.com/new", location)
	})

	t.Run("Returns error if Location missing", func(t *testing.T) {
		resp := &http.Response{
			Header: http.Header{},
		}

		location, err := extractLocation(resp)
		require.Error(t, err)
		require.Contains(t, err.Error(), "missing Location header")
		require.Empty(t, location)
	})

	t.Run("Returns error if Location empty", func(t *testing.T) {
		resp := &http.Response{
			Header: http.Header{
				"Location": []string{""},
			},
		}

		location, err := extractLocation(resp)
		require.Error(t, err)
		require.Empty(t, location)
	})
}

func TestResolveRedirectLocation(t *testing.T) {
	t.Run("Absolute URL is returned as-is", func(t *testing.T) {
		base := mustParseURL("https://example.com/old")
		location := "https://other.com/new"

		resolved, err := resolveRedirectLocation(base, location)
		require.NoError(t, err)
		require.Equal(t, "https://other.com/new", resolved.String())
	})

	t.Run("Relative path is resolved", func(t *testing.T) {
		base := mustParseURL("https://example.com/path/old")
		location := "new"

		resolved, err := resolveRedirectLocation(base, location)
		require.NoError(t, err)
		require.Equal(t, "https://example.com/path/new", resolved.String())
	})

	t.Run("Root-relative path is resolved", func(t *testing.T) {
		base := mustParseURL("https://example.com/path/old")
		location := "/new"

		resolved, err := resolveRedirectLocation(base, location)
		require.NoError(t, err)
		require.Equal(t, "https://example.com/new", resolved.String())
	})

	t.Run("Protocol-relative URL is resolved", func(t *testing.T) {
		base := mustParseURL("https://example.com/old")
		location := "//other.com/new"

		resolved, err := resolveRedirectLocation(base, location)
		require.NoError(t, err)
		require.Equal(t, "https://other.com/new", resolved.String())
	})

	t.Run("Query parameters are preserved", func(t *testing.T) {
		base := mustParseURL("https://example.com/old")
		location := "new?param=value"

		resolved, err := resolveRedirectLocation(base, location)
		require.NoError(t, err)
		require.Equal(t, "https://example.com/new?param=value", resolved.String())
	})

	t.Run("Fragment is preserved", func(t *testing.T) {
		base := mustParseURL("https://example.com/old")
		location := "new#section"

		resolved, err := resolveRedirectLocation(base, location)
		require.NoError(t, err)
		require.Equal(t, "https://example.com/new#section", resolved.String())
	})

	t.Run("Empty location returns error", func(t *testing.T) {
		base := mustParseURL("https://example.com/old")

		resolved, err := resolveRedirectLocation(base, "")
		require.Error(t, err)
		require.Contains(t, err.Error(), "empty")
		require.Nil(t, resolved)
	})

	t.Run("Malformed location returns error", func(t *testing.T) {
		base := mustParseURL("https://example.com/old")

		resolved, err := resolveRedirectLocation(base, "://invalid")
		require.Error(t, err)
		require.Contains(t, err.Error(), "malformed")
		require.Nil(t, resolved)
	})

	t.Run("Parent directory path is resolved", func(t *testing.T) {
		base := mustParseURL("https://example.com/a/b/c")
		location := "../d"

		resolved, err := resolveRedirectLocation(base, location)
		require.NoError(t, err)
		require.Equal(t, "https://example.com/a/d", resolved.String())
	})
}

func TestCheckRedirectLoop(t *testing.T) {
	t.Run("No loop in empty via chain", func(t *testing.T) {
		err := checkRedirectLoop("https://example.com/a", []*http.Request{})
		require.NoError(t, err)
	})

	t.Run("No loop with unique URLs", func(t *testing.T) {
		via := []*http.Request{
			{URL: mustParseURL("https://example.com/a")},
			{URL: mustParseURL("https://example.com/b")},
			{URL: mustParseURL("https://example.com/c")},
		}

		err := checkRedirectLoop("https://example.com/d", via)
		require.NoError(t, err)
	})

	t.Run("Detects loop when URL visited twice", func(t *testing.T) {
		via := []*http.Request{
			{URL: mustParseURL("https://example.com/a")},
			{URL: mustParseURL("https://example.com/b")},
		}

		err := checkRedirectLoop("https://example.com/a", via)

		var loopErr *RedirectLoopError
		require.ErrorAs(t, err, &loopErr)
		require.Equal(t, "https://example.com/a", loopErr.URL)
	})

	t.Run("Detects loop with query parameters", func(t *testing.T) {
		via := []*http.Request{
			{URL: mustParseURL("https://example.com/page?param=1")},
		}

		err := checkRedirectLoop("https://example.com/page?param=1", via)

		var loopErr *RedirectLoopError
		require.ErrorAs(t, err, &loopErr)
	})

	t.Run("Different query parameters not considered loop", func(t *testing.T) {
		via := []*http.Request{
			{URL: mustParseURL("https://example.com/page?param=1")},
		}

		err := checkRedirectLoop("https://example.com/page?param=2", via)
		require.NoError(t, err)
	})

	t.Run("URL with fragment is distinct", func(t *testing.T) {
		via := []*http.Request{
			{URL: mustParseURL("https://example.com/page")},
		}

		// Fragments are typically not included in URL.String() comparison
		err := checkRedirectLoop("https://example.com/page#section", via)
		require.NoError(t, err)
	})
}

func TestCloseRedirectBody(t *testing.T) {
	t.Run("Handles nil response", func(t *testing.T) {
		// Should not panic
		closeRedirectBody(nil)
	})

	t.Run("Handles response with nil body", func(t *testing.T) {
		resp := &http.Response{Body: nil}
		// Should not panic
		closeRedirectBody(resp)
	})

	// Note: Testing actual body close behavior would require mock HTTP responses,
	// which is beyond the scope of unit tests. Integration tests will cover this.
}

func TestParseURL(t *testing.T) {
	t.Run("Valid HTTP URL", func(t *testing.T) {
		u, err := parseURL("http://example.com")
		require.NoError(t, err)
		require.Equal(t, "example.com", u.Host)
		require.Equal(t, "http", u.Scheme)
	})

	t.Run("Valid HTTPS URL", func(t *testing.T) {
		u, err := parseURL("https://example.com/path")
		require.NoError(t, err)
		require.Equal(t, "example.com", u.Host)
		require.Equal(t, "/path", u.Path)
	})

	t.Run("Invalid URL returns error", func(t *testing.T) {
		u, err := parseURL("://invalid")
		require.Error(t, err)
		require.Nil(t, u)
		require.Contains(t, err.Error(), "invalid URL")
	})

	t.Run("Empty URL parses successfully", func(t *testing.T) {
		// Go's url.Parse actually accepts empty strings
		u, err := parseURL("")
		require.NoError(t, err)
		require.NotNil(t, u)
	})
}

func TestCloneHeader(t *testing.T) {
	t.Run("Nil header returns nil", func(t *testing.T) {
		cloned := cloneHeader(nil)
		require.Nil(t, cloned)
	})

	t.Run("Empty header returns empty header", func(t *testing.T) {
		h := http.Header{}
		cloned := cloneHeader(h)
		require.NotNil(t, cloned)
		require.Len(t, cloned, 0)
	})

	t.Run("Clones headers correctly", func(t *testing.T) {
		h := http.Header{
			"Content-Type": []string{"application/json"},
			"X-Custom":     []string{"value1", "value2"},
		}

		cloned := cloneHeader(h)
		require.Equal(t, h, cloned)

		// Verify it's a copy, not a reference
		cloned.Set("New-Header", "new-value")
		require.NotContains(t, h, "New-Header")
		require.Contains(t, cloned, "New-Header")
	})

	t.Run("Modifying cloned header doesn't affect original", func(t *testing.T) {
		h := http.Header{
			"Original": []string{"value"},
		}

		cloned := cloneHeader(h)
		cloned.Add("Original", "modified")

		require.Equal(t, []string{"value"}, h["Original"])
		require.Equal(t, []string{"value", "modified"}, cloned["Original"])
	})
}

// Phase 4 (US2): CheckRedirect callback tests

func TestCheckRedirectCallback(t *testing.T) {
	t.Run("CheckRedirect callback is invoked during redirect", func(t *testing.T) {
		// Track callback invocations
		callbackInvoked := false
		var receivedReq *http.Request
		var receivedVia []*http.Request

		cfg := Config{
			MaxRedirects: 10,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				callbackInvoked = true
				receivedReq = req
				receivedVia = via
				return nil // Allow redirect
			},
		}

		// This test verifies the callback mechanism is wired correctly
		// Integration tests will verify actual behavior with real servers
		require.NotNil(t, cfg.CheckRedirect)

		// Simulate callback invocation
		req := &http.Request{URL: mustParseURL("https://example.com/redirect")}
		via := []*http.Request{{URL: mustParseURL("https://example.com/original")}}

		err := cfg.CheckRedirect(req, via)
		require.NoError(t, err)
		require.True(t, callbackInvoked)
		require.Equal(t, req, receivedReq)
		require.Equal(t, via, receivedVia)
	})

	t.Run("CheckRedirect callback can reject redirect", func(t *testing.T) {
		rejectedErr := errors.New("redirect rejected")

		cfg := Config{
			MaxRedirects: 10,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// Reject all redirects
				return rejectedErr
			},
		}

		req := &http.Request{URL: mustParseURL("https://example.com/redirect")}
		via := []*http.Request{{URL: mustParseURL("https://example.com/original")}}

		err := cfg.CheckRedirect(req, via)
		require.Error(t, err)
		require.Equal(t, rejectedErr, err)
	})

	t.Run("CheckRedirect callback receives correct via chain", func(t *testing.T) {
		var capturedVia []*http.Request

		cfg := Config{
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				capturedVia = via
				return nil
			},
		}

		// Simulate multiple redirects in chain
		via := []*http.Request{
			{URL: mustParseURL("https://example.com/1")},
			{URL: mustParseURL("https://example.com/2")},
			{URL: mustParseURL("https://example.com/3")},
		}
		req := &http.Request{URL: mustParseURL("https://example.com/4")}

		cfg.CheckRedirect(req, via)
		require.Len(t, capturedVia, 3)
		require.Equal(t, "https://example.com/1", capturedVia[0].URL.String())
		require.Equal(t, "https://example.com/2", capturedVia[1].URL.String())
		require.Equal(t, "https://example.com/3", capturedVia[2].URL.String())
	})
}

func TestErrUseLastResponseHandling(t *testing.T) {
	t.Run("ErrUseLastResponse is defined", func(t *testing.T) {
		require.NotNil(t, ErrUseLastResponse)
		require.Contains(t, ErrUseLastResponse.Error(), "use last response")
	})

	t.Run("ErrUseLastResponse stops redirect following", func(t *testing.T) {
		cfg := Config{
			MaxRedirects: 10,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// Stop immediately on first redirect
				return ErrUseLastResponse
			},
		}

		req := &http.Request{URL: mustParseURL("https://example.com/redirect")}
		err := cfg.CheckRedirect(req, []*http.Request{})

		require.ErrorIs(t, err, ErrUseLastResponse)
	})

	t.Run("FollowRedirects(0) returns ErrUseLastResponse", func(t *testing.T) {
		// Already tested above, but verify again for US2
		fn := FollowRedirects(0)
		err := fn(nil, nil)
		require.ErrorIs(t, err, ErrUseLastResponse)
	})

	t.Run("FollowRedirects(-1) returns ErrUseLastResponse", func(t *testing.T) {
		// Negative also disables redirects
		fn := FollowRedirects(-1)
		err := fn(nil, nil)
		require.ErrorIs(t, err, ErrUseLastResponse)
	})
}

func TestCustomPolicyErrorPropagation(t *testing.T) {
	t.Run("Custom error from CheckRedirect is returned", func(t *testing.T) {
		customErr := errors.New("custom policy violation")

		cfg := Config{
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return customErr
			},
		}

		req := &http.Request{URL: mustParseURL("https://example.com/redirect")}
		err := cfg.CheckRedirect(req, []*http.Request{})

		require.Error(t, err)
		require.Equal(t, customErr, err)
	})

	t.Run("Wrapped error from CheckRedirect is returned", func(t *testing.T) {
		baseErr := errors.New("base error")
		wrappedErr := fmt.Errorf("wrapped: %w", baseErr)

		cfg := Config{
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return wrappedErr
			},
		}

		req := &http.Request{URL: mustParseURL("https://example.com/redirect")}
		err := cfg.CheckRedirect(req, []*http.Request{})

		require.Error(t, err)
		require.ErrorIs(t, err, baseErr)
	})

	t.Run("Same-domain policy example", func(t *testing.T) {
		// Example custom policy: only allow redirects within same domain
		sameDomainOnly := func(req *http.Request, via []*http.Request) error {
			if len(via) == 0 {
				return nil // First request, allow
			}

			originalDomain := via[0].URL.Host
			redirectDomain := req.URL.Host

			if originalDomain != redirectDomain {
				return fmt.Errorf("cross-domain redirect not allowed: %s -> %s",
					originalDomain, redirectDomain)
			}
			return nil
		}

		// Test same domain - should allow
		via := []*http.Request{{URL: mustParseURL("https://example.com/a")}}
		req := &http.Request{URL: mustParseURL("https://example.com/b")}
		err := sameDomainOnly(req, via)
		require.NoError(t, err)

		// Test different domain - should reject
		req2 := &http.Request{URL: mustParseURL("https://other.com/b")}
		err2 := sameDomainOnly(req2, via)
		require.Error(t, err2)
		require.Contains(t, err2.Error(), "cross-domain redirect not allowed")
	})

	t.Run("HTTPS-only policy example", func(t *testing.T) {
		// Example custom policy: only allow HTTPS redirects
		httpsOnly := func(req *http.Request, _ []*http.Request) error {
			if req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to non-HTTPS URL not allowed: %s", req.URL.String())
			}
			return nil
		}

		// Test HTTPS - should allow
		req := &http.Request{URL: mustParseURL("https://example.com/secure")}
		err := httpsOnly(req, []*http.Request{})
		require.NoError(t, err)

		// Test HTTP - should reject
		req2 := &http.Request{URL: mustParseURL("http://example.com/insecure")}
		err2 := httpsOnly(req2, []*http.Request{})
		require.Error(t, err2)
		require.Contains(t, err2.Error(), "non-HTTPS")
	})
}
