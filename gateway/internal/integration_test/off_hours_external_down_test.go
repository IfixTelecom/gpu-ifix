//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auditctx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/proxy"
)

// TestOffHoursExternalDown — D-C2 no-fallback-of-fallback contract: a
// peak-mode tenant hitting the dispatcher with upstream_override set AND
// the override target's breaker OPEN must receive 503 +
// off_hours_upstream_unavailable. There is NO retry to OpenAI direct —
// Phase 3 D-C4 forbids it (drift Qwen→GPT is proibitivo).
//
// The test wires the production Dispatcher (not the full main.go chain),
// seeds the override into ctx directly, and trips the override target's
// breaker to OPEN.
func TestOffHoursExternalDown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, rdb := freshSchema(t, ctx)

	// Two mock upstreams: tier-0 (local-llm) CLOSED + responsive, tier-1
	// (openrouter-chat) — we don't actually exercise tier-1 because the
	// breaker trips before any proxy call; a panic-proxy catches any bug
	// that would dispatch anyway.
	tier0 := newSuccessMock(t)
	tier1 := newSuccessMock(t)

	loader := resilienceLoader("llm",
		"local-llm", tier0.server.URL,
		"openrouter-chat", tier1.server.URL,
	)
	bs := breaker.NewSet(rdb, discardLogger(),
		breaker.Options{ConsecutiveFailures: 1, Cooldown: 30 * time.Second},
		loader.Names(),
	)
	// Trip the OVERRIDE target's breaker (openrouter-chat) to OPEN so the
	// dispatchOverride branch's pre-check fails.
	driveBreaker(t, bs, "openrouter-chat", 500, 3)
	if got := bs.Snapshot()["openrouter-chat"]; got != "open" {
		t.Fatalf("tier-1 breaker: want open, got %q", got)
	}

	// PanicProxy for BOTH upstreams — neither may be dispatched once
	// the override pre-check runs (openrouter-chat breaker OPEN fails
	// fast; local-llm must not be considered because override bypasses
	// tier-0 entirely per D-C2).
	panicProxy := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("no proxy should be dispatched when override breaker is OPEN")
		w.WriteHeader(http.StatusInternalServerError)
	})

	disp := proxy.NewDispatcher(proxy.DispatcherConfig{
		Role:    "llm",
		Loader:  loader,
		Breaker: bs,
		Proxies: map[string]http.Handler{
			"local-llm":       panicProxy,
			"openrouter-chat": panicProxy,
		},
		Log: slog.New(slog.NewTextHandler(discardWriter{}, nil)),
	})

	// Write override AFTER auth; dispatcher reads from ctx directly.
	// seedPhase4 is unnecessary — we don't need tenants.Loader in this
	// path because the dispatcher just consumes the ctx value.
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	// auth.FromContext must return ok for the dispatcher's early check.
	req = req.WithContext(auditctx.WithUpstreamOverride(req.Context(), "openrouter-chat"))
	// Authenticated via injectAuth.
	authed := injectAuth(disp, "00000000-0000-0000-0000-000000000001", "normal")

	rec := httptest.NewRecorder()
	authed.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status: want 503, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode envelope: %v body=%s", err, rec.Body.String())
	}
	if body.Error.Code != "off_hours_upstream_unavailable" {
		t.Errorf("error.code: want off_hours_upstream_unavailable, got %q", body.Error.Code)
	}
	if body.Error.Type != "service_unavailable" {
		t.Errorf("error.type: want service_unavailable, got %q", body.Error.Type)
	}
}
