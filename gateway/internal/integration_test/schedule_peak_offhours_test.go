//go:build integration

package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auditctx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/schedule"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/tenants"
)

// TestSchedulePeakOffHours — SC-4: peak-mode tenant with the current
// America/Sao_Paulo clock OUTSIDE the peak window must have
// upstream_override="openrouter-chat" written to the request context by
// schedule.Middleware.
//
// Strategy: we pick a peak window that is guaranteed to NOT contain the
// current minute. Since we can't fake the clock without a larger refactor,
// the window is computed dynamically relative to nowSP.
func TestSchedulePeakOffHours(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)
	seed := seedPhase4(t, ctx, pool)

	loc, _ := time.LoadLocation("America/Sao_Paulo")
	nowSP := time.Now().In(loc)
	// Compose a window that is guaranteed NOT to contain the current hour.
	// Use (now+2h .. now+4h) in SP hours wrapped around 0..23.
	startH := (nowSP.Hour() + 2) % 24
	endH := (nowSP.Hour() + 4) % 24
	if _, err := pool.Exec(ctx, `
		UPDATE ai_gateway.tenants
		SET mode = 'peak',
		    peak_window_start = make_time($1, 0, 0),
		    peak_window_end   = make_time($2, 0, 0),
		    schedule_timezone = 'America/Sao_Paulo'
		WHERE id = $3
	`, startH, endH, seed.ConverseAITenantID); err != nil {
		t.Fatalf("UPDATE tenant mode: %v", err)
	}

	loader, err := tenants.NewLoader(ctx, pool, loc, discardLogger())
	if err != nil {
		t.Fatalf("new loader: %v", err)
	}

	var capturedOverride string
	h := schedule.Middleware(loader, discardLogger())(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedOverride = auditctx.UpstreamOverrideFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		}),
	)
	h = injectAuthWithID(h, seed.ConverseAITenantID.String(), auth.DataClassNormal)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", nil))

	if capturedOverride != "openrouter-chat" {
		t.Errorf("peak off-hours: expected upstream_override='openrouter-chat', got %q (window=%02d-%02d nowSP=%02d)",
			capturedOverride, startH, endH, nowSP.Hour())
	}
}

// TestSchedulePeakInHours — SC-4 complement: peak + clock INSIDE the
// window → no override (dispatcher follows normal tier-0 chain).
func TestSchedulePeakInHours(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)
	seed := seedPhase4(t, ctx, pool)

	loc, _ := time.LoadLocation("America/Sao_Paulo")
	nowSP := time.Now().In(loc)
	// Window wrapping the current hour: (now-1h) .. (now+1h).
	startH := (nowSP.Hour() - 1 + 24) % 24
	endH := (nowSP.Hour() + 1) % 24
	if _, err := pool.Exec(ctx, `
		UPDATE ai_gateway.tenants
		SET mode = 'peak',
		    peak_window_start = make_time($1, 0, 0),
		    peak_window_end   = make_time($2, 0, 0),
		    schedule_timezone = 'America/Sao_Paulo'
		WHERE id = $3
	`, startH, endH, seed.ConverseAITenantID); err != nil {
		t.Fatalf("UPDATE tenant mode: %v", err)
	}

	loader, err := tenants.NewLoader(ctx, pool, loc, discardLogger())
	if err != nil {
		t.Fatalf("new loader: %v", err)
	}

	var capturedOverride string
	h := schedule.Middleware(loader, discardLogger())(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedOverride = auditctx.UpstreamOverrideFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		}),
	)
	h = injectAuthWithID(h, seed.ConverseAITenantID.String(), auth.DataClassNormal)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", nil))

	if capturedOverride != "" {
		t.Errorf("peak in-hours: expected no override, got %q", capturedOverride)
	}
}

// TestSchedule24x7AlwaysLocal — SC-4 complement: mode=24/7 never sets
// an override regardless of clock.
func TestSchedule24x7AlwaysLocal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)
	seed := seedPhase4(t, ctx, pool)

	// seedPhase4 already leaves converseai at mode=24/7; assert default.
	var mode string
	if err := pool.QueryRow(ctx,
		`SELECT mode FROM ai_gateway.tenants WHERE id = $1`,
		seed.ConverseAITenantID).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "24/7" {
		t.Fatalf("fixture precondition: want mode=24/7, got %q", mode)
	}

	loc, _ := time.LoadLocation("America/Sao_Paulo")
	loader, err := tenants.NewLoader(ctx, pool, loc, discardLogger())
	if err != nil {
		t.Fatal(err)
	}

	var capturedOverride string
	h := schedule.Middleware(loader, discardLogger())(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedOverride = auditctx.UpstreamOverrideFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		}),
	)
	h = injectAuthWithID(h, seed.ConverseAITenantID.String(), auth.DataClassNormal)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", nil))

	if capturedOverride != "" {
		t.Errorf("24/7 must NEVER override; got %q", capturedOverride)
	}
}
