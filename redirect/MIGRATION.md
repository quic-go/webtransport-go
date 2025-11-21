# Migration Guide: Using the redirect Package

## Important Changes

**As of this version, the base `webtransport.Dialer` no longer supports automatic redirect following.**

- The `CheckRedirect` field has been **removed** from `webtransport.Dialer`
- The base `Dialer.Dial()` performs a **single dial attempt** and returns redirect responses (3xx) directly to the caller
- All redirect-related types (`TooManyRedirectsError`, `RedirectLoopError`, `ErrUseLastResponse`, `FollowRedirects()`) have been **moved** to the `retry` package
- For automatic redirect following, you **must** use the `redirect.Dialer` package

**Note:** The `retry` package name is historical. Despite the name, it only handles
automatic redirect following, not retry-on-failure. For retry logic, implement it
in your application or use a separate retry library.

This change provides cleaner separation of concerns:
- **Base Dialer**: Simple, single-attempt connection (no redirect exports)
- **Retry Dialer**: Automatic redirect following only

## Overview

The `retry` package provides automatic redirect following for WebTransport connections through:
- Automatic redirect following with configurable limits
- Custom redirect policies via callbacks
- Redirect loop detection
- Comprehensive error handling

## Quick Start

### Before (using base package with redirect support)

```go
import "github.com/quic-go/webtransport-go"

d := &webtransport.Dialer{
    CheckRedirect: webtransport.FollowRedirects(10),
}

resp, sess, err := d.Dial(ctx, "https://example.com/webtransport", nil)
```

### After (using retry package)

```go
import (
    webtransport "github.com/quic-go/webtransport-go"
    "github.com/quic-go/webtransport-go/redirect"
)

baseDialer := &webtransport.Dialer{}
cfg := redirect.DefaultConfig()
d, err := redirect.NewDialer(baseDialer, cfg)
if err != nil {
    log.Fatal(err)
}

resp, sess, err := d.Dial(ctx, "https://example.com/webtransport", nil)
```

## Configuration

### Basic Redirect Configuration

```go
cfg := redirect.Config{
    MaxRedirects: 10,  // Follow up to 10 redirects
}

d, err := redirect.NewDialer(&webtransport.Dialer{}, cfg)
```

### Custom Redirect Policy

```go
cfg := redirect.Config{
    MaxRedirects: 10,
    CheckRedirect: func(req *http.Request, via []*http.Request) error {
        // Only allow same-domain redirects
        if len(via) > 0 && req.URL.Host != via[0].URL.Host {
            return errors.New("cross-domain redirect not allowed")
        }
        return nil
    },
}
```

### Disable Redirects

```go
cfg := redirect.Config{
    MaxRedirects: 0,  // No redirects
}
```

## Configuration Defaults

When creating a dialer with `redirect.DefaultConfig()`:

```go
Config{
    MaxRedirects:  10,
    CheckRedirect: nil,  // Follow all redirects up to MaxRedirects
}
```

## Redirect Handling

### Automatic Redirect Following

The retry package automatically follows HTTP 3xx redirects:

- **301** (Moved Permanently)
- **302** (Found)
- **303** (See Other)
- **307** (Temporary Redirect)
- **308** (Permanent Redirect)

### Redirect Loop Detection

The package automatically detects and prevents redirect loops:

```go
cfg := redirect.DefaultConfig()
d, err := redirect.NewDialer(&webtransport.Dialer{}, cfg)

resp, sess, err := d.Dial(ctx, url, nil)
if err != nil {
    var loopErr *redirect.RedirectLoopError
    if errors.As(err, &loopErr) {
        fmt.Printf("Redirect loop detected at %s\n", loopErr.URL)
    }
}
```

### Custom Redirect Policies

Use `CheckRedirect` to implement custom redirect logic:

```go
cfg := redirect.Config{
    CheckRedirect: func(req *http.Request, via []*http.Request) error {
        // Reject HTTPS → HTTP downgrades
        if len(via) > 0 && via[0].URL.Scheme == "https" && req.URL.Scheme == "http" {
            return errors.New("HTTPS to HTTP downgrade not allowed")
        }

        // Stop following redirects at this point
        if someCondition {
            return redirect.ErrUseLastResponse
        }

        return nil  // Follow the redirect
    },
}
```

### Compatibility Helper

For code previously using `webtransport.FollowRedirects()`:

```go
// Old approach (still works for compatibility)
cfg := redirect.Config{
    CheckRedirect: redirect.FollowRedirects(10),
}

// Recommended new approach
cfg := redirect.Config{
    MaxRedirects: 10,
}
```

## Error Handling

The retry package provides specific error types for different redirect scenarios:

```go
resp, sess, err := d.Dial(ctx, url, nil)
if err != nil {
    // Check for redirect limit exceeded
    var tooManyErr *redirect.TooManyRedirectsError
    if errors.As(err, &tooManyErr) {
        log.Printf("Stopped after %d redirects (limit: %d)",
            len(tooManyErr.Via), tooManyErr.Limit)
        return
    }

    // Check for redirect loop
    var loopErr *redirect.RedirectLoopError
    if errors.As(err, &loopErr) {
        log.Printf("Redirect loop detected at %s (after %d redirects)",
            loopErr.URL, len(loopErr.Via))
        return
    }

    // Check for context cancellation
    if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
        log.Printf("Operation canceled: %v", err)
        return
    }

    // Other errors from underlying dialer
    log.Printf("Dial failed: %v", err)
    return
}

// Success - use session
defer sess.CloseWithError(0, "")
```

## Complete Example

```go
package main

import (
    "context"
    "errors"
    "fmt"
    "log"
    "net/http"
    "time"

    webtransport "github.com/quic-go/webtransport-go"
    "github.com/quic-go/webtransport-go/redirect"
)

func main() {
    // Create base dialer
    baseDialer := &webtransport.Dialer{}

    // Configure redirect handling
    cfg := redirect.Config{
        MaxRedirects: 10,
        CheckRedirect: func(req *http.Request, via []*http.Request) error {
            // Only allow same-domain redirects
            if len(via) > 0 && req.URL.Host != via[0].URL.Host {
                return errors.New("cross-domain redirect not allowed")
            }
            return nil
        },
    }

    // Create retry dialer
    d, err := redirect.NewDialer(baseDialer, cfg)
    if err != nil {
        log.Fatalf("Failed to create dialer: %v", err)
    }

    // Dial with timeout
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    resp, sess, err := d.Dial(ctx, "https://example.com/webtransport", nil)
    if err != nil {
        handleError(err)
        return
    }
    defer sess.CloseWithError(0, "")

    fmt.Printf("Connected! Status: %d\n", resp.StatusCode)

    // Use session...
}

func handleError(err error) {
    var tooManyErr *redirect.TooManyRedirectsError
    if errors.As(err, &tooManyErr) {
        log.Printf("Too many redirects: %d (limit: %d)",
            len(tooManyErr.Via), tooManyErr.Limit)
        return
    }

    var loopErr *redirect.RedirectLoopError
    if errors.As(err, &loopErr) {
        log.Printf("Redirect loop at %s", loopErr.URL)
        return
    }

    if errors.Is(err, context.DeadlineExceeded) {
        log.Printf("Connection timeout")
        return
    }

    log.Printf("Dial failed: %v", err)
}
```

## Testing Considerations

### Shorter Redirect Limits in Tests

Use lower redirect limits in tests for faster failure detection:

```go
cfg := redirect.Config{
    MaxRedirects: 5,  // Fewer redirects for faster tests
}
```

### Mock Servers

For testing redirect behavior, use test servers:

```go
func TestRedirectHandling(t *testing.T) {
    // Set up test server that returns redirects
    // (requires HTTP/3 + WebTransport support)

    cfg := redirect.Config{
        MaxRedirects: 2,
    }
    d, err := redirect.NewDialer(&webtransport.Dialer{}, cfg)
    require.NoError(t, err)

    // Test redirect following
    // ...
}
```

## Performance Considerations

The retry package adds minimal overhead:
- Redirect following: < 1% overhead for non-redirect responses
- No retry logic overhead (retry-on-failure not supported)

## Common Patterns

### Allow Only HTTPS Redirects

```go
cfg := redirect.Config{
    CheckRedirect: func(req *http.Request, via []*http.Request) error {
        if req.URL.Scheme != "https" {
            return fmt.Errorf("only HTTPS redirects allowed, got: %s", req.URL.Scheme)
        }
        return nil
    },
}
```

### Limit Redirect Chain Depth

```go
cfg := redirect.Config{
    MaxRedirects: 3,  // Maximum 3 redirects
}
```

### Stop at First Redirect

```go
cfg := redirect.Config{
    CheckRedirect: func(req *http.Request, via []*http.Request) error {
        // Always stop and use the redirect response
        return redirect.ErrUseLastResponse
    },
}
```

## Troubleshooting

### "Dialer must not be nil" Error

Ensure you pass a valid base dialer:

```go
// Wrong
d, err := redirect.NewDialer(nil, cfg)

// Correct
d, err := redirect.NewDialer(&webtransport.Dialer{}, cfg)
```

### "MaxRedirects must be >= -1" Error

Use valid redirect limits:

```go
// Wrong
cfg := redirect.Config{MaxRedirects: -5}

// Correct
cfg := redirect.Config{MaxRedirects: 10}   // Specific limit
cfg := redirect.Config{MaxRedirects: -1}   // Unlimited (not recommended)
cfg := redirect.Config{MaxRedirects: 0}    // No redirects
```

### Redirect Loops

If you encounter redirect loops, check your server configuration or use `CheckRedirect` to break the loop:

```go
visited := make(map[string]bool)
cfg := redirect.Config{
    CheckRedirect: func(req *http.Request, via []*http.Request) error {
        url := req.URL.String()
        if visited[url] {
            return fmt.Errorf("visited %s twice", url)
        }
        visited[url] = true
        return nil
    },
}
```

Note: The package already detects loops automatically - this is just for custom handling.

## Additional Resources

- [WebTransport Protocol](https://www.w3.org/TR/webtransport/)
- [HTTP Redirects (RFC 7231)](https://tools.ietf.org/html/rfc7231#section-6.4)
- [Go net/http Redirects](https://pkg.go.dev/net/http#Client)

## Migration Checklist

- [ ] Replace `webtransport.Dialer{CheckRedirect: ...}` with `redirect.NewDialer()`
- [ ] Update imports to include `"github.com/quic-go/webtransport-go/redirect"`
- [ ] Replace `webtransport.FollowRedirects()` with `redirect.Config{MaxRedirects: N}`
- [ ] Update error handling to use retry error types
- [ ] Test redirect behavior with your servers
- [ ] Remove any custom retry-on-failure logic if relying on package name
