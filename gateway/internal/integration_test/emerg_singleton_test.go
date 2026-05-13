//go:build integration

// Phase 6 Plan 06-02 Task 2 — partial unique index PRV-05/D-B5 invariant.
//
// Proves the `emergency_live_singleton` partial unique index rejects a 2nd
// row with `ended_at IS NULL`, defending against the split-brain scenario
// where two replicas both believe they hold leader-election. This is the
// last-line defense (defense-in-depth alongside redsync leader-election +
// reconciler pre-flight check D-C5).
package integration

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestEmergSingletonDBIndex proves PRV-05 "no duplicate live lifecycles" via
// the database-level partial unique index. Insert N°1 succeeds; Insert N°2
// (with ended_at IS NULL) fails with a unique constraint violation that
// names the `emergency_live_singleton` index. After closing the first
// lifecycle (set ended_at), Insert N°3 succeeds — proving the index only
// constrains the live slice.
func TestEmergSingletonDBIndex(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)

	// 1. First live lifecycle — succeeds.
	if _, err := pool.Exec(ctx,
		`INSERT INTO ai_gateway.emergency_lifecycles (trigger_reason) VALUES ('manual_force')`); err != nil {
		t.Fatalf("insert 1st live lifecycle: %v", err)
	}

	// 2. Second live lifecycle (ended_at IS NULL) — MUST violate
	// emergency_live_singleton index.
	_, err := pool.Exec(ctx,
		`INSERT INTO ai_gateway.emergency_lifecycles (trigger_reason) VALUES ('manual_force')`)
	if err == nil {
		t.Fatalf("insert 2nd live lifecycle should have failed but succeeded — partial unique index emergency_live_singleton is not enforcing PRV-05/D-B5")
	}
	if !strings.Contains(err.Error(), "emergency_live_singleton") {
		t.Errorf("error does not name the singleton index: %v", err)
	}

	// 3. Close the first lifecycle (set ended_at) — releases the partial
	// unique slot.
	res, err := pool.Exec(ctx, `
		UPDATE ai_gateway.emergency_lifecycles
		SET ended_at = NOW(), shutdown_reason = 'manual'
		WHERE id = (SELECT MIN(id) FROM ai_gateway.emergency_lifecycles)`)
	if err != nil {
		t.Fatalf("close 1st lifecycle: %v", err)
	}
	if res.RowsAffected() != 1 {
		t.Fatalf("close 1st lifecycle: rows affected got %d want 1", res.RowsAffected())
	}

	// 4. Third live lifecycle now succeeds — index only constrains ended_at IS NULL.
	if _, err := pool.Exec(ctx,
		`INSERT INTO ai_gateway.emergency_lifecycles (trigger_reason) VALUES ('failed_over_sustained')`); err != nil {
		t.Fatalf("insert 3rd lifecycle (after closing 1st) should succeed: %v", err)
	}

	// 5. Sanity: row count = 2 (1 ended + 1 live), live count = 1.
	var totalRows, liveRows int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM ai_gateway.emergency_lifecycles`).Scan(&totalRows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if totalRows != 2 {
		t.Errorf("total rows: got %d want 2", totalRows)
	}
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM ai_gateway.emergency_lifecycles WHERE ended_at IS NULL`).Scan(&liveRows); err != nil {
		t.Fatalf("count live rows: %v", err)
	}
	if liveRows != 1 {
		t.Errorf("live rows: got %d want 1 (PRV-05 invariant violated)", liveRows)
	}
}
