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
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/idempotency"
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

// TestRateLimitMiddleware_ReplaySkipsBucket asserts the replay context flag
// short-circuits the Lua check so replays never re-consume the bucket
// (D-D1). The test uses a nil rdb — if the middleware attempted a Redis
// call, it would panic on the nil deref; passing through silently proves
// the skip path is taken.
func TestRateLimitMiddleware_ReplaySkipsBucket(t *testing.T) {
	loader := &tenants.Loader{}
	passed := false
	h := quota.RateLimitMiddleware(nil, loader, true, silentLog())(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			passed = true
			w.WriteHeader(http.StatusOK)
		}))
	rec := httptest.NewRecorder()
	ctx := idempotency.WithReplay(context.Background())
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil).WithContext(ctx)
	h.ServeHTTP(rec, req)
	if !passed {
		t.Fatalf("handler must run on replay; got status %d", rec.Code)
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
	h := quota.QuotaMiddleware(checker, loader, silentLog())(
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
	h := quota.QuotaMiddleware(checker, loader, silentLog())(
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
