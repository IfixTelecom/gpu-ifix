//go:build integration

// Phase 6 Plan 06-02 Task 2 — migration 0019 schema completeness.
//
// Verifies that goose-applied migration 0019_emergency_lifecycles.sql produces
// the expected ai_gateway.emergency_lifecycles table with 12 columns, ≥5
// indexes (PK + 4 created), and a COMMENT on first_health_pass_at that
// references D-D4 cost calculation. Reseats migration via freshSchema().
package integration

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestMigration0019 asserts the emergency_lifecycles schema landed correctly
// after the standard freshSchema() goose Up cycle. PRV-10 requires this table
// to exist as the durable audit log; downstream plans (06-04, 06-06, 06-07,
// 06-08, 06-09, 06-10) consume the columns asserted here.
func TestMigration0019(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)

	// 1. Table exists in ai_gateway schema.
	var tableExists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE c.relname = 'emergency_lifecycles' AND n.nspname = 'ai_gateway'
		)`).Scan(&tableExists); err != nil {
		t.Fatalf("query pg_class: %v", err)
	}
	if !tableExists {
		t.Fatalf("ai_gateway.emergency_lifecycles does not exist after migration")
	}

	// 2. Exactly the 12 expected columns in the right order.
	wantColumns := []string{
		"id",
		"started_at",
		"first_health_pass_at",
		"ended_at",
		"trigger_reason",
		"vast_offer_id",
		"vast_instance_id",
		"accepted_dph",
		"total_cost_brl",
		"shutdown_reason",
		"events",
		"leader_replica",
	}
	rows, err := pool.Query(ctx, `
		SELECT column_name
		FROM information_schema.columns
		WHERE table_schema = 'ai_gateway'
		  AND table_name   = 'emergency_lifecycles'
		ORDER BY ordinal_position`)
	if err != nil {
		t.Fatalf("query information_schema.columns: %v", err)
	}
	var gotColumns []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan column_name: %v", err)
		}
		gotColumns = append(gotColumns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if len(gotColumns) != len(wantColumns) {
		t.Fatalf("column count: got %d want %d (got=%v)",
			len(gotColumns), len(wantColumns), gotColumns)
	}
	for i, want := range wantColumns {
		if gotColumns[i] != want {
			t.Errorf("column[%d]: got %q want %q", i, gotColumns[i], want)
		}
	}

	// 3. Critical column existence: first_health_pass_at TIMESTAMPTZ
	// (Pitfall 15 — D-D4 cost calc depends on this).
	var firstHealthDataType string
	if err := pool.QueryRow(ctx, `
		SELECT data_type
		FROM information_schema.columns
		WHERE table_schema = 'ai_gateway'
		  AND table_name   = 'emergency_lifecycles'
		  AND column_name  = 'first_health_pass_at'`).Scan(&firstHealthDataType); err != nil {
		t.Fatalf("query first_health_pass_at: %v", err)
	}
	if firstHealthDataType != "timestamp with time zone" {
		t.Errorf("first_health_pass_at data_type: got %q want %q",
			firstHealthDataType, "timestamp with time zone")
	}

	// 4. ≥5 indexes on the table (PK + 4 created in migration).
	var indexNames []string
	idxRows, err := pool.Query(ctx, `
		SELECT indexname FROM pg_indexes
		WHERE schemaname = 'ai_gateway'
		  AND tablename  = 'emergency_lifecycles'
		ORDER BY indexname`)
	if err != nil {
		t.Fatalf("query pg_indexes: %v", err)
	}
	for idxRows.Next() {
		var n string
		if err := idxRows.Scan(&n); err != nil {
			t.Fatalf("scan indexname: %v", err)
		}
		indexNames = append(indexNames, n)
	}
	if err := idxRows.Err(); err != nil {
		t.Fatalf("idxRows.Err: %v", err)
	}
	if len(indexNames) < 5 {
		t.Errorf("index count: got %d want ≥5 (got=%v)", len(indexNames), indexNames)
	}
	wantIndexes := map[string]bool{
		"emergency_live_singleton":            false,
		"idx_emergency_lifecycles_started_at": false,
		"idx_emergency_lifecycles_live":       false,
		"idx_emergency_lifecycles_month_cost": false,
	}
	for _, n := range indexNames {
		if _, ok := wantIndexes[n]; ok {
			wantIndexes[n] = true
		}
	}
	for n, found := range wantIndexes {
		if !found {
			t.Errorf("required index missing: %s (got indexes=%v)", n, indexNames)
		}
	}

	// 5. COMMENT on first_health_pass_at references D-D4 cost calc.
	var firstHealthComment *string
	if err := pool.QueryRow(ctx, `
		SELECT col_description(c.oid, a.attnum)
		FROM pg_class c
		JOIN pg_namespace n  ON n.oid = c.relnamespace
		JOIN pg_attribute a  ON a.attrelid = c.oid
		WHERE n.nspname = 'ai_gateway'
		  AND c.relname = 'emergency_lifecycles'
		  AND a.attname = 'first_health_pass_at'`).Scan(&firstHealthComment); err != nil {
		t.Fatalf("query col_description: %v", err)
	}
	if firstHealthComment == nil {
		t.Fatalf("COMMENT on first_health_pass_at is missing — required for D-D4 traceability")
	}
	if !strings.Contains(*firstHealthComment, "D-D4") {
		t.Errorf("COMMENT on first_health_pass_at does not reference D-D4: %q", *firstHealthComment)
	}
	if !strings.Contains(*firstHealthComment, "cost calc") {
		t.Errorf("COMMENT on first_health_pass_at does not mention cost calc: %q", *firstHealthComment)
	}

	// 6. COMMENT on table mentions PRV-10.
	var tableComment *string
	if err := pool.QueryRow(ctx, `
		SELECT obj_description('ai_gateway.emergency_lifecycles'::regclass)`).Scan(&tableComment); err != nil {
		t.Fatalf("query obj_description: %v", err)
	}
	if tableComment == nil || !strings.Contains(*tableComment, "PRV-10") {
		got := "<nil>"
		if tableComment != nil {
			got = *tableComment
		}
		t.Errorf("table COMMENT does not reference PRV-10: %q", got)
	}
}
