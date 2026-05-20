// Unit tests for the RateLimit + Quota middlewares. Integration coverage
// with a real Redis + Postgres testcontainer lives in Plan 04-08.
package quota_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/quota"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/tenants"
)

func silentLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestRateLimitMiddleware_NoAuthContext_401 asserts the middleware rejects
// requests that reach it without an auth context (defensive; chain order
// puts auth BEFORE rate-limit so this branch should never fire in prod).
func TestRateLimitMiddleware_NoAuthContext_401(t *testing.T) {
	loader := &tenants.Loader{}
	h := quota.RateLimitMiddleware(nil, loader, true, silentLog())(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Fatal("should not pass without auth context")
		}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status: want 401, got %d", rec.Code)
	}
}

// TestRateLimitMiddleware_NoAuthReturns401 asserts that a request without
// an auth context is rejected with 401 (auth middleware runs earlier in
// the chain; this is a defensive guard).
//
// ME-02 fix: the previous test TestRateLimitMiddleware_ReplaySkipsBucket
// asserted dead code — the idempotency middleware is mounted per-handler
// AFTER rate-limit in the chain, so a replay never reaches the rate-
// limit middleware. The D-D1 "replays skip rate-limit" semantic is
// enforced by the chain ORDER, not a ctx flag.
func TestRateLimitMiddleware_NoAuthReturns401(t *testing.T) {
	loader := &tenants.Loader{}
	h := quota.RateLimitMiddleware(nil, loader, true, silentLog())(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when no auth ctx, got %d", rec.Code)
	}
}

// TestRateLimitMiddleware_TenantUnknownPassesThrough asserts that when the
// tenants loader has no snapshot row for the authed tenant (freshly added
// and pending refresh), the middleware passes through rather than emitting
// a 503 — auth already confirmed the API key is active. Mirrors the same
// graceful-fallthrough behavior the schedule middleware uses.
func TestRateLimitMiddleware_TenantUnknownPassesThrough(t *testing.T) {
	loader := &tenants.Loader{} // snap is nil → Get returns ErrTenantNotFound
	passed := false
	h := quota.RateLimitMiddleware(nil, loader, true, silentLog())(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			passed = true
			w.WriteHeader(http.StatusOK)
		}))
	ctx := auth.WithContext(context.Background(), auth.AuthContext{
		TenantID: uuid.New().String(),
		APIKeyID: uuid.New().String(),
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil).WithContext(ctx)
	h.ServeHTTP(rec, req)
	if !passed {
		t.Fatalf("handler must run when tenant snapshot missing; got %d", rec.Code)
	}
}

// TestQuotaMiddleware_NoAuthPassesThrough asserts graceful fallthrough
// when auth is not in context (should not happen in a properly wired
// chain; defensive guard).
func TestQuotaMiddleware_NoAuthPassesThrough(t *testing.T) {
	loader := &tenants.Loader{}
	checker := quota.NewQuotaChecker(nil, silentLog()) // nil queries — never reached
	passed := false
	h := quota.QuotaMiddleware(checker, loader, false, silentLog())(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			passed = true
		}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	h.ServeHTTP(rec, req)
	if !passed {
		t.Fatalf("handler must run when no auth ctx; got %d", rec.Code)
	}
}

// TestQuotaMiddleware_TenantUnknownPassesThrough asserts graceful
// fallthrough when the loader has no snapshot for the authed tenant.
func TestQuotaMiddleware_TenantUnknownPassesThrough(t *testing.T) {
	loader := &tenants.Loader{} // nil snapshot
	checker := quota.NewQuotaChecker(nil, silentLog())
	passed := false
	h := quota.QuotaMiddleware(checker, loader, false, silentLog())(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			passed = true
		}))
	ctx := auth.WithContext(context.Background(), auth.AuthContext{
		TenantID: uuid.New().String(),
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil).WithContext(ctx)
	h.ServeHTTP(rec, req)
	if !passed {
		t.Fatalf("handler must run when tenant snapshot missing; got %d", rec.Code)
	}
}

// Compile-time assurance that the exported symbols referenced by main.go
// wiring exist with the expected signatures.
var (
	_ = quota.RateLimitMiddleware
	_ = quota.QuotaMiddleware
	_ = quota.NewQuotaChecker
)

// Silence unused-import complaints when we fold tests; keep this named
// dep-compile so refactors that forget a test file still tell us.
var _ = errors.Is

// ---------------------------------------------------------------------------
// Phase 06.7 Wave 0 RED scaffolding (Nyquist gate). Skip stub binding the
// TTS route-class plumbing to its owning implementation plan. ENGINE-AGNOSTIC
// — it asserts that `/v1/audio/speech` classifies to a TTS rate-limit bucket,
// regardless of which TTS server runs on the primary pod.
//
// OWNER map (authority: 06.7-02-PLAN.md <stub_ownership_map>):
//   - TestClassifyRoute_TTS -> Plan 06.7-03
// ---------------------------------------------------------------------------

// TestClassifyRoute_TTS asserts that classifyRoute("/v1/audio/speech")
// returns the new RouteClassTTS constant (NOT the RouteClassChat default
// fallback). The owning plan must add the new const RouteClassTTS
// RouteClass = "tts" in bucket.go and the case "/v1/audio/speech":
// return RouteClassTTS arm in classifyRoute. Because RouteClass strings are
// persisted in Redis keys (gw:rate:{tenant}:{class}:*) the chosen value
// "tts" is a wire contract and must not change once deployed.
//
// OWNER: Plan 06.7-03 — add RouteClassTTS + classify arm, unskip, and
// assert classifyRoute("/v1/audio/speech") == quota.RouteClassTTS.
//
// classifyRoute is unexported, so the path->class mapping is asserted in the
// internal-package test (classify_tts_internal_test.go, package quota). Here
// in the external test we lock the Redis wire-contract value of the exported
// RouteClassTTS constant ("tts" must never change once deployed).
func TestClassifyRoute_TTS(t *testing.T) {
	if quota.RouteClassTTS != "tts" {
		t.Errorf("RouteClassTTS wire value = %q, want \"tts\" (Redis key contract)", quota.RouteClassTTS)
	}
	// Distinct from the existing classes so buckets are namespaced apart.
	if quota.RouteClassTTS == quota.RouteClassChat || quota.RouteClassTTS == quota.RouteClassSTT {
		t.Errorf("RouteClassTTS must be distinct from Chat/STT classes")
	}
}
