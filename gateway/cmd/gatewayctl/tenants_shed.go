// Package main (tenants_shed.go): `gatewayctl tenant set-shed-limits`
// subcommand for tuning per-tenant fairness hard caps (CONTEXT.md D-B1
// / D-B2).
//
// Each tenant carries four columns introduced by migration 0016:
//
//	local_inflight_max_llm    INT  (per-tenant LLM cap)
//	local_inflight_max_stt    INT  (per-tenant STT cap)
//	local_inflight_max_embed  INT  (per-tenant embed cap)
//	priority_tier             TEXT in {S, A, B}  (metadata-only in v1)
//
// All flags are optional; the underlying sqlc query uses COALESCE so
// fields left at sentinel -1 (or empty string for tier) are preserved
// exactly. This is the EXACT pattern used by runTenantSetQuota in
// tenant.go (Phase 4): pgtype.Int4{Valid: true} marks "write this
// value", pgtype.Int4{Valid: false} marks "leave unchanged".
//
// NOTIFY tenants_changed fires automatically via the trigger extended
// in migration 0016 to cover the four new columns, so the running
// gateway hot-reloads the new caps within the SC-3 budget (<2s).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// runTenantSetShedLimits implements `gatewayctl tenant set-shed-limits`.
// Called from runTenant in tenant.go via the "set-shed-limits" dispatch
// case added by Plan 05-07.
//
// Range invariants enforced pre-DB:
//   - --llm / --stt / --embed: rejected at 0, capped at 1000 (typo guard)
//   - --tier: restricted to S|A|B (CHECK constraint already exists in DB,
//     but a CLI-side check produces a clearer error)
func runTenantSetShedLimits(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("tenant set-shed-limits", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	slug := fs.String("slug", "", "tenant slug (required)")
	llm := fs.Int("llm", -1, "local_inflight_max_llm (1..1000); -1 = unchanged")
	stt := fs.Int("stt", -1, "local_inflight_max_stt (1..1000); -1 = unchanged")
	embed := fs.Int("embed", -1, "local_inflight_max_embed (1..1000); -1 = unchanged")
	tier := fs.String("tier", "", "priority_tier {S|A|B}; empty = unchanged")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *slug == "" {
		fmt.Fprintln(os.Stderr, "--slug required")
		return 2
	}

	params := gen.UpdateTenantShedLimitsParams{Slug: *slug}
	anyFlag := false

	if *llm >= 0 {
		if *llm < 1 || *llm > 1000 {
			fmt.Fprintln(os.Stderr, "--llm must be in [1, 1000]")
			return 2
		}
		params.LocalInflightMaxLlm = pgtype.Int4{Int32: int32(*llm), Valid: true}
		anyFlag = true
	}
	if *stt >= 0 {
		if *stt < 1 || *stt > 1000 {
			fmt.Fprintln(os.Stderr, "--stt must be in [1, 1000]")
			return 2
		}
		params.LocalInflightMaxStt = pgtype.Int4{Int32: int32(*stt), Valid: true}
		anyFlag = true
	}
	if *embed >= 0 {
		if *embed < 1 || *embed > 1000 {
			fmt.Fprintln(os.Stderr, "--embed must be in [1, 1000]")
			return 2
		}
		params.LocalInflightMaxEmbed = pgtype.Int4{Int32: int32(*embed), Valid: true}
		anyFlag = true
	}
	if *tier != "" {
		if *tier != "S" && *tier != "A" && *tier != "B" {
			fmt.Fprintf(os.Stderr, "--tier must be S, A, or B (got %q)\n", *tier)
			return 2
		}
		params.PriorityTier = pgtype.Text{String: *tier, Valid: true}
		anyFlag = true
	}
	if !anyFlag {
		fmt.Fprintln(os.Stderr, "at least one flag required (--llm, --stt, --embed, --tier)")
		return 2
	}

	_, pool, err := loadAndPool(ctx, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer pool.Close()
	q := gen.New(pool)

	// Lookup the tenant first to surface a clean "not found" error
	// (UpdateTenantShedLimits does not report rows-affected, so a typo
	// in --slug would otherwise be a silent no-op).
	if _, err := q.GetTenantBySlug(ctx, *slug); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			fmt.Fprintf(os.Stderr, "tenant %q not found\n", *slug)
			return 1
		}
		fmt.Fprintf(os.Stderr, "lookup tenant: %v\n", err)
		return 1
	}

	if err := q.UpdateTenantShedLimits(ctx, params); err != nil {
		fmt.Fprintf(os.Stderr, "update failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "shed-limits updated: slug=%s\n", *slug)
	log.Info("tenant shed limits updated",
		"slug", *slug,
		"llm_set", params.LocalInflightMaxLlm.Valid,
		"stt_set", params.LocalInflightMaxStt.Valid,
		"embed_set", params.LocalInflightMaxEmbed.Valid,
		"tier_set", params.PriorityTier.Valid,
	)
	return 0
}
