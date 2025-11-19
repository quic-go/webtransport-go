// Package webtransport provides HTTP redirect handling for WebTransport connections.
//
// Redirect handling in WebTransport differs from net/http.Client to comply with the
// WebTransport RFC (draft-ietf-webtrans-http3). The RFC requires application-level
// control over redirect following because optimistic data (streams, datagrams) may have
// already been sent for a session before the redirect response is received.
//
// By default, redirect responses (3xx status codes) are returned to the application
// without automatic following. Applications can opt-in to redirect following by setting
// the Dialer.CheckRedirect callback. The CheckRedirect function allows fine-grained
// control over which redirects to follow, with access to the redirect chain history.
//
// For convenience, the FollowRedirects function provides a standard redirect following
// policy similar to net/http.Client, with configurable limits on the number of redirects.
//
// Relative redirect locations are resolved against the request URL per RFC 3986,
// and the redirect loop detection prevents infinite redirect cycles.
//
// Key differences from net/http.Client:
//   - CheckRedirect must be explicitly set to follow redirects (nil = no following)
//   - Applications have full control over the redirect policy
//   - ErrUseLastResponse allows returning the redirect response without following
//
// See draft-ietf-webtrans-http3 sections 3.1-3.3 for WebTransport redirect requirements.
package webtransport

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// ErrUseLastResponse can be returned by CheckRedirect to control how redirects
// are processed. If returned, the most recent response is returned with its body
// unclosed, along with a nil error. This matches the semantics of
// net/http.ErrUseLastResponse.
var ErrUseLastResponse = errors.New("webtransport: use last response")

// TooManyRedirectsError is returned when a redirect limit is exceeded.
type TooManyRedirectsError struct {
	Limit int
	Via   []*http.Request
}

func (e *TooManyRedirectsError) Error() string {
	return fmt.Sprintf("webtransport: stopped after %d redirects (limit: %d)", len(e.Via), e.Limit)
}

// RedirectLoopError is returned when a redirect loop is detected (same URL appears twice in the redirect chain).
type RedirectLoopError struct {
	URL string
	Via []*http.Request
}

// Error implements the error interface.
func (e *RedirectLoopError) Error() string {
	return fmt.Sprintf("webtransport: redirect loop detected at %s (after %d redirects)", e.URL, len(e.Via))
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
		return "", errors.New("webtransport: redirect response missing Location header")
	}
	return location, nil
}

// FollowRedirects returns a CheckRedirect function that follows redirects
// automatically up to a maximum number of redirects. A maxRedirects value
// of 0 means unlimited redirects (not recommended).
//
// This is a convenience function for the common case of wanting automatic
// redirect following similar to net/http.Client. The default limit of 10
// matches net/http.Client behavior.
//
// Example:
//
//	d := &webtransport.Dialer{
//	    CheckRedirect: webtransport.FollowRedirects(10),
//	}
func FollowRedirects(maxRedirects int) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		// Special case: maxRedirects == 0 means unlimited
		if maxRedirects > 0 && len(via) >= maxRedirects {
			return &TooManyRedirectsError{
				Limit: maxRedirects,
				Via:   via,
			}
		}
		return nil // Follow the redirect
	}
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
		return nil, errors.New("webtransport: redirect location header is empty")
	}

	ref, err := url.Parse(location)
	if err != nil {
		return nil, fmt.Errorf("webtransport: malformed redirect location: %w", err)
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
