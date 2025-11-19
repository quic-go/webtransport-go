package webtransport

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

// Test helpers for creating mock redirect responses
func makeRedirectResponse(statusCode int, location string) *http.Response {
	resp := &http.Response{
		StatusCode: statusCode,
		Header:     make(http.Header),
	}
	if location != "" {
		resp.Header.Set("Location", location)
	}
	return resp
}

// TestIsRedirectStatus tests the isRedirectStatus helper function
func TestIsRedirectStatus(t *testing.T) {
	tests := []struct {
		name     string
		code     int
		expected bool
	}{
		// Redirect status codes
		{"301 Moved Permanently", http.StatusMovedPermanently, true},
		{"302 Found", http.StatusFound, true},
		{"303 See Other", http.StatusSeeOther, true},
		{"307 Temporary Redirect", http.StatusTemporaryRedirect, true},
		{"308 Permanent Redirect", http.StatusPermanentRedirect, true},
		// Non-redirect 3xx codes
		{"304 Not Modified", http.StatusNotModified, false},
		{"305 Use Proxy", http.StatusUseProxy, false},
		// Non-3xx codes
		{"200 OK", http.StatusOK, false},
		{"201 Created", http.StatusCreated, false},
		{"400 Bad Request", http.StatusBadRequest, false},
		{"404 Not Found", http.StatusNotFound, false},
		{"500 Internal Server Error", http.StatusInternalServerError, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isRedirectStatus(tt.code)
			require.Equal(t, tt.expected, result)
		})
	}
}

// TestExtractLocation_Valid tests extractLocation with valid Location headers
func TestExtractLocation_Valid(t *testing.T) {
	tests := []struct {
		name     string
		location string
	}{
		{"absolute URL", "https://example.com/redirect"},
		{"relative path", "/redirect"},
		{"relative with query", "/redirect?foo=bar"},
		{"relative with fragment", "/redirect#section"},
		{"absolute with port", "https://example.com:8443/redirect"},
		{"scheme-relative", "//example.com/redirect"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := makeRedirectResponse(http.StatusFound, tt.location)
			location, err := extractLocation(resp)
			require.NoError(t, err)
			require.Equal(t, tt.location, location)
		})
	}
}

// TestExtractLocation_Missing tests extractLocation with missing Location header
func TestExtractLocation_Missing(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusFound,
		Header:     make(http.Header),
		// No Location header set
	}
	location, err := extractLocation(resp)
	require.Error(t, err)
	require.Empty(t, location)
	require.Contains(t, err.Error(), "missing Location header")
}

// TestExtractLocation_Empty tests extractLocation with empty Location header
func TestExtractLocation_Empty(t *testing.T) {
	resp := makeRedirectResponse(http.StatusFound, "")
	// The makeRedirectResponse helper doesn't set Location if empty string passed
	// But let's explicitly set it to empty to test
	resp.Header.Set("Location", "")
	location, err := extractLocation(resp)
	require.Error(t, err)
	require.Empty(t, location)
	require.Contains(t, err.Error(), "missing Location header")
}

// TestFollowRedirects_Default tests FollowRedirects with default limit of 10
func TestFollowRedirects_Default(t *testing.T) {
	checkRedirect := FollowRedirects(10)

	// Via chain with 9 requests should be allowed (this would be the 10th redirect)
	via := make([]*http.Request, 9)
	for i := range via {
		via[i] = &http.Request{}
	}
	req := &http.Request{}
	err := checkRedirect(req, via)
	require.NoError(t, err)

	// Via chain with 10 requests should be blocked (this would be the 11th redirect)
	via = make([]*http.Request, 10)
	for i := range via {
		via[i] = &http.Request{}
	}
	err = checkRedirect(req, via)
	require.Error(t, err)
	var tooMany *TooManyRedirectsError
	require.ErrorAs(t, err, &tooMany)
	require.Equal(t, 10, tooMany.Limit)
	require.Equal(t, 10, len(tooMany.Via))
}

// TestFollowRedirects_CustomLimit tests FollowRedirects with custom limit
func TestFollowRedirects_CustomLimit(t *testing.T) {
	checkRedirect := FollowRedirects(3)

	// Via chain with 2 requests should be allowed
	via := make([]*http.Request, 2)
	req := &http.Request{}
	err := checkRedirect(req, via)
	require.NoError(t, err)

	// Via chain with 3 requests should be blocked
	via = make([]*http.Request, 3)
	err = checkRedirect(req, via)
	require.Error(t, err)
	var tooMany *TooManyRedirectsError
	require.ErrorAs(t, err, &tooMany)
	require.Equal(t, 3, tooMany.Limit)
}

// TestFollowRedirects_Unlimited tests maxRedirects=0 means unlimited
func TestFollowRedirects_Unlimited(t *testing.T) {
	checkRedirect := FollowRedirects(0)

	// Create a via chain with 100 requests
	via := make([]*http.Request, 100)
	for i := range via {
		via[i] = &http.Request{}
	}

	// Should never error with unlimited redirects
	req := &http.Request{}
	err := checkRedirect(req, via)
	require.NoError(t, err)
}

// TestResolveRedirectLocation_Absolute tests absolute URL resolution
func TestResolveRedirectLocation_Absolute(t *testing.T) {
	base, _ := url.Parse("https://example.com/path")
	location := "https://other.com/newpath"

	resolved, err := resolveRedirectLocation(base, location)
	require.NoError(t, err)
	require.Equal(t, "https://other.com/newpath", resolved.String())
}

// TestResolveRedirectLocation_Relative tests relative URL resolution
func TestResolveRedirectLocation_Relative(t *testing.T) {
	base, _ := url.Parse("https://example.com/api/v1/resource")

	tests := []struct {
		name     string
		location string
		expected string
	}{
		{"relative path", "/newpath", "https://example.com/newpath"},
		{"relative with query", "/newpath?foo=bar", "https://example.com/newpath?foo=bar"},
		{"relative with fragment", "/newpath#section", "https://example.com/newpath#section"},
		{"same directory", "other", "https://example.com/api/v1/other"},
		{"query only", "?query=value", "https://example.com/api/v1/resource?query=value"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolved, err := resolveRedirectLocation(base, tt.location)
			require.NoError(t, err)
			require.Equal(t, tt.expected, resolved.String())
		})
	}
}

// TestResolveRedirectLocation_RelativeWithDotDot tests ../path resolution
func TestResolveRedirectLocation_RelativeWithDotDot(t *testing.T) {
	base, _ := url.Parse("https://example.com/api/v1/resource")
	location := "../v2/resource"

	resolved, err := resolveRedirectLocation(base, location)
	require.NoError(t, err)
	require.Equal(t, "https://example.com/api/v2/resource", resolved.String())
}

// TestResolveRedirectLocation_Malformed tests malformed URL handling
func TestResolveRedirectLocation_Malformed(t *testing.T) {
	base, _ := url.Parse("https://example.com/path")

	tests := []struct {
		name     string
		location string
	}{
		{"empty", ""},
		{"invalid scheme", "ht!tp://example.com/path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := resolveRedirectLocation(base, tt.location)
			require.Error(t, err)
		})
	}
}
