//go:build integration

package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/sony/gobreaker/v2"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

// setupProbeEnv wires the env vars + Loader + breaker.Set + sqlc Queries
// for a probe test. llmURL/sttURL/embedURL are httptest mock servers; we
// reuse them as the "external" tier-1 endpoints too so the test never
// needs real OpenRouter / OpenAI credentials.
func setupProbeEnv(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	rdb *redis.Client,
	llmURL, sttURL, embedURL string,
	breakerOpts breaker.Options,
) (*upstreams.Loader, *breaker.Set, *gen.Queries) {
	t.Helper()
	resetUpstreamsTable(t, ctx, pool)
	clearUpstreamEnvs(t)
	t.Setenv("UPSTREAM_LLM_URL", llmURL)
	t.Setenv("UPSTREAM_STT_URL", sttURL)
	t.Setenv("UPSTREAM_EMBED_URL", embedURL)
	t.Setenv("UPSTREAM_LLM_OPENROUTER_URL", llmURL)
	t.Setenv("UPSTREAM_LLM_OPENROUTER_AUTH_BEARER", "or-test")
	t.Setenv("UPSTREAM_STT_OPENAI_URL", sttURL)
	t.Setenv("UPSTREAM_STT_OPENAI_AUTH_BEARER", "oa-test")
	t.Setenv("UPSTREAM_EMBED_OPENAI_URL", embedURL)
	t.Setenv("UPSTREAM_EMBED_OPENAI_AUTH_BEARER", "oa-embed")

	loader, err := upstreams.NewLoader(ctx, pool, discardLogger())
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}
	bs := breaker.NewSet(rdb, discardLogger(), breakerOpts, loader.Names())
	return loader, bs, gen.New(pool)
}

// TestIntegration_ProbeLoop_DispatchesToTier0 verifies that on each tick
// every tier-0 upstream is probed at least once and tier-1 externals
// are NOT probed while tier-0 breakers are CLOSED (D-A2). Cadence is
// asserted by sleeping ≥2 ticks worth and checking the per-server hit
// counters.
func TestIntegration_ProbeLoop_DispatchesToTier0(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, rdb := freshSchema(t, ctx)

	var llmHits, sttHits, embedHits atomic.Int64
	mkSrv := func(counter *atomic.Int64) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			counter.Add(1)
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"id":"probe-ok"}`))
		}))
	}
	llm := mkSrv(&llmHits)
	defer llm.Close()
	stt := mkSrv(&sttHits)
	defer stt.Close()
	embed := mkSrv(&embedHits)
	defer embed.Close()

	loader, bs, q := setupProbeEnv(t, ctx, pool, rdb,
		llm.URL, stt.URL, embed.URL,
		breaker.Options{ConsecutiveFailures: 3, Cooldown: 30 * time.Second},
	)

	p := upstreams.NewProbe(loader, bs, q, upstreams.ProbeConfig{
		Interval: 500 * time.Millisecond,
		Budget:   2 * time.Second,
	}, discardLogger())

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	probeDone := make(chan struct{})
	go func() {
		defer close(probeDone)
		p.Run(runCtx)
	}()

	// Wait for at least 2 ticks (≈1s) — first tick fires immediately.
	time.Sleep(1500 * time.Millisecond)

	if llmHits.Load() < 2 {
		t.Errorf("llm tier-0 hits = %d, want >=2", llmHits.Load())
	}
	if sttHits.Load() < 2 {
		t.Errorf("stt tier-0 hits = %d, want >=2", sttHits.Load())
	}
	if embedHits.Load() < 2 {
		t.Errorf("embed tier-0 hits = %d, want >=2", embedHits.Load())
	}

	// Tier-1 externals reuse the same mock servers, so we can't separate
	// hits by tier here — but D-A2 says when tier-0 is CLOSED, tier-1
	// MUST be skipped. The tier-0/tier-1 ratio gives us a clean signal:
	// with 3 tier-0 upstreams and 3 tier-1 skipped, total hits per server
	// must equal exactly the number of ticks (1 hit/tick/server). If
	// tier-1 were also being probed, each server would see 2 hits/tick.
	//
	// Allow some slack (>=2 hits per server is mandatory; we additionally
	// assert that hits are NOT >2× the expected count, which would only
	// happen if tier-1 were being probed against the same mock).
	tickWindow := llmHits.Load()
	if tickWindow > 6 {
		t.Errorf("llm hits = %d in 1.5s window with tier-0 CLOSED — tier-1 should not be probed (D-A2)", tickWindow)
	}

	runCancel()
	select {
	case <-probeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("probe goroutine did not exit within 5s of ctx cancel")
	}
}

// TestIntegration_ProbeLoop_OneFailureDoesNotCancelSiblings is the
// Pitfall 3 regression test. tier-0 LLM returns 500 (fast); tier-0 STT
// returns 200 after 1s (slow but within the 3s budget). With
// errgroup.WithContext, the LLM 500 would cancel the parent ctx and the
// slow STT call would return with ctx.Canceled before the server ever
// fires its handler — sttHits would stay 0. With the zero-value
// errgroup.Group{}, siblings keep running and sttHits MUST be ≥ 1.
func TestIntegration_ProbeLoop_OneFailureDoesNotCancelSiblings(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, rdb := freshSchema(t, ctx)

	var llmHits, sttHits, embedHits atomic.Int64
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		llmHits.Add(1)
		w.WriteHeader(500) // fast failure
	}))
	defer llm.Close()
	stt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sttHits.Add(1)
		time.Sleep(1 * time.Second) // slow but within 3s budget
		w.WriteHeader(200)
	}))
	defer stt.Close()
	embed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		embedHits.Add(1)
		w.WriteHeader(200)
	}))
	defer embed.Close()

	// Use a high failure threshold so the breaker doesn't open mid-test
	// and accidentally start probing tier-1.
	loader, bs, q := setupProbeEnv(t, ctx, pool, rdb,
		llm.URL, stt.URL, embed.URL,
		breaker.Options{ConsecutiveFailures: 50, Cooldown: 30 * time.Second},
	)

	p := upstreams.NewProbe(loader, bs, q, upstreams.ProbeConfig{
		Interval: 5 * time.Second, // long enough that we observe ONE tick
		Budget:   3 * time.Second,
	}, discardLogger())

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	probeDone := make(chan struct{})
	go func() {
		defer close(probeDone)
		p.Run(runCtx)
	}()

	// First tick fires immediately on Run; wait for it to complete.
	// Budget is 3s, slow STT takes 1s — so 2.5s after start the tick
	// MUST be done.
	time.Sleep(2500 * time.Millisecond)

	// LLM must have been called (fast 500).
	if llmHits.Load() == 0 {
		t.Fatal("LLM was not probed in the first tick")
	}
	// STT MUST have been called even though LLM failed fast.
	// This is the Pitfall 3 regression assertion.
	if sttHits.Load() == 0 {
		t.Fatal("STT was not probed; errgroup cascade cancel from LLM 500 — Pitfall 3 regression")
	}
	// Embed must have been called too (independent of LLM).
	if embedHits.Load() == 0 {
		t.Fatal("Embed was not probed in the first tick")
	}

	runCancel()
	select {
	case <-probeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("probe goroutine did not exit within 5s of ctx cancel")
	}
}

// TestIntegration_ProbeLoop_PrimaryFailuresOpenBreaker drives the LLM
// tier-0 to 5xx repeatedly; after enough failures the breaker MUST be
// OPEN and the tier-1 (external) probe MUST start firing (D-A2: tier-1
// is probed only when same-role tier-0 breaker is OPEN/HALF_OPEN).
func TestIntegration_ProbeLoop_PrimaryFailuresOpenBreaker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, rdb := freshSchema(t, ctx)

	var llmTier0Hits, llmTier1Hits atomic.Int64

	llm0 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		llmTier0Hits.Add(1)
		w.WriteHeader(500)
	}))
	defer llm0.Close()
	llm1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		llmTier1Hits.Add(1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"or-ok"}`))
	}))
	defer llm1.Close()
	stt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer stt.Close()
	embed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer embed.Close()

	resetUpstreamsTable(t, ctx, pool)
	clearUpstreamEnvs(t)
	t.Setenv("UPSTREAM_LLM_URL", llm0.URL)
	t.Setenv("UPSTREAM_STT_URL", stt.URL)
	t.Setenv("UPSTREAM_EMBED_URL", embed.URL)
	// tier-1 LLM points at a SEPARATE mock so the assertion can isolate
	// tier-0 hits from tier-1 hits.
	t.Setenv("UPSTREAM_LLM_OPENROUTER_URL", llm1.URL)
	t.Setenv("UPSTREAM_LLM_OPENROUTER_AUTH_BEARER", "or-test")
	t.Setenv("UPSTREAM_STT_OPENAI_URL", stt.URL)
	t.Setenv("UPSTREAM_STT_OPENAI_AUTH_BEARER", "oa-test")
	t.Setenv("UPSTREAM_EMBED_OPENAI_URL", embed.URL)
	t.Setenv("UPSTREAM_EMBED_OPENAI_AUTH_BEARER", "oa-embed")

	loader, err := upstreams.NewLoader(ctx, pool, discardLogger())
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}
	bs := breaker.NewSet(rdb, discardLogger(),
		breaker.Options{ConsecutiveFailures: 3, Cooldown: 5 * time.Second},
		loader.Names())

	p := upstreams.NewProbe(loader, bs, gen.New(pool), upstreams.ProbeConfig{
		Interval: 200 * time.Millisecond,
		Budget:   2 * time.Second,
	}, discardLogger())

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	probeDone := make(chan struct{})
	go func() {
		defer close(probeDone)
		p.Run(runCtx)
	}()

	// 3 ticks of 5xx → breaker must open. With Interval=200ms, three
	// ticks elapse in ~0.6s; allow generous wall time for slow CI.
	deadline := time.Now().Add(5 * time.Second)
	var openedAt time.Time
	for time.Now().Before(deadline) {
		cb, ok := bs.Get("local-llm")
		if ok && cb.State() == gobreaker.StateOpen {
			openedAt = time.Now()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if openedAt.IsZero() {
		t.Fatalf("local-llm breaker did not open within 5s; tier-0 hits=%d", llmTier0Hits.Load())
	}

	// Once OPEN, tier-1 (openrouter-chat) MUST start being probed.
	deadline2 := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline2) {
		if llmTier1Hits.Load() > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if llmTier1Hits.Load() == 0 {
		t.Fatal("openrouter-chat (tier-1) was not probed after local-llm (tier-0) opened — D-A2 violated")
	}

	runCancel()
	select {
	case <-probeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("probe goroutine did not exit within 5s of ctx cancel")
	}
}

// TestIntegration_ProbeLoop_BatchUpdateFlushesWithinOneSecond verifies
// the writeback channel drains at the 1s flush interval and the
// upstreams.last_probe_* columns are populated for every probed row.
// This is the contract /v1/health/upstreams (Task 2) reads from when
// the operator wants to see "last successful probe" alongside breaker
// state.
func TestIntegration_ProbeLoop_BatchUpdateFlushesWithinOneSecond(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, rdb := freshSchema(t, ctx)

	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer llm.Close()
	stt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer stt.Close()
	embed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer embed.Close()

	loader, bs, q := setupProbeEnv(t, ctx, pool, rdb,
		llm.URL, stt.URL, embed.URL,
		breaker.Options{ConsecutiveFailures: 50, Cooldown: 30 * time.Second},
	)

	p := upstreams.NewProbe(loader, bs, q, upstreams.ProbeConfig{
		Interval: 500 * time.Millisecond,
		Budget:   2 * time.Second,
	}, discardLogger())

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	probeDone := make(chan struct{})
	go func() {
		defer close(probeDone)
		p.Run(runCtx)
	}()

	// Wait long enough for first tick + flush cycle (1s).
	deadline := time.Now().Add(5 * time.Second)
	var localLLMStatus string
	for time.Now().Before(deadline) {
		err := pool.QueryRow(ctx,
			`SELECT COALESCE(last_probe_status, '') FROM ai_gateway.upstreams WHERE name='local-llm'`).Scan(&localLLMStatus)
		if err == nil && localLLMStatus == "ok" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if localLLMStatus != "ok" {
		t.Fatalf("local-llm last_probe_status = %q, want 'ok' within 5s", localLLMStatus)
	}

	// last_probe_ms must be populated (>=0).
	var lastMs int32
	if err := pool.QueryRow(ctx,
		`SELECT last_probe_ms FROM ai_gateway.upstreams WHERE name='local-llm'`).Scan(&lastMs); err != nil {
		t.Fatalf("read last_probe_ms: %v", err)
	}
	if lastMs < 0 {
		t.Errorf("last_probe_ms = %d, want >=0", lastMs)
	}

	runCancel()
	select {
	case <-probeDone:
	case <-time.After(10 * time.Second):
		t.Fatal("probe goroutine did not exit within 10s of ctx cancel")
	}
}
