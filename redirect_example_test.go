package webtransport

import (
	"fmt"
	"net/http"
)

// Example_followRedirects demonstrates using the FollowRedirects helper function
// to automatically follow redirects with a limit on the number of hops.
func Example_followRedirects() {
	// Create a dialer with automatic redirect following up to 10 redirects.
	// This is the most common use case and matches net/http.Client behavior.
	dialer := &Dialer{
		CheckRedirect: FollowRedirects(10),
	}

	// The dialer will now automatically follow redirects for any WebTransport
	// connection attempts. Each request in the redirect chain is validated
	// against the limit.
	_ = dialer

	// Example output message:
	fmt.Println("Dialer configured to follow up to 10 redirects")
	// Output: Dialer configured to follow up to 10 redirects
}

// Example_customCheckRedirect demonstrates implementing a custom redirect policy
// by providing a CheckRedirect callback function. This allows fine-grained control
// over which redirects to follow, such as restricting to the same host.
func Example_customCheckRedirect() {
	// Custom redirect policy that only follows redirects to the same host.
	// This is useful for security when you only trust redirects within your domain.
	customPolicy := func(req *http.Request, via []*http.Request) error {
		// Limit redirects to 5
		if len(via) > 5 {
			return &TooManyRedirectsError{
				Limit: 5,
				Via:   via,
			}
		}

		// Only follow redirects to the same host
		if len(via) > 0 {
			originalHost := via[0].URL.Host
			if req.URL.Host != originalHost {
				// Return ErrUseLastResponse to return the redirect without following it
				return ErrUseLastResponse
			}
		}

		return nil // Follow the redirect
	}

	dialer := &Dialer{
		CheckRedirect: customPolicy,
	}

	_ = dialer

	fmt.Println("Dialer configured with custom same-host redirect policy")
	// Output: Dialer configured with custom same-host redirect policy
}

// Example_noRedirectFollowing demonstrates disabling automatic redirect following
// by setting CheckRedirect to nil. In this mode, redirect responses (3xx status codes)
// are returned directly to the application, allowing full control over handling.
// This is the default behavior in WebTransport.
func Example_noRedirectFollowing() {
	// Create a dialer without automatic redirect following.
	// When CheckRedirect is nil (the default), the Dialer returns redirect
	// responses to the application without following them.
	dialer := &Dialer{
		CheckRedirect: nil, // Explicitly disable redirect following
	}

	// With this configuration, the application receives 3xx responses and can
	// decide how to handle them manually. This gives the application full control
	// over the redirect policy and access to the redirect response details.
	_ = dialer

	fmt.Println("Dialer configured to return redirect responses without following")
	// Output: Dialer configured to return redirect responses without following
}
