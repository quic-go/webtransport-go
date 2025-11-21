package redirect

import (
	"errors"
	"net/http"
)

// Config holds configuration for redirect behavior.
//
// All fields are optional. Zero values trigger default behavior:
//   - MaxRedirects: 10
//   - CheckRedirect: nil (follow all redirects up to MaxRedirects)
//
// Config is a value type. Changes to a Config after passing it to NewDialer
// do not affect the created Dialer.
type Config struct {
	// MaxRedirects limits the number of HTTP redirects to follow.
	//
	// If zero, uses default (10).
	// If positive, redirects are followed up to this limit.
	// If -1, redirects are followed without limit (not recommended).
	//
	// Redirect limits are checked before invoking CheckRedirect.
	MaxRedirects int

	// CheckRedirect is called before following each HTTP redirect.
	//
	// The req parameter is the upcoming redirect request.
	// The via parameter contains all previous requests in the chain,
	// with via[0] being the original request.
	//
	// If CheckRedirect returns an error, the redirect is not followed:
	//   - ErrUseLastResponse: Stop and return the redirect response
	//   - Any other error: Abort Dial with that error
	//
	// If CheckRedirect is nil, all redirects up to MaxRedirects are followed.
	//
	// CheckRedirect is invoked with redirect limit already checked, so
	// len(via) is guaranteed to be < MaxRedirects.
	CheckRedirect func(req *http.Request, via []*http.Request) error
}

// DefaultConfig returns a Config with recommended default values:
//   - MaxRedirects: 10
//   - CheckRedirect: nil
//
// Callers can modify the returned Config before passing to NewDialer.
func DefaultConfig() Config {
	return Config{
		MaxRedirects:  10,
		CheckRedirect: nil,
	}
}

// applyDefaults returns a copy of the config with defaults applied for zero values.
func (c Config) applyDefaults() Config {
	cfg := c

	if cfg.MaxRedirects == 0 {
		cfg.MaxRedirects = 10
	}

	return cfg
}

// validate checks if the configuration is valid.
func (c Config) validate() error {
	if c.MaxRedirects < -1 {
		return errors.New("redirect: MaxRedirects must be >= -1")
	}
	return nil
}
