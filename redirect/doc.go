// Package redirect provides automatic redirect following for WebTransport connections.
//
// The redirect package wraps webtransport.Dialer to add:
//   - Configurable redirect following with loop detection
//   - Custom redirect policies via CheckRedirect callbacks
//
// # Basic Usage
//
// Create a Dialer with default configuration:
//
//	baseDialer := &webtransport.Dialer{}
//	cfg := redirect.DefaultConfig()
//	d, err := redirect.NewDialer(baseDialer, cfg)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	resp, sess, err := d.Dial(ctx, "https://example.com/webtransport", nil)
//
// # Redirect Handling
//
// By default, the Dialer follows up to 10 redirects and detects redirect loops:
//
//	cfg := redirect.DefaultConfig()
//	cfg.MaxRedirects = 5
//	cfg.CheckRedirect = func(req *http.Request, via []*http.Request) error {
//	    // Custom redirect policy
//	    if req.URL.Host != via[0].URL.Host {
//	        return errors.New("cross-host redirects not allowed")
//	    }
//	    return nil
//	}
//
// # Error Handling
//
// The package provides specific error types for redirect handling:
//
//	resp, sess, err := d.Dial(ctx, url, nil)
//	if err != nil {
//	    var tooManyErr *redirect.TooManyRedirectsError
//	    if errors.As(err, &tooManyErr) {
//	        // Handle redirect limit exceeded
//	    }
//
//	    var loopErr *redirect.RedirectLoopError
//	    if errors.As(err, &loopErr) {
//	        // Handle redirect loop
//	    }
//	}
//
// # Thread Safety
//
// The Dialer is safe for concurrent use. Configuration is immutable after
// creation, and each Dial() call maintains independent redirect state.
//
// # Migration from Core Package
//
// If you were using the core package's redirect functionality:
//
//	// Old (no longer supported):
//	d := &webtransport.Dialer{
//	    CheckRedirect: webtransport.FollowRedirects(10),
//	}
//
//	// New:
//	cfg := redirect.Config{
//	    MaxRedirects: 10,
//	}
//	d, err := redirect.NewDialer(&webtransport.Dialer{}, cfg)
package redirect
