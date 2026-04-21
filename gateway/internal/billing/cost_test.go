package billing_test

import (
	"io"
	"log/slog"
	"testing"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/billing"
)

func discardLog() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// TestComputeCost_MissingPriceReturnsZero asserts the "unknown model" path.
// When PricesLoader.Get returns (Price{}, false) — which a zero-value loader
// (nil snapshot) does — the helper returns 0 so the flush doesn't block.
//
// Full numeric verification with real loaders + seeded prices lives in
// Plan 04-08 integration tests (testcontainers Postgres). Here we cover
// the pure-Go contract: missing price, missing fx, negative units.
func TestComputeCost_MissingPriceReturnsZero(t *testing.T) {
	var prices billing.PricesLoader
	var fx billing.FXLoader
	got := billing.ComputeCostBRL(1000,
		"unknown-model", "unknown-provider", "input_token",
		&prices, &fx, 5.10, discardLog())
	if got != 0 {
		t.Errorf("missing price → cost should be 0, got %v", got)
	}
}

// TestComputeCost_NegativeUnitsClampToZero asserts the defensive clamp.
func TestComputeCost_NegativeUnitsClampToZero(t *testing.T) {
	var prices billing.PricesLoader
	var fx billing.FXLoader
	got := billing.ComputeCostBRL(-100,
		"m", "p", "u",
		&prices, &fx, 5.10, discardLog())
	if got != 0 {
		t.Errorf("negative units should clamp to 0, got %v", got)
	}
}

// TestComputeCost_ZeroUnitsReturnsZero — boundary guard.
func TestComputeCost_ZeroUnitsReturnsZero(t *testing.T) {
	var prices billing.PricesLoader
	var fx billing.FXLoader
	got := billing.ComputeCostBRL(0,
		"m", "p", "u",
		&prices, &fx, 5.10, discardLog())
	if got != 0 {
		t.Errorf("zero units: want 0, got %v", got)
	}
}

// TestComputeCost_NilLoadersSafe — defensive-wiring path.
func TestComputeCost_NilLoadersSafe(t *testing.T) {
	got := billing.ComputeCostBRL(1.0, "m", "p", "u", nil, nil, 5.10, discardLog())
	if got != 0 {
		t.Errorf("nil loaders: want 0, got %v", got)
	}
}

// TestAccountant_SetGetDelete exercises the concurrent-safe per-request map.
func TestAccountant_SetGetDelete(t *testing.T) {
	a := billing.NewAccountant()
	if a.Get("nonexistent") != nil {
		t.Error("Get on empty Accountant should return nil")
	}
	u := &billing.RequestUsage{}
	u.TokensIn.Store(42)
	a.Set("req-1", u)
	got := a.Get("req-1")
	if got == nil {
		t.Fatal("Get after Set returned nil")
	}
	if got.TokensIn.Load() != 42 {
		t.Errorf("TokensIn roundtrip: want 42, got %d", got.TokensIn.Load())
	}
	a.Delete("req-1")
	if a.Get("req-1") != nil {
		t.Error("Delete did not remove the slot")
	}
}

// TestAccountant_DeleteNonexistent — idempotency of Delete.
func TestAccountant_DeleteNonexistent(t *testing.T) {
	a := billing.NewAccountant()
	a.Delete("never-set") // must not panic
}
