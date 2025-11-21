package redirect

import (
	"errors"
	"fmt"
	"net/http"
)

// ErrUseLastResponse can be returned by CheckRedirect to control how redirects
// are processed. If returned, the most recent response is returned with its body
// unclosed, along with a nil error. This matches the semantics of
// net/http.ErrUseLastResponse.
var ErrUseLastResponse = errors.New("redirect: use last response")

// TooManyRedirectsError is returned when a redirect limit is exceeded.
type TooManyRedirectsError struct {
	// Limit is the configured maximum redirects
	Limit int
	// Via contains all requests in the redirect chain
	// via[0] is the initial request, via[len-1] is the request that triggered the error
	Via []*http.Request
}

// Error implements the error interface.
func (e *TooManyRedirectsError) Error() string {
	return fmt.Sprintf("redirect: stopped after %d redirects (limit: %d)", len(e.Via), e.Limit)
}

// RedirectLoopError is returned when a circular redirect is detected.
type RedirectLoopError struct {
	// URL is the duplicate URL that was visited
	URL string
	// Via contains all requests in the redirect chain up to the duplicate
	Via []*http.Request
}

// Error implements the error interface.
func (e *RedirectLoopError) Error() string {
	return fmt.Sprintf("redirect: redirect loop detected at %s (after %d redirects)", e.URL, len(e.Via))
}
