//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/billing"
)

// TestPricesHotReload — D-B3 NOTIFY prices_changed → loader.Refresh
// roundtrip. Mutating prices.unit_cost_usd must propagate to the loader's
// lock-free snapshot within the SLA (≤ 5s, target ≤ 1s per CONTEXT §D-B3).
func TestPricesHotReload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)
	_ = seedPhase4(t, ctx, pool)

	loader, err := billing.NewPricesLoader(ctx, pool, discardLogger())
	if err != nil {
		t.Fatalf("new prices loader: %v", err)
	}
	fxLoader, err := billing.NewFXLoader(ctx, pool, discardLogger())
	if err != nil {
		t.Fatalf("new fx loader: %v", err)
	}

	// Starting price must match seedPhase4's 0.00000020 baseline.
	p0, ok := loader.Get("qwen3.5-27b", "openrouter-fireworks", "input_token")
	if !ok {
		t.Fatal("seed price missing on initial load")
	}
	const initial = 0.00000020
	if p0.UnitCostUSD < initial*0.9 || p0.UnitCostUSD > initial*1.1 {
		t.Logf("WARN: initial price %.10f not near 0.00000020 (may have drifted from seed)", p0.UnitCostUSD)
	}

	// Listener goroutine — pgxlisten multiplexes prices_changed + fx_changed.
	listenCtx, listenCancel := context.WithCancel(ctx)
	defer listenCancel()
	listenDone := make(chan struct{})
	go func() {
		defer close(listenDone)
		_ = billing.ListenAndReload(listenCtx, sharedPGDSN, loader, fxLoader, discardLogger())
	}()
	time.Sleep(500 * time.Millisecond) // let LISTEN register

	// UPDATE the active price row. Triggers notify_prices_changed → the
	// pgxlisten handler calls loader.Refresh.
	const target = 0.00000099
	if _, err := pool.Exec(ctx, `
		UPDATE ai_gateway.prices
		SET unit_cost_usd = $1
		WHERE model = 'qwen3.5-27b'
		  AND provider = 'openrouter-fireworks'
		  AND unit = 'input_token'
		  AND valid_to IS NULL
	`, target); err != nil {
		t.Fatalf("UPDATE prices: %v", err)
	}

	// Poll loader.Get with 5s deadline.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		p2, _ := loader.Get("qwen3.5-27b", "openrouter-fireworks", "input_token")
		if p2.UnitCostUSD > 0.0000009 && p2.UnitCostUSD < 0.00000999 {
			// Target price landed within 5s — graceful shutdown and pass.
			listenCancel()
			<-listenDone
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	pFinal, _ := loader.Get("qwen3.5-27b", "openrouter-fireworks", "input_token")
	t.Fatalf("prices did not hot-reload within 5s: got UnitCostUSD=%.10f want ~%.10f",
		pFinal.UnitCostUSD, target)
}

// TestFXHotReload — D-B3 NOTIFY fx_changed (multiplexed on the same pgx
// connection). UPDATEing fx_rates must propagate to FXLoader within 5s.
func TestFXHotReload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)
	_ = seedPhase4(t, ctx, pool)

	loader, err := billing.NewPricesLoader(ctx, pool, discardLogger())
	if err != nil {
		t.Fatalf("new prices loader: %v", err)
	}
	fxLoader, err := billing.NewFXLoader(ctx, pool, discardLogger())
	if err != nil {
		t.Fatalf("new fx loader: %v", err)
	}

	listenCtx, listenCancel := context.WithCancel(ctx)
	defer listenCancel()
	listenDone := make(chan struct{})
	go func() {
		defer close(listenDone)
		_ = billing.ListenAndReload(listenCtx, sharedPGDSN, loader, fxLoader, discardLogger())
	}()
	time.Sleep(500 * time.Millisecond)

	// UPDATE USD/BRL rate. seedPhase4 seeded 5.10; set to 7.77.
	if _, err := pool.Exec(ctx, `
		UPDATE ai_gateway.fx_rates SET rate = 7.77
		WHERE currency_pair = 'USD/BRL' AND valid_to IS NULL
	`); err != nil {
		t.Fatalf("UPDATE fx: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r, ok := fxLoader.Get("USD/BRL")
		if ok && r.Rate > 7.76 && r.Rate < 7.78 {
			listenCancel()
			<-listenDone
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	r, _ := fxLoader.Get("USD/BRL")
	t.Fatalf("fx did not hot-reload within 5s: got Rate=%.4f want ~7.77", r.Rate)
}
