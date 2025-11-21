package redirect

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	webtransport "github.com/quic-go/webtransport-go"
)

// Dialer wraps a webtransport.Dialer to add automatic redirect following.
//
// Dialer is safe for concurrent use by multiple goroutines. All configuration
// is immutable after creation via NewDialer.
//
// Each call to Dial maintains independent redirect state, so concurrent Dial
// operations do not interfere with each other.
type Dialer struct {
	// dialer is the wrapped webtransport.Dialer instance
	// Immutable after creation
	dialer *webtransport.Dialer

	// config holds redirect configuration
	// Immutable after creation (value type)
	config Config
}

// NewDialer creates a new Dialer that wraps the given webtransport.Dialer
// with the specified configuration.
//
// The dialer parameter must not be nil. The config parameter is validated
// and any zero-valued optional fields are set to defaults.
//
// Returns an error if:
//   - dialer is nil
//   - config.MaxRedirects < -1
//
// The returned Dialer holds an immutable reference to the provided dialer
// and a copy of the config. Subsequent changes to the config parameter do
// not affect the returned Dialer.
func NewDialer(dialer *webtransport.Dialer, config Config) (*Dialer, error) {
	if dialer == nil {
		return nil, errors.New("redirect: dialer must not be nil")
	}

	// Validate configuration
	if err := config.validate(); err != nil {
		return nil, err
	}

	// Apply defaults
	config = config.applyDefaults()

	return &Dialer{
		dialer: dialer,
		config: config,
	}, nil
}

// Dial establishes a WebTransport session to the given URL, following
// redirects according to the Dialer's configuration.
//
// The ctx parameter controls cancellation and deadlines. If ctx is canceled
// or expires, Dial returns immediately with ctx.Err() wrapped in an error.
//
// Redirect handling:
//   - HTTP 3xx redirect responses are followed up to MaxRedirects times
//   - CheckRedirect callback is invoked before each redirect (if configured)
//   - Redirect loops are detected by tracking visited URLs
//   - Returns TooManyRedirectsError if limit exceeded
//   - Returns RedirectLoopError if circular redirect detected
//   - CheckRedirect can return ErrUseLastResponse to stop and return the redirect response
//
// Returns:
//   - *webtransport.Session: The established session (non-nil on success)
//   - *http.Response: The final HTTP response (non-nil on success or redirect stop)
//   - error: nil on success, otherwise one of:
//   - context.DeadlineExceeded or context.Canceled (wrapped)
//   - *TooManyRedirectsError
//   - *RedirectLoopError
//   - Errors from the underlying dialer
//   - Errors from CheckRedirect callback
//
// The caller is responsible for closing the returned session if err is nil.
// The returned http.Response body is always closed by Dial for redirect responses,
// but the caller must close it for the final response.
func (d *Dialer) Dial(ctx context.Context, urlStr string, reqHdr http.Header) (*http.Response, *webtransport.Session, error) {
	// Via chain for redirect tracking
	var via []*http.Request

	// Redirect following loop
	for {
		// Check context before each attempt
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}

		// Call the underlying dialer
		resp, sess, err := d.dialer.Dial(ctx, urlStr, reqHdr)
		if err != nil {
			return resp, sess, err
		}

		// Check if this is a redirect response
		if !isRedirectStatus(resp.StatusCode) {
			// Not a redirect, return success
			return resp, sess, nil
		}

		// Handle redirect
		location, err := extractLocation(resp)
		if err != nil {
			closeRedirectBody(resp)
			return resp, nil, err
		}

		// Parse current URL for resolution
		currentURL, err := parseURL(urlStr)
		if err != nil {
			closeRedirectBody(resp)
			return resp, nil, err
		}

		// Resolve redirect URL
		redirectURL, err := resolveRedirectLocation(currentURL, location)
		if err != nil {
			closeRedirectBody(resp)
			return resp, nil, err
		}

		redirectURLStr := redirectURL.String()

		// Check for redirect loop
		if err := checkRedirectLoop(redirectURLStr, via); err != nil {
			closeRedirectBody(resp)
			return resp, nil, err
		}

		// Check redirect limit
		if d.config.MaxRedirects >= 0 && len(via) >= d.config.MaxRedirects {
			closeRedirectBody(resp)
			return resp, nil, &TooManyRedirectsError{
				Limit: d.config.MaxRedirects,
				Via:   via,
			}
		}

		// Create request for CheckRedirect callback
		redirectReq := &http.Request{
			Method: http.MethodConnect,
			Header: cloneHeader(reqHdr),
			Proto:  "webtransport",
			Host:   redirectURL.Host,
			URL:    redirectURL,
		}
		redirectReq = redirectReq.WithContext(ctx)

		// Invoke CheckRedirect if configured
		if d.config.CheckRedirect != nil {
			// Make a copy of via to prevent modification
			viaCopy := make([]*http.Request, len(via))
			copy(viaCopy, via)
			viaCopy = append(viaCopy, &http.Request{
				Method: http.MethodConnect,
				Header: cloneHeader(reqHdr),
				Proto:  "webtransport",
				Host:   currentURL.Host,
				URL:    currentURL,
			})

			err = d.config.CheckRedirect(redirectReq, viaCopy)
			if err != nil {
				if errors.Is(err, ErrUseLastResponse) {
					// Return redirect response without closing body
					return resp, nil, nil
				}
				// Other error, close body and return
				closeRedirectBody(resp)
				return resp, nil, err
			}
		}

		// Follow the redirect
		closeRedirectBody(resp)

		// Add current request to via chain
		via = append(via, &http.Request{
			Method: http.MethodConnect,
			Header: cloneHeader(reqHdr),
			Proto:  "webtransport",
			Host:   currentURL.Host,
			URL:    currentURL,
		})

		// Update URL for next iteration
		urlStr = redirectURLStr
	}
}

// parseURL is a helper to parse a URL string.
func parseURL(urlStr string) (*url.URL, error) {
	u, err := url.Parse(urlStr)
	if err != nil {
		return nil, fmt.Errorf("redirect: invalid URL: %w", err)
	}
	return u, nil
}

// cloneHeader creates a shallow copy of an http.Header.
func cloneHeader(h http.Header) http.Header {
	if h == nil {
		return nil
	}
	h2 := make(http.Header, len(h))
	for k, vv := range h {
		vv2 := make([]string, len(vv))
		copy(vv2, vv)
		h2[k] = vv2
	}
	return h2
}
