//go:build integration

// Phase 06.9 Plan 05a Task 2 — EMBED-OAI-FIX end-to-end integration test.
//
// Closes the third per-upstream model-rewrite gap. Client POSTs
// /v1/embeddings with model="bge-m3" + input array; the gateway's
// embed director rewrites body.model to "text-embedding-3-small" (the
// schema-driven target from migration 0026) AND injects dimensions=1024
// (BGE-M3 parity invariant, explicitly NOT schema-driven). The `input`
// field MUST survive byte-identical.
//
// Flow mirrors the OpenRouter / Whisper tests: freshSchema → real
// resolver → tier0=newFailMock + tier1=newSuccessMockCapturing → director
// → dispatcher → trip local-embed breaker → POST → assert on captured body.
package integration

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/proxy"
)

// TestIntegration_OpenAIEmbedModelRewrite — base case (EMBED-OAI-FIX).
// Client POSTs {"model":"bge-m3","input":["test"]}; tier-1 receives
// {"model":"text-embedding-3-small","dimensions":1024,"input":["test"]}.
func TestIntegration_OpenAIEmbedModelRewrite(t *testing.T) {
	t.Setenv("UPSTREAM_EMBED_OPENAI_MODEL", "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, rdb := freshSchema(t, ctx)
	resolver := models.NewResolver(pool, discardLogger())
	if err := resolver.Refresh(ctx); err != nil {
		t.Fatalf("resolver.Refresh: %v", err)
	}

	tier0 := newFailMock(t)
	tier1 := newSuccessMockCapturing(t)

	tier1URL, _ := url.Parse(tier1.server.URL)
	director := proxy.BuildOpenAIEmbedDirector(
		tier1URL,
		"sk-openai-test-bearer",
		resolver,
		"openai-embed",
		discardLogger(),
	)
	tier1Proxy := &httputil.ReverseProxy{Director: director}

	loader := resilienceLoader("embed",
		"local-embed", tier0.server.URL,
		"openai-embed", tier1.server.URL,
	)
	bs := breaker.NewSet(rdb, discardLogger(),
		breaker.Options{ConsecutiveFailures: 2, Cooldown: 30 * time.Second},
		loader.Names(),
	)
	t0Proxy := newClassifyingProxy(t, tier0.server.URL, bs, "local-embed")

	disp := proxy.NewDispatcher(proxy.DispatcherConfig{
		Role:    "embed",
		Loader:  loader,
		Breaker: bs,
		Proxies: map[string]http.Handler{
			"local-embed":  t0Proxy,
			"openai-embed": tier1Proxy,
		},
		Log: slog.New(slog.NewTextHandler(discardWriter{}, nil)),
	})

	// Trip the local-embed breaker so the dispatcher routes to tier-1.
	driveBreaker(t, bs, "local-embed", 500, 3)

	rw := httptest.NewRecorder()
	bodyJSON := `{"model":"bge-m3","input":["test"]}`
	r := httptest.NewRequest(http.MethodPost, "/v1/embeddings",
		strings.NewReader(bodyJSON))
	r.Header.Set("Content-Type", "application/json")
	ctx2 := auth.WithContext(r.Context(), auth.AuthContext{
		TenantID:  "00000000-0000-0000-0000-000000000001",
		APIKeyID:  "00000000-0000-0000-0000-000000000002",
		DataClass: auth.DataClassNormal,
	})
	r = r.WithContext(ctx2)

	disp.ServeHTTP(rw, r)

	if got := tier1.hits.Load(); got < 1 {
		t.Fatalf("tier-1 hits = %d; want >= 1. status=%d body=%s",
			got, rw.Code, rw.Body.String())
	}

	captured := tier1.LastBody()
	if len(captured) == 0 {
		t.Fatalf("tier-1 captured body is empty")
	}
	var body map[string]any
	if err := json.Unmarshal(captured, &body); err != nil {
		t.Fatalf("captured body parse: %v; raw=%s", err, string(captured))
	}

	// PRIMARY assertion: model rewritten to schema target.
	gotModel, _ := body["model"].(string)
	if gotModel != "text-embedding-3-small" {
		t.Errorf("EMBED-OAI-FIX REGRESSION: forwarded model = %q, want %q (schema-driven rewrite)",
			gotModel, "text-embedding-3-small")
	}

	// dimensions=1024 invariant (NOT schema-driven; explicitly hard-coded).
	dim, _ := body["dimensions"].(float64) // JSON numbers decode as float64
	if dim != 1024 {
		t.Errorf("dimensions = %v, want 1024 (BGE-M3 parity invariant)", body["dimensions"])
	}

	// input array survives byte-identical (it's the load-bearing payload).
	inputs, ok := body["input"].([]any)
	if !ok {
		t.Errorf("input field lost or wrong shape; body: %s", string(captured))
	} else if len(inputs) != 1 || inputs[0] != "test" {
		t.Errorf("input = %v, want [\"test\"]", inputs)
	}

	t.Logf("EMBED-OAI-FIX VERIFIED: model rewritten to %q, dimensions=%v, input preserved",
		gotModel, body["dimensions"])
}

// Compile-time guard.
var _ = proxy.BuildOpenAIEmbedDirector
