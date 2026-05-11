// Package main: unit tests for `gatewayctl tenant set-shed-limits` flag
// validation. Round-trip behavior (sqlc.narg UPDATE preserving unset
// columns) lives in the integration suite under build tag
// `integration` in tenants_shed_integration_test.go.
package main

import (
	"context"
	"log/slog"
	"testing"
)

// TestRunTenantSetShedLimits_MissingSlug asserts that the --slug flag
// is required.
func TestRunTenantSetShedLimits_MissingSlug(t *testing.T) {
	ctx := context.Background()
	if code := runTenantSetShedLimits(ctx, []string{"--llm", "4"}, slog.Default()); code != 2 {
		t.Fatalf("expected exit 2 without --slug, got %d", code)
	}
}

// TestRunTenantSetShedLimits_NoFlagsIsError asserts that calling with
// only --slug (no shed-limit flags) is a usage error.
func TestRunTenantSetShedLimits_NoFlagsIsError(t *testing.T) {
	ctx := context.Background()
	if code := runTenantSetShedLimits(ctx, []string{"--slug", "converseai"}, slog.Default()); code != 2 {
		t.Fatalf("expected exit 2 with no flags, got %d", code)
	}
}

// TestRunTenantSetShedLimits_InvalidTier asserts the --tier flag is
// restricted to S|A|B (the priority_tier domain).
func TestRunTenantSetShedLimits_InvalidTier(t *testing.T) {
	ctx := context.Background()
	for _, bad := range []string{"Z", "X", "C", "1", "AAA"} {
		if code := runTenantSetShedLimits(ctx, []string{"--slug", "converseai", "--tier", bad}, slog.Default()); code != 2 {
			t.Fatalf("expected exit 2 for --tier=%s, got %d", bad, code)
		}
	}
}

// TestRunTenantSetShedLimits_RejectsLLMZero asserts the --llm flag is
// rejected at 0 (operator typo guard — zero means "always saturated").
func TestRunTenantSetShedLimits_RejectsLLMZero(t *testing.T) {
	ctx := context.Background()
	if code := runTenantSetShedLimits(ctx, []string{"--slug", "converseai", "--llm", "0"}, slog.Default()); code != 2 {
		t.Fatalf("expected exit 2 for --llm=0, got %d", code)
	}
}

// TestRunTenantSetShedLimits_RejectsLLMOverflow asserts the --llm flag
// is capped at 1000.
func TestRunTenantSetShedLimits_RejectsLLMOverflow(t *testing.T) {
	ctx := context.Background()
	if code := runTenantSetShedLimits(ctx, []string{"--slug", "converseai", "--llm", "10000"}, slog.Default()); code != 2 {
		t.Fatalf("expected exit 2 for --llm=10000, got %d", code)
	}
}

// TestRunTenantSetShedLimits_RejectsSTTOutOfRange asserts the --stt
// flag is constrained to [1, 1000].
func TestRunTenantSetShedLimits_RejectsSTTOutOfRange(t *testing.T) {
	ctx := context.Background()
	if code := runTenantSetShedLimits(ctx, []string{"--slug", "converseai", "--stt", "0"}, slog.Default()); code != 2 {
		t.Fatalf("expected exit 2 for --stt=0, got %d", code)
	}
	if code := runTenantSetShedLimits(ctx, []string{"--slug", "converseai", "--stt", "5000"}, slog.Default()); code != 2 {
		t.Fatalf("expected exit 2 for --stt=5000, got %d", code)
	}
}

// TestRunTenantSetShedLimits_RejectsEmbedOutOfRange asserts the
// --embed flag is constrained to [1, 1000].
func TestRunTenantSetShedLimits_RejectsEmbedOutOfRange(t *testing.T) {
	ctx := context.Background()
	if code := runTenantSetShedLimits(ctx, []string{"--slug", "converseai", "--embed", "0"}, slog.Default()); code != 2 {
		t.Fatalf("expected exit 2 for --embed=0, got %d", code)
	}
	if code := runTenantSetShedLimits(ctx, []string{"--slug", "converseai", "--embed", "1500"}, slog.Default()); code != 2 {
		t.Fatalf("expected exit 2 for --embed=1500, got %d", code)
	}
}

// TestRunTenantSetShedLimits_ValidTierAccepted_ButFailsAtDB sanity
// check: with a valid tier flag, validation passes (returns code 1
// when DB connect fails, not code 2 for usage).
//
// We point the DSN at a non-listening port so config.Load succeeds
// but db.NewPool fails fast.
func TestRunTenantSetShedLimits_ValidTierAccepted_ButFailsAtDB(t *testing.T) {
	t.Setenv("AI_GATEWAY_PG_DSN", "postgres://nope:nope@127.0.0.1:1/x?sslmode=disable&connect_timeout=1")
	t.Setenv("AI_GATEWAY_REDIS_ADDR", "127.0.0.1:1")
	t.Setenv("UPSTREAM_LLM_URL", "http://x")
	t.Setenv("UPSTREAM_STT_URL", "http://x")
	t.Setenv("UPSTREAM_EMBED_URL", "http://x")
	t.Setenv("UPSTREAM_HEALTH_BRIDGE_URL", "http://x")
	ctx := context.Background()
	// Each valid tier value must pass the pre-DB validation. Code 1
	// indicates "validation passed, DB layer rejected" — that is the
	// signal we want.
	for _, tier := range []string{"S", "A", "B"} {
		code := runTenantSetShedLimits(ctx, []string{"--slug", "converseai", "--tier", tier}, slog.Default())
		if code == 2 {
			t.Fatalf("tier %q rejected by pre-DB validation; want code 1 (DB failure), got 2 (usage)", tier)
		}
	}
}
