package proxy

import (
	"fmt"
	"net/http"
)

// ProxyResponseInterceptor is the formal extension point for observing OR
// wrapping the upstream response before it is written to the client. Plan
// 02-05 audit tee implements this to wrap resp.Body in a TeeBody; Plan 02-06
// idempotency uses middleware (NOT an interceptor) because it operates at
// the handler layer, not the proxy layer.
//
// Interceptors MUST NOT call resp.Body.Close themselves — the proxy's
// downstream Writer is responsible. Wrapping resp.Body with a custom
// io.ReadCloser is fine; the wrapper's Close MUST delegate to the original.
type ProxyResponseInterceptor interface {
	Intercept(resp *http.Response) error
}

// ComposeInterceptors combines N interceptors into a single
// httputil.ReverseProxy ModifyResponse function. Order-preserving: first
// arg runs first. On first error, the remaining interceptors are NOT
// invoked, and the error propagates to ErrorHandler. An empty arg list
// returns a no-op so the three proxy constructors stay working when no
// hooks are passed in (Plan 02-04 stand-alone baseline).
func ComposeInterceptors(ics ...ProxyResponseInterceptor) func(*http.Response) error {
	if len(ics) == 0 {
		return func(*http.Response) error { return nil }
	}
	return func(resp *http.Response) error {
		for i, ic := range ics {
			if err := ic.Intercept(resp); err != nil {
				return fmt.Errorf("proxy interceptor #%d: %w", i, err)
			}
		}
		return nil
	}
}
