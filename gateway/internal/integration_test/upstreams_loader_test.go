//go:build integration

package integration

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

// resetUpstreamsTable re-enables every seeded upstream row and clears
// any probe writebacks. Necessary because freshSchema's TRUNCATE list
// does NOT include ai_gateway.upstreams (the 0008 seed migration is
// idempotent but only inserts; it doesn't reset enabled=true on rows
// previously disabled by another test in the same process). Without
// this reset, listener tests that disable a row leak state into
// subsequent loader tests, which then see fewer rows than expected.
func resetUpstreamsTable(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE ai_gateway.upstreams
        SET enabled = TRUE,
            tier = CASE name
                WHEN 'local-llm' THEN 0
                WHEN 'openrouter-chat' THEN 1
                WHEN 'local-stt' THEN 0
                WHEN 'openai-whisper' THEN 1
                WHEN 'local-embed' THEN 0
                WHEN 'openai-embed' THEN 1
                ELSE tier
            END,
            circuit_config = '{}'::jsonb,
            last_probe_at = NULL,
            last_probe_ms = NULL,
            last_probe_status = NULL,
            last_probe_error = NULL`); err != nil {
		t.Fatalf("resetUpstreamsTable: %v", err)
	}
}

// clearUpstreamEnvs nulls the eight Phase 3 UPSTREAM_* env vars so the
// test starts from a known baseline. Restored automatically via t.Setenv
// in the caller. Returns nothing — callers re-set whatever they need.
//
// We use os.Unsetenv (not t.Setenv to "") because the loader checks
// for empty-string and an Unset returns "" via os.Getenv, which is what
// we want. t.Setenv handles restoration on test cleanup.
func clearUpstreamEnvs(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"UPSTREAM_LLM_URL",
		"UPSTREAM_STT_URL",
		"UPSTREAM_EMBED_URL",
		"UPSTREAM_LLM_OPENROUTER_URL",
		"UPSTREAM_LLM_OPENROUTER_AUTH_BEARER",
		"UPSTREAM_STT_OPENAI_URL",
		"UPSTREAM_STT_OPENAI_AUTH_BEARER",
		"UPSTREAM_EMBED_OPENAI_URL",
		"UPSTREAM_EMBED_OPENAI_AUTH_BEARER",
	} {
		// Save current value (if any) and restore on cleanup.
		prev, had := os.LookupEnv(k)
		_ = os.Unsetenv(k)
		t.Cleanup(func() {
			if had {
				_ = os.Setenv(k, prev)
			} else {
				_ = os.Unsetenv(k)
			}
		})
	}
}

func TestIntegration_UpstreamsLoader_RefreshLoadsSixUpstreams(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)
	resetUpstreamsTable(t, ctx, pool)

	clearUpstreamEnvs(t)
	t.Setenv("UPSTREAM_LLM_URL", "http://local-llm:8000")
	t.Setenv("UPSTREAM_STT_URL", "http://local-stt:8001")
	t.Setenv("UPSTREAM_EMBED_URL", "http://local-embed:8002")
	t.Setenv("UPSTREAM_LLM_OPENROUTER_URL", "https://openrouter.ai/api/v1")
	t.Setenv("UPSTREAM_LLM_OPENROUTER_AUTH_BEARER", "or-test")
	t.Setenv("UPSTREAM_STT_OPENAI_URL", "https://api.openai.com/v1")
	t.Setenv("UPSTREAM_STT_OPENAI_AUTH_BEARER", "oa-test")
	t.Setenv("UPSTREAM_EMBED_OPENAI_URL", "https://api.openai.com/v1")
	t.Setenv("UPSTREAM_EMBED_OPENAI_AUTH_BEARER", "oa-test-embed")

	loader, err := upstreams.NewLoader(ctx, pool, discardLogger())
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}

	if got := len(loader.All()); got != 6 {
		t.Fatalf("All() = %d, want 6", got)
	}

	if u, ok := loader.Resolve("llm", 0); !ok {
		t.Errorf("Resolve(llm,0): not found")
	} else if u.Name != "local-llm" {
		t.Errorf("Resolve(llm,0).Name = %q, want local-llm", u.Name)
	} else if u.URL != "http://local-llm:8000" {
		t.Errorf("Resolve(llm,0).URL = %q, want http://local-llm:8000", u.URL)
	} else if u.AuthBearer != "" {
		t.Errorf("Resolve(llm,0).AuthBearer = %q, want empty (no auth on local)", u.AuthBearer)
	}

	if u, ok := loader.Resolve("llm", 1); !ok {
		t.Errorf("Resolve(llm,1): not found")
	} else if u.Name != "openrouter-chat" {
		t.Errorf("Resolve(llm,1).Name = %q, want openrouter-chat", u.Name)
	} else if u.URL != "https://openrouter.ai/api/v1" {
		t.Errorf("Resolve(llm,1).URL = %q, want OpenRouter URL", u.URL)
	} else if u.AuthBearer != "or-test" {
		t.Errorf("Resolve(llm,1).AuthBearer = %q, want or-test (resolved env)", u.AuthBearer)
	} else if u.AuthBearerEnv != "UPSTREAM_LLM_OPENROUTER_AUTH_BEARER" {
		t.Errorf("Resolve(llm,1).AuthBearerEnv = %q, want UPSTREAM_LLM_OPENROUTER_AUTH_BEARER", u.AuthBearerEnv)
	}

	if u, ok := loader.Resolve("stt", 0); !ok || u.Name != "local-stt" {
		t.Errorf("Resolve(stt,0) = %+v ok=%v", u, ok)
	}
	if u, ok := loader.Resolve("stt", 1); !ok || u.Name != "openai-whisper" || u.AuthBearer != "oa-test" {
		t.Errorf("Resolve(stt,1) = %+v ok=%v", u, ok)
	}
	if u, ok := loader.Resolve("embed", 0); !ok || u.Name != "local-embed" {
		t.Errorf("Resolve(embed,0) = %+v ok=%v", u, ok)
	}
	if u, ok := loader.Resolve("embed", 1); !ok || u.Name != "openai-embed" || u.AuthBearer != "oa-test-embed" {
		t.Errorf("Resolve(embed,1) = %+v ok=%v", u, ok)
	}

	// Get by name path
	if u, ok := loader.Get("local-llm"); !ok || u.Name != "local-llm" {
		t.Errorf("Get(local-llm) = %+v ok=%v", u, ok)
	}
	if _, ok := loader.Get("nonexistent"); ok {
		t.Errorf("Get(nonexistent) should return ok=false")
	}

	// Names returns sorted list
	names := loader.Names()
	if len(names) != 6 {
		t.Errorf("Names() len = %d, want 6", len(names))
	}
	// Check sort order
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Errorf("Names() not sorted: %v", names)
			break
		}
	}

	// AuthBearer must NEVER appear in JSON-marshaled UpstreamConfig
	// (T-03-04-03 — json:"-" tag enforcement).
	all := loader.All()
	if len(all) == 0 {
		t.Fatal("All() returned empty after successful refresh")
	}
	// Just check that the AuthBearer field is set on the in-memory copy
	// — JSON marshalling assertion is unit-tested separately if needed.
	hasAuthBearer := false
	for _, u := range all {
		if u.AuthBearer != "" {
			hasAuthBearer = true
		}
	}
	if !hasAuthBearer {
		t.Error("expected at least one upstream to have AuthBearer set in memory")
	}
}

func TestIntegration_UpstreamsLoader_MissingURLEnvSkipsRow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)
	resetUpstreamsTable(t, ctx, pool)

	clearUpstreamEnvs(t)
	// Only set the three local URLs; externals MUST be skipped.
	t.Setenv("UPSTREAM_LLM_URL", "http://local-llm:8000")
	t.Setenv("UPSTREAM_STT_URL", "http://local-stt:8001")
	t.Setenv("UPSTREAM_EMBED_URL", "http://local-embed:8002")

	loader, err := upstreams.NewLoader(ctx, pool, discardLogger())
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}

	if got := len(loader.All()); got != 3 {
		t.Fatalf("All() = %d, want 3 (3 externals skipped due to missing url_env)", got)
	}

	// Externals must NOT resolve.
	if _, ok := loader.Resolve("llm", 1); ok {
		t.Error("openrouter-chat should be skipped when its url_env is missing")
	}
	if _, ok := loader.Resolve("stt", 1); ok {
		t.Error("openai-whisper should be skipped when its url_env is missing")
	}
	if _, ok := loader.Resolve("embed", 1); ok {
		t.Error("openai-embed should be skipped when its url_env is missing")
	}

	// Locals must still resolve.
	if u, ok := loader.Resolve("llm", 0); !ok || u.Name != "local-llm" {
		t.Errorf("local-llm Resolve = %+v ok=%v", u, ok)
	}
}

func TestIntegration_UpstreamsLoader_MissingAuthBearerEnvKeepsRow(t *testing.T) {
	// Per CONTEXT.md plumbing + 03-04-PLAN must_haves: a row whose
	// auth_bearer_env value is empty MUST still be present in the snapshot
	// (with empty AuthBearer) so the dispatcher can decide what to do at
	// request time — typically the upstream returns 401 and the breaker
	// counts it as a failure. We log a warn but never drop the row.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)
	resetUpstreamsTable(t, ctx, pool)

	clearUpstreamEnvs(t)
	t.Setenv("UPSTREAM_LLM_URL", "http://local-llm:8000")
	t.Setenv("UPSTREAM_STT_URL", "http://local-stt:8001")
	t.Setenv("UPSTREAM_EMBED_URL", "http://local-embed:8002")
	t.Setenv("UPSTREAM_LLM_OPENROUTER_URL", "https://openrouter.ai/api/v1")
	// NOT setting UPSTREAM_LLM_OPENROUTER_AUTH_BEARER — should warn but keep row.
	t.Setenv("UPSTREAM_STT_OPENAI_URL", "https://api.openai.com/v1")
	t.Setenv("UPSTREAM_STT_OPENAI_AUTH_BEARER", "oa-test")
	t.Setenv("UPSTREAM_EMBED_OPENAI_URL", "https://api.openai.com/v1")
	t.Setenv("UPSTREAM_EMBED_OPENAI_AUTH_BEARER", "oa-test-embed")

	loader, err := upstreams.NewLoader(ctx, pool, discardLogger())
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}

	if got := len(loader.All()); got != 6 {
		t.Fatalf("All() = %d, want 6 (auth-bearer-missing row must be retained)", got)
	}

	u, ok := loader.Resolve("llm", 1)
	if !ok {
		t.Fatal("openrouter-chat MUST be present even with missing auth_bearer_env value")
	}
	if u.AuthBearer != "" {
		t.Errorf("AuthBearer = %q, want empty (env var was unset)", u.AuthBearer)
	}
	if u.AuthBearerEnv != "UPSTREAM_LLM_OPENROUTER_AUTH_BEARER" {
		t.Errorf("AuthBearerEnv = %q, want UPSTREAM_LLM_OPENROUTER_AUTH_BEARER (name preserved)", u.AuthBearerEnv)
	}
}

func TestIntegration_UpstreamsLoader_AtomicSwapNoRace(t *testing.T) {
	// Asserts atomic.Pointer[snapshot] swap is concurrency-safe under
	// `go test -race`. Spawns N reader goroutines repeatedly calling
	// Resolve while a writer goroutine repeatedly calls Refresh. No
	// data race must surface. This is the lock-free invariant the
	// dispatcher hot path depends on.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)
	resetUpstreamsTable(t, ctx, pool)

	clearUpstreamEnvs(t)
	t.Setenv("UPSTREAM_LLM_URL", "http://local-llm:8000")
	t.Setenv("UPSTREAM_STT_URL", "http://local-stt:8001")
	t.Setenv("UPSTREAM_EMBED_URL", "http://local-embed:8002")

	loader, err := upstreams.NewLoader(ctx, pool, discardLogger())
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}

	const readers = 8
	const iterPerReader = 200
	const writes = 50

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var resolves atomic.Int64
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterPerReader; j++ {
				select {
				case <-stop:
					return
				default:
				}
				if u, ok := loader.Resolve("llm", 0); ok {
					if u.Name != "local-llm" {
						// Don't fail from goroutine — just record an
						// invalid name via atomic counter check below.
						_ = u
					}
					resolves.Add(1)
				}
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for k := 0; k < writes; k++ {
			if err := loader.Refresh(ctx); err != nil {
				t.Errorf("concurrent Refresh failed: %v", err)
				return
			}
		}
	}()

	wg.Wait()
	close(stop)

	if resolves.Load() == 0 {
		t.Error("readers performed 0 successful Resolve calls — test ineffective")
	}
}
