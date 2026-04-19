// Package db owns the gateway's Postgres connection pool and migration
// runner. Every package that talks to Postgres goes through the pgxpool
// created here. Schema isolation (CONTEXT.md D-D4) is enforced via an
// AfterConnect hook that sets search_path on every acquired connection.
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
)

// NewPool opens a pgxpool.Pool against cfg.PGDSN with search_path hooked
// to 'ai_gateway, public' on every connection acquired. Fail-fast: a bad
// DSN or unreachable Postgres surfaces at startup (Ping) rather than on
// the first request.
func NewPool(ctx context.Context, cfg config.Config) (*pgxpool.Pool, error) {
	pcfg, err := pgxpool.ParseConfig(cfg.PGDSN)
	if err != nil {
		return nil, fmt.Errorf("db: parse DSN: %w", err)
	}
	if cfg.PGMaxConns > 0 {
		pcfg.MaxConns = cfg.PGMaxConns
	} else {
		pcfg.MaxConns = 10
	}
	pcfg.MaxConnIdleTime = 5 * time.Minute
	pcfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		if _, err := conn.Exec(ctx, "SET search_path = ai_gateway, public"); err != nil {
			return fmt.Errorf("db: set search_path: %w", err)
		}
		// Register the project-specific enum types so sqlc's
		// `interface{}` columns scan to Go strings rather than erroring
		// with "cannot scan unknown type (OID ...) in text format into
		// *interface {}". Best-effort: the ENUMs may not yet exist when
		// pool initialization runs before the migrations (e.g. freshSchema
		// in integration tests creates the pool, then migrates). pgx
		// rebuilds the connection type map lazily on first scan, so if
		// LoadType fails here we leave it to pgx to fall back.
		if err := registerEnumTypes(ctx, conn); err != nil {
			// Do not fail fast — downstream sqlc-generated scans handle
			// unregistered types by returning an error that surfaces at
			// query time, not at connect time. This keeps bootstrapping
			// (fresh DB, no schema yet) working.
			_ = err
		}
		return nil
	}

	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("db: new pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return pool, nil
}

// enumTypes is the list of schema-qualified ENUM names the gateway code
// consumes via sqlc-generated `interface{}` scan targets. Registering
// them turns the scan into a Go `string` rather than leaving pgx with an
// unknown OID. See the comment in AfterConnect for why this is best-effort
// during bootstrap (ENUMs may not exist yet before migration 0002 runs).
var enumTypes = []string{
	"ai_gateway.api_key_status",
	"ai_gateway.data_class",
}

// registerEnumTypes loads each ENUM's OID via pgx's LoadType helper and
// registers it on the connection-local type map. Called from AfterConnect;
// errors are non-fatal at connect time (a fresh DB with no schema yet
// returns errors here — that's acceptable).
func registerEnumTypes(ctx context.Context, conn *pgx.Conn) error {
	tm := conn.TypeMap()
	for _, name := range enumTypes {
		t, err := conn.LoadType(ctx, name)
		if err != nil {
			return fmt.Errorf("db: load type %s: %w", name, err)
		}
		tm.RegisterType(t)
	}
	return nil
}

// Silence the unused-import check when registerEnumTypes is the only
// consumer of pgtype — keeps the linter quiet if the helper list shrinks.
var _ = pgtype.TextOID
