// Package upstreams aggregates the pod's health-bridge state and exposes
// it to Phase 2 clients via GET /v1/health/upstreams. Subset of Phase 3
// responsibilities — here we only echo the health-bridge's aggregate.
// In-memory 5s cache avoids hammering :9100 on dashboard refreshes.
package upstreams

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
)

const (
	cacheTTL    = 5 * time.Second
	probeBudget = 2 * time.Second
)

type cachedResponse struct {
	status   int
	body     []byte
	storedAt time.Time
}

// NewHealthHandler returns the HTTP handler for GET /v1/health/upstreams.
// The healthBridgeURL is cfg.UpstreamHealthBridgeURL.
func NewHealthHandler(healthBridgeURL string, log *slog.Logger) http.HandlerFunc {
	client := &http.Client{Timeout: probeBudget + 500*time.Millisecond}
	log = log.With("module", "UPSTREAMS")
	var (
		mu    sync.Mutex
		cache cachedResponse
	)

	return func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if time.Since(cache.storedAt) < cacheTTL && cache.body != nil {
			b, s := cache.body, cache.status
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(s)
			_, _ = w.Write(b)
			return
		}
		mu.Unlock()

		ctx, cancel := context.WithTimeout(r.Context(), probeBudget)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthBridgeURL+"/health", nil)
		if err != nil {
			writeUnreachable(w, log, "build request", err, httpx.RequestIDFrom(r.Context()))
			return
		}
		resp, err := client.Do(req)
		if err != nil {
			writeUnreachable(w, log, "probe", err, httpx.RequestIDFrom(r.Context()))
			return
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			writeUnreachable(w, log, "read", err, httpx.RequestIDFrom(r.Context()))
			return
		}

		// Cache the upstream body verbatim (it's already in our desired
		// shape — flat {status, services, ...} per Phase 1 D-12).
		mu.Lock()
		cache = cachedResponse{
			status:   resp.StatusCode,
			body:     append([]byte(nil), body...),
			storedAt: time.Now(),
		}
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
	}
}

func writeUnreachable(w http.ResponseWriter, log *slog.Logger, stage string, err error, reqID string) {
	log.Error("upstreams health probe failed", "stage", stage, "err", err, "request_id", reqID)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":   "failed",
		"services": map[string]any{},
		"error":    err.Error(),
	})
}
