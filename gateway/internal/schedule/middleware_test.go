// Unit tests for schedule.Middleware. Full peak-off-hours behavior with a
// populated loader snapshot is covered by the Plan 04-08 integration suite
// (where a real tenants.Loader can be wired via testcontainers Postgres).
package schedule_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auditctx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/schedule"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/tenants"
)

func silentLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestScheduleMiddleware_NoAuthContextPassthrough asserts the middleware
// does not touch the context when auth is absent.
func TestScheduleMiddleware_NoAuthContextPassthrough(t *testing.T) {
	loader := &tenants.Loader{}
	var got string
	h := schedule.Middleware(loader, silentLog())(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = auditctx.UpstreamOverrideFromContext(r.Context())
		}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	h.ServeHTTP(rec, req)
	if got != "" {
		t.Errorf("expected no override without auth ctx, got %q", got)
	}
}

// TestScheduleMiddleware_TenantUnknownPassthrough asserts a missing tenant
// snapshot row (freshly added; pending refresh) does not write an override.
func TestScheduleMiddleware_TenantUnknownPassthrough(t *testing.T) {
	loader := &tenants.Loader{} // nil snapshot
	var got string
	h := schedule.Middleware(loader, silentLog())(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = auditctx.UpstreamOverrideFromContext(r.Context())
		}))
	ctx := auth.WithContext(context.Background(), auth.AuthContext{
		TenantID: uuid.New().String(),
	})
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got != "" {
		t.Errorf("expected no override on unknown tenant, got %q", got)
	}
}

// TestScheduleMiddleware_MalformedTenantIDPassthrough asserts defensively
// that a non-UUID tenant_id (should be impossible post-auth) does not
// panic the middleware.
func TestScheduleMiddleware_MalformedTenantIDPassthrough(t *testing.T) {
	loader := &tenants.Loader{}
	var got string
	h := schedule.Middleware(loader, silentLog())(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = auditctx.UpstreamOverrideFromContext(r.Context())
		}))
	ctx := auth.WithContext(context.Background(), auth.AuthContext{
		TenantID: "not-a-uuid",
	})
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got != "" {
		t.Errorf("expected no override on malformed tenant id, got %q", got)
	}
}
