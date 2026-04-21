//go:build integration

package integration

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestBillingReconcileDrift — D-D4 reconcile:
//  1. Seed billing_events rows whose SUM drifts from usage_counters cache.
//  2. `gatewayctl billing reconcile` (no --apply) alarms with DRIFT + exit 1.
//  3. `gatewayctl billing reconcile --apply` rewrites usage_counters from
//     billing_events + exits 0.
//  4. Post-apply, usage_counters tokens_in matches the authoritative SUM.
func TestBillingReconcileDrift(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)
	seed := seedPhase4(t, ctx, pool)

	bin := buildGatewayctl(t)

	// Seed billing_events rows spread across today (different request_ids
	// so PK doesn't conflict). SUM tokens_in = 1100.
	loc, _ := time.LoadLocation("America/Sao_Paulo")
	nowSP := time.Now().In(loc)
	for i := 0; i < 5; i++ {
		ts := time.Date(nowSP.Year(), nowSP.Month(), nowSP.Day(), 4+i, 0, 0, 0, loc)
		if _, err := pool.Exec(ctx, `
			INSERT INTO ai_gateway.billing_events
				(request_id, ts, tenant_id, route, upstream, model,
				 tokens_in, tokens_out, audio_seconds, embeds_count,
				 cost_local_brl, cost_local_phantom_brl, cost_external_brl, source)
			VALUES ($1, $2, $3, 'chat', 'local-llm', 'qwen3.5-27b',
			        220, 440, 0, 0, 0, 0.000440, 0, 'final')
		`, uuid.New(), ts, seed.ConverseAITenantID); err != nil {
			t.Fatalf("seed billing row %d: %v", i, err)
		}
	}

	// Seed usage_counters today with tokens_in=1000 (drift = ~9%).
	todaySP := time.Date(nowSP.Year(), nowSP.Month(), nowSP.Day(), 0, 0, 0, 0, loc)
	if _, err := pool.Exec(ctx, `
		INSERT INTO ai_gateway.usage_counters
			(tenant_id, date, tokens_in, tokens_out, requests_count)
		VALUES ($1, $2::date, 1000, 2000, 5)
	`, seed.ConverseAITenantID, todaySP.Format("2006-01-02")); err != nil {
		t.Fatalf("seed usage_counters: %v", err)
	}

	// 1. Reconcile without --apply → non-zero exit + DRIFT message.
	env := append(os.Environ(),
		"AI_GATEWAY_PG_DSN="+sharedPGDSN,
		"AI_GATEWAY_REDIS_ADDR="+sharedRedisAddr,
		"UPSTREAM_LLM_URL=http://dummy",
		"UPSTREAM_STT_URL=http://dummy",
		"UPSTREAM_EMBED_URL=http://dummy",
		"UPSTREAM_HEALTH_BRIDGE_URL=http://dummy",
	)

	cmd := exec.CommandContext(ctx, bin, "billing", "reconcile")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	outStr := string(out)
	if err == nil {
		t.Fatalf("expected non-zero exit on drift; got success: %s", outStr)
	}
	if !strings.Contains(outStr, "DRIFT") {
		t.Errorf("expected DRIFT message in stderr, got: %s", outStr)
	}

	// 2. Reconcile --apply → exit 0 and counters rewritten to 1100 tokens_in.
	cmd2 := exec.CommandContext(ctx, bin, "billing", "reconcile", "--apply")
	cmd2.Env = env
	if out, err := cmd2.CombinedOutput(); err != nil {
		t.Fatalf("reconcile --apply: %v\n%s", err, out)
	}

	var newIn, newOut int64
	if err := pool.QueryRow(ctx, `
		SELECT tokens_in, tokens_out
		FROM ai_gateway.usage_counters
		WHERE tenant_id = $1 AND date = (now() AT TIME ZONE 'America/Sao_Paulo')::date
	`, seed.ConverseAITenantID).Scan(&newIn, &newOut); err != nil {
		t.Fatalf("post-apply read: %v", err)
	}
	if newIn != 1100 || newOut != 2200 {
		t.Errorf("post-apply usage_counters: want 1100/2200, got %d/%d", newIn, newOut)
	}
}
