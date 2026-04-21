//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/billing"
)

// TestBillingFlushNonStream — SC-2 final path: a non-streaming chat
// produces exactly one billing_events row with source='final' and the
// expected token/cost columns.
//
// This exercises the Flusher + CTE insert contract directly; the
// dispatcher-side wiring that actually constructs billing.Event from
// response.usage is covered by proxy/interceptor_usage_test.go unit tests
// and end-to-end by Plan 04-09 HUMAN-UAT.
func TestBillingFlushNonStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)
	seed := seedPhase4(t, ctx, pool)

	flusher := billing.NewFlusher(pool, discardLogger())
	runCtx, cancelRun := context.WithCancel(ctx)
	runDone := make(chan struct{})
	go func() { flusher.Run(runCtx); close(runDone) }()

	reqID := uuid.New()
	ev := billing.Event{
		TS:                  time.Now().UTC(),
		RequestID:           reqID,
		TenantID:            seed.ConverseAITenantID,
		APIKeyID:            seed.ConverseAIAPIKeyID,
		Route:               "chat",
		Upstream:            "local-llm",
		Model:               "qwen3.5-27b",
		TokensIn:            10,
		TokensOut:           20,
		AudioSeconds:        0,
		EmbedsCount:         0,
		CostLocalBRL:        0,
		CostLocalPhantomBRL: 0.000200,
		CostExternalBRL:     0,
		Source:              "final",
	}
	flusher.Enqueue(ev)

	// Give the flusher a tick + a buffer to land the row.
	cancelRun()
	select {
	case <-runDone:
	case <-time.After(10 * time.Second):
		t.Fatal("flusher Run did not exit on ctx cancel")
	}

	var count int
	var gotIn, gotOut int
	var source string
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*)::int, COALESCE(MAX(tokens_in), 0),
		       COALESCE(MAX(tokens_out), 0), COALESCE(MAX(source), '')
		FROM ai_gateway.billing_events
		WHERE request_id = $1
	`, reqID).Scan(&count, &gotIn, &gotOut, &source); err != nil {
		t.Fatalf("query billing_events: %v", err)
	}
	if count != 1 {
		t.Errorf("billing_events rows: want 1, got %d", count)
	}
	if gotIn != 10 || gotOut != 20 {
		t.Errorf("tokens: want 10/20, got %d/%d", gotIn, gotOut)
	}
	if source != "final" {
		t.Errorf("source: want 'final', got %q", source)
	}

	// usage_counters UPSERT also fired via the CTE (TokensIn+Out+requests+1).
	var countersIn, countersOut, reqCount int64
	if err := pool.QueryRow(ctx, `
		SELECT tokens_in, tokens_out, requests_count
		FROM ai_gateway.usage_counters
		WHERE tenant_id = $1 AND date = (now() AT TIME ZONE 'America/Sao_Paulo')::date
	`, seed.ConverseAITenantID).Scan(&countersIn, &countersOut, &reqCount); err != nil {
		t.Fatalf("query usage_counters: %v", err)
	}
	if countersIn != 10 || countersOut != 20 {
		t.Errorf("usage_counters tokens: want 10/20, got %d/%d", countersIn, countersOut)
	}
	if reqCount != 1 {
		t.Errorf("usage_counters requests_count: want 1, got %d", reqCount)
	}
}

// TestBillingFlushPartialSource — SC-2 partial path: an abnormal-close
// event (source='partial') carries the captured-up-to-disconnect tokens
// and still materializes exactly one billing_events row (same idempotency
// guarantee as final).
func TestBillingFlushPartialSource(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)
	seed := seedPhase4(t, ctx, pool)

	flusher := billing.NewFlusher(pool, discardLogger())
	runCtx, cancelRun := context.WithCancel(ctx)
	runDone := make(chan struct{})
	go func() { flusher.Run(runCtx); close(runDone) }()

	reqID := uuid.New()
	flusher.Enqueue(billing.Event{
		TS:                  time.Now().UTC(),
		RequestID:           reqID,
		TenantID:            seed.ConverseAITenantID,
		APIKeyID:            seed.ConverseAIAPIKeyID,
		Route:               "chat",
		Upstream:            "local-llm",
		Model:               "qwen3.5-27b",
		TokensIn:            25,
		TokensOut:           50, // only half the response landed before disconnect
		CostLocalPhantomBRL: 0.000350,
		Source:              "partial",
	})

	cancelRun()
	select {
	case <-runDone:
	case <-time.After(10 * time.Second):
		t.Fatal("flusher Run did not exit on ctx cancel")
	}

	var source string
	var tokOut int
	if err := pool.QueryRow(ctx, `
		SELECT source, tokens_out FROM ai_gateway.billing_events WHERE request_id = $1
	`, reqID).Scan(&source, &tokOut); err != nil {
		t.Fatalf("query partial row: %v", err)
	}
	if source != "partial" {
		t.Errorf("source: want 'partial', got %q", source)
	}
	if tokOut != 50 {
		t.Errorf("partial tokens_out: want 50 (captured-to-disconnect), got %d", tokOut)
	}
}

// TestBillingFlushStreamUsageExtraction — Pitfall 5 end-to-end via the
// UsageInterceptor direct path. The interceptor wiring + flusher are
// exercised in isolation here; the director.injectStreamOptionsIncludeUsage
// unit tests (Plan 04-06) cover the upstream request mutation.
//
// Deferred to Plan 04-09 HUMAN-UAT: a live OpenRouter streaming request
// observing the usage chunk + ts-correlated billing_events row.
func TestBillingFlushStreamWithUsageInjection(t *testing.T) {
	t.Skip("Deferred to Plan 04-09 HUMAN-UAT — live OpenRouter SSE + usage-chunk observation requires a real upstream. Unit tests in proxy/interceptor_usage_test.go + proxy/stream_options_test.go cover the extraction + injection halves; DB-side flush is covered by TestBillingFlushNonStream + TestBillingFlushPartialSource.")
}
