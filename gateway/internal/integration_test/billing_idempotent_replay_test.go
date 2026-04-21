//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/billing"
)

// TestBillingIdempotentReplay — Pitfall 7 mitigation: the CTE in
// InsertBillingEvent uses ON CONFLICT (request_id, ts) DO NOTHING. When an
// idempotency replay flushes twice with the same (request_id, ts) pair,
// the second insert must no-op AND the usage_counters UPSERT must NOT
// double-increment (the CTE only runs the UPSERT on rows actually inserted).
//
// This is the DB-side invariant. The middleware-side replay semantics
// (does a REPLAY re-enqueue at all? D-D1 says quota consumes but cache
// short-circuits everything else) is covered by middleware_chain_test.go
// and by the production flow in cmd/gateway/main.go.
func TestBillingIdempotentReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)
	seed := seedPhase4(t, ctx, pool)

	flusher := billing.NewFlusher(pool, discardLogger())
	runCtx, cancelRun := context.WithCancel(ctx)
	runDone := make(chan struct{})
	go func() { flusher.Run(runCtx); close(runDone) }()

	reqID := uuid.New()
	ts := time.Now().UTC()
	// The same logical event enqueued twice — simulates a code path that
	// double-flushes (replay path + defer-flush racing, or a bug).
	ev := billing.Event{
		TS:                  ts,
		RequestID:           reqID,
		TenantID:            seed.ConverseAITenantID,
		APIKeyID:            seed.ConverseAIAPIKeyID,
		Route:               "chat",
		Upstream:            "local-llm",
		Model:               "qwen3.5-27b",
		TokensIn:            10,
		TokensOut:           20,
		CostLocalPhantomBRL: 0.000200,
		Source:              "final",
	}
	flusher.Enqueue(ev)
	flusher.Enqueue(ev)
	flusher.Enqueue(ev)

	cancelRun()
	select {
	case <-runDone:
	case <-time.After(10 * time.Second):
		t.Fatal("flusher Run did not exit on ctx cancel")
	}

	// Exactly 1 billing_events row (ON CONFLICT DO NOTHING).
	var billingCount int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*)::int FROM ai_gateway.billing_events WHERE request_id = $1`,
		reqID).Scan(&billingCount); err != nil {
		t.Fatal(err)
	}
	if billingCount != 1 {
		t.Errorf("idempotency broken: want 1 billing_events row, got %d", billingCount)
	}

	// usage_counters incremented exactly once — the CTE only runs the
	// UPSERT on the INSERT that actually happened.
	var countersIn, countersOut, reqCount int64
	if err := pool.QueryRow(ctx, `
		SELECT tokens_in, tokens_out, requests_count
		FROM ai_gateway.usage_counters
		WHERE tenant_id = $1 AND date = (now() AT TIME ZONE 'America/Sao_Paulo')::date
	`, seed.ConverseAITenantID).Scan(&countersIn, &countersOut, &reqCount); err != nil {
		t.Fatalf("query usage_counters: %v", err)
	}
	if countersIn != 10 || countersOut != 20 {
		t.Errorf("usage_counters over-counted on replay: want 10/20, got %d/%d",
			countersIn, countersOut)
	}
	if reqCount != 1 {
		t.Errorf("usage_counters.requests_count over-counted: want 1, got %d", reqCount)
	}
}
