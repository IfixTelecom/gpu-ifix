package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultPartitionLookahead is the rolling window (months) for which
// EnsurePartitions creates missing partitions. N=3 covers current + 2 next,
// matching the seed migration 0003/0004.
const DefaultPartitionLookahead = 3

// EnsurePartitions creates monthly partitions of ai_gateway.audit_log and
// ai_gateway.audit_log_content for the window [truncMonth(now), now+nMonths].
// Idempotent (CREATE TABLE IF NOT EXISTS). Safe to call on every gateway
// boot. Addresses Codex review [LOW] 02-02 (partition automation).
func EnsurePartitions(ctx context.Context, pool *pgxpool.Pool, now time.Time, nMonths int) error {
	if nMonths <= 0 {
		nMonths = DefaultPartitionLookahead
	}
	now = now.UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i <= nMonths; i++ {
		m := start.AddDate(0, i, 0)
		end := m.AddDate(0, 1, 0)
		for _, table := range []string{"audit_log", "audit_log_content"} {
			partName := fmt.Sprintf("%s_%04d%02d", table, m.Year(), int(m.Month()))
			q := fmt.Sprintf(
				`CREATE TABLE IF NOT EXISTS ai_gateway.%s PARTITION OF ai_gateway.%s FOR VALUES FROM ('%s') TO ('%s')`,
				partName, table,
				m.Format("2006-01-02"), end.Format("2006-01-02"),
			)
			if _, err := pool.Exec(ctx, q); err != nil {
				return fmt.Errorf("db.EnsurePartitions: create %s: %w", partName, err)
			}
		}
	}
	return nil
}
