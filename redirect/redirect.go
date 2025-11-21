package redirect

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// FollowRedirects returns a CheckRedirect callback that follows up to
// maxRedirects redirects.
//
// This is a convenience helper equivalent to setting Config.MaxRedirects
// and leaving CheckRedirect nil, but provided for compatibility with code
// that previously used webtransport.FollowRedirects.
//
// Parameters:
//   - maxRedirects: Maximum number of redirects to follow
//   - If <= 0: No redirects are followed (returns first redirect response)
//   - If > 0: Follows up to maxRedirects redirects
//
// Returns a function that can be assigned to Config.CheckRedirect.
//
// Example (legacy compatibility):
//
//	cfg := redirect.Config{
//	    CheckRedirect: redirect.FollowRedirects(10),
//	}
//
// Equivalent modern approach:
//
//	cfg := redirect.Config{
//	    MaxRedirects: 10,
//	}
//
// NOTE: This function is provided for backward compatibility with code
// that used webtransport.FollowRedirects from the core package. New code
// should use Config.MaxRedirects directly.
func FollowRedirects(maxRedirects int) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		// Special case: maxRedirects == 0 means no redirects
		if maxRedirects <= 0 {
			return ErrUseLastResponse
		}
		// If we've reached the limit, stop
		if len(via) >= maxRedirects {
			return &TooManyRedirectsError{
				Limit: maxRedirects,
				Via:   via,
			}
		}
		return nil // Follow the redirect
	}
}

// isRedirectStatus returns true if the HTTP status code represents a redirect (3xx).
// Checks for: 301 (Moved Permanently), 302 (Found), 303 (See Other),
// 307 (Temporary Redirect), 308 (Permanent Redirect).
func isRedirectStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusMovedPermanently, // 301
		http.StatusFound,             // 302
		http.StatusSeeOther,          // 303
		http.StatusTemporaryRedirect, // 307
		http.StatusPermanentRedirect: // 308
		return true
	}
	return false
}

// extractLocation extracts and validates the Location header from a redirect response.
// Returns an error if the Location header is missing or empty.
func extractLocation(resp *http.Response) (string, error) {
	location := resp.Header.Get("Location")
	if location == "" {
		return "", errors.New("redirect: redirect response missing Location header")
	}
	return location, nil
}

// resolveRedirectLocation resolves a redirect Location header against the base URL
// per RFC 3986 Section 5.3 (Component Recomposition). Returns the resolved absolute URL
// or an error if the location is empty or malformed.
//
// RFC 3986 defines the algorithm for resolving relative references against a base URI,
// handling partial URI references (missing scheme, authority, path, etc.) by combining
// them with the base URI's components.
func resolveRedirectLocation(base *url.URL, location string) (*url.URL, error) {
	if location == "" {
		return nil, errors.New("redirect: redirect location header is empty")
	}

	ref, err := url.Parse(location)
	if err != nil {
		return nil, fmt.Errorf("redirect: malformed redirect location: %w", err)
	}

	return base.ResolveReference(ref), nil
}

// closeRedirectBody safely closes a redirect response body, discarding any unread content.
// This is necessary before following a redirect to prevent resource leaks.
func closeRedirectBody(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		// Discard any remaining body content before closing
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}

// checkRedirectLoop detects if a URL has already been visited in the redirect chain.
// Returns RedirectLoopError if a loop is detected.
func checkRedirectLoop(urlStr string, via []*http.Request) error {
	// Check if this URL has been visited before
	for _, req := range via {
		if req.URL.String() == urlStr {
			return &RedirectLoopError{
				URL: urlStr,
				Via: via,
			}
		}
	}
	return nil
}
