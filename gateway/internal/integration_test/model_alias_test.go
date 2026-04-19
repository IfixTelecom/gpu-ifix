//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
)

// TestIntegration_05_ModelAlias verifies the resolver reads aliases from
// Postgres, returns expected targets, and picks up new rows on Refresh.
func TestIntegration_05_ModelAlias(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)

	r := models.NewResolver(pool, discardLogger())
	if err := r.Refresh(ctx); err != nil {
		t.Fatal(err)
	}

	// Seeded: qwen (llm) → qwen, whisper (stt) → Systran/faster-whisper-large-v3,
	// bge-m3 (embed) → BAAI/bge-m3. Resolve takes (alias, upstream).
	cases := []struct{ alias, upstream, want string }{
		{"qwen", "llm", "qwen"},
		{"whisper", "stt", "Systran/faster-whisper-large-v3"},
		{"bge-m3", "embed", "BAAI/bge-m3"},
		// Unknown alias → pass-through (pod decides).
		{"unknown", "llm", "unknown"},
	}
	for _, c := range cases {
		if got := r.Resolve(c.alias, c.upstream); got != c.want {
			t.Errorf("Resolve(%q,%q) got %q want %q", c.alias, c.upstream, got, c.want)
		}
	}

	// Add a new alias via SQL, call Refresh again, verify picked up.
	if _, err := pool.Exec(ctx,
		`INSERT INTO ai_gateway.model_aliases (alias, upstream, target) VALUES ('gpt4o','llm','gpt4o-v2')`); err != nil {
		t.Fatal(err)
	}
	if err := r.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got := r.Resolve("gpt4o", "llm"); got != "gpt4o-v2" {
		t.Errorf("post-refresh Resolve(gpt4o,llm) got %q want gpt4o-v2", got)
	}
}
