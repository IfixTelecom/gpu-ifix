//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/tenants"
)

// TestTenantsHotReload — D-C4 NOTIFY tenants_changed → loader.Refresh.
// Updating a quota/limit column must propagate to the loader snapshot
// within 5s (SLA target ≤ 1s).
func TestTenantsHotReload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)
	seed := seedPhase4(t, ctx, pool)

	loc, _ := time.LoadLocation("America/Sao_Paulo")
	loader, err := tenants.NewLoader(ctx, pool, loc, discardLogger())
	if err != nil {
		t.Fatalf("new loader: %v", err)
	}

	// Baseline: seedPhase4 leaves the default rps_limit from migration
	// 0013 (20). Record it so we can assert change.
	cfg0, err := loader.Get(seed.ConverseAITenantID)
	if err != nil {
		t.Fatalf("loader.Get initial: %v", err)
	}
	initial := cfg0.RPSLimit

	listenCtx, listenCancel := context.WithCancel(ctx)
	defer listenCancel()
	listenDone := make(chan struct{})
	go func() {
		defer close(listenDone)
		_ = tenants.ListenAndReload(listenCtx, sharedPGDSN, loader, discardLogger())
	}()
	time.Sleep(500 * time.Millisecond)

	// UPDATE rps_limit (in the trigger's WHEN filter per migration 0013).
	const target = 999
	if _, err := pool.Exec(ctx,
		`UPDATE ai_gateway.tenants SET rps_limit = $1 WHERE id = $2`,
		target, seed.ConverseAITenantID); err != nil {
		t.Fatalf("UPDATE rps: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cfg, err := loader.Get(seed.ConverseAITenantID)
		if err == nil && cfg.RPSLimit == target {
			listenCancel()
			<-listenDone
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	cfgFinal, _ := loader.Get(seed.ConverseAITenantID)
	t.Fatalf("tenants did not hot-reload within 5s: RPSLimit=%d want %d (initial=%d)",
		cfgFinal.RPSLimit, target, initial)
}

// TestTenantsHotReloadMode — D-C4 mode change specifically. gatewayctl
// tenant set-mode is the operator entry point; the listener must pick up
// mode transitions so schedule.Middleware sees the new state without a
// restart.
func TestTenantsHotReloadMode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)
	seed := seedPhase4(t, ctx, pool)

	loc, _ := time.LoadLocation("America/Sao_Paulo")
	loader, err := tenants.NewLoader(ctx, pool, loc, discardLogger())
	if err != nil {
		t.Fatalf("new loader: %v", err)
	}

	listenCtx, listenCancel := context.WithCancel(ctx)
	defer listenCancel()
	listenDone := make(chan struct{})
	go func() {
		defer close(listenDone)
		_ = tenants.ListenAndReload(listenCtx, sharedPGDSN, loader, discardLogger())
	}()
	time.Sleep(500 * time.Millisecond)

	// Switch converseai (normal → peak is allowed; sensitive cobrancas is
	// blocked by chk_sensitive_no_peak and tested elsewhere).
	if _, err := pool.Exec(ctx, `
		UPDATE ai_gateway.tenants
		SET mode = 'peak', peak_window_start = '08:00', peak_window_end = '22:00'
		WHERE id = $1
	`, seed.ConverseAITenantID); err != nil {
		t.Fatalf("UPDATE mode: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cfg, err := loader.Get(seed.ConverseAITenantID)
		if err == nil && cfg.Mode == "peak" {
			listenCancel()
			<-listenDone
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	cfgFinal, _ := loader.Get(seed.ConverseAITenantID)
	t.Fatalf("tenants mode did not hot-reload within 5s: Mode=%q want peak", cfgFinal.Mode)
}
