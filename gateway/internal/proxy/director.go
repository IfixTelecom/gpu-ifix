// Package proxy implements single-upstream reverse proxies for the three
// OpenAI-compatible routes: /v1/chat/completions, /v1/embeddings, and
// /v1/audio/transcriptions. All three share Director behavior (auth-header
// stripping + X-Request-ID propagation) and ErrorHandler behavior
// (OpenAI envelope 502). SSE streaming is enabled for chat via
// FlushInterval: -1 on the ReverseProxy. Multipart is preserved for audio
// by leaving Content-Type untouched.
//
// Phase 3 introduces failover / circuit breakers; this package gets a
// new `NewChainProxy` constructor there and the single-upstream proxies
// become the primary tier of that chain.
package proxy

import (
	"net/http"
	"net/url"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
)

// clientAuthHeaders are stripped before any outbound request to upstream.
// The pod trusts the gateway, not the caller — letting client headers
// through would defeat the gateway as a trust boundary. Case variants
// (x-api-key, X-Api-Key) are handled by Go's canonical MIME header
// matching automatically at Header.Del time.
var clientAuthHeaders = []string{
	"Authorization",
	"X-API-Key",
	"Cookie",
	"Proxy-Authorization",
}

// BuildDirector returns a Director function suitable for
// httputil.ReverseProxy for a single upstream URL.
func BuildDirector(upstream *url.URL) func(*http.Request) {
	return func(r *http.Request) {
		// Rewrite URL to upstream.
		r.URL.Scheme = upstream.Scheme
		r.URL.Host = upstream.Host
		// r.URL.Path is deliberately left unchanged — pod routes
		// mirror the gateway's /v1/... paths 1:1.
		r.Host = upstream.Host

		// Strip client auth headers so pod never sees them.
		for _, h := range clientAuthHeaders {
			r.Header.Del(h)
		}

		// Replace whatever X-Request-ID came in (possibly client-supplied)
		// with the gateway's authoritative request id so pod logs correlate
		// on OUR id — not on a client-controlled value.
		if rid := httpx.RequestIDFrom(r.Context()); rid != "" {
			r.Header.Set("X-Request-ID", rid)
		} else {
			r.Header.Del("X-Request-ID")
		}
	}
}
