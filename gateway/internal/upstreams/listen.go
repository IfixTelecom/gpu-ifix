package upstreams

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgxlisten"
)

// ListenAndReload opens a dedicated pgx.Conn (NOT pgxpool — pgxpool
// recycles connections and LISTEN state is connection-scoped; CONTEXT.md
// D-D4 / RESEARCH.md Pitfall 2), issues LISTEN upstreams_changed, and
// calls loader.Refresh + onReload on each NOTIFY received. Blocks until
// ctx is canceled. Reconnects with 5s backoff if the underlying
// connection drops (pgxlisten handles the loop internally).
//
// onReload is invoked AFTER loader.Refresh completes. Typically the
// caller uses it to rebuild the breaker.Set with loader.Names() so new
// upstreams get breakers and removed ones are dropped (Wave 2 — 03-05).
// Pass nil to skip the post-reload callback.
//
// dsn is the libpq connection string (typically cfg.PGDSN) — same DSN
// used by the gateway's main pgxpool, but a fresh pgx.Conn is created
// here so LISTEN does not pin a pool slot.
//
// Returns ctx.Err() on graceful shutdown; returns the underlying
// pgxlisten error only if ctx is still alive (true unrecoverable
// failure — currently only "Listen: Connect is nil" or
// "Listen: No handlers", both impossible by construction here).
func ListenAndReload(ctx context.Context, dsn string, loader *Loader, onReload func(), log *slog.Logger) error {
	log = log.With("module", "LISTEN")
	listener := &pgxlisten.Listener{
		Connect: func(ctx context.Context) (*pgx.Conn, error) {
			return pgx.Connect(ctx, dsn)
		},
		LogError: func(_ context.Context, err error) {
			log.Warn("pgxlisten error", "err", err)
		},
		ReconnectDelay: 5 * time.Second,
	}
	listener.Handle("upstreams_changed", pgxlisten.HandlerFunc(
		func(ctx context.Context, n *pgconn.Notification, _ *pgx.Conn) error {
			log.Info("upstreams_changed NOTIFY received", "payload", n.Payload)
			if err := loader.Refresh(ctx); err != nil {
				log.Error("loader refresh after NOTIFY failed", "err", err)
				// Returning nil keeps the listener alive — pgxlisten only
				// logs the handler error; an erroring handler MUST NOT
				// take the listen loop down (transient DB hiccup must
				// not stop hot-reload after recovery).
				return nil
			}
			if onReload != nil {
				onReload()
			}
			return nil
		},
	))
	log.Info("starting LISTEN upstreams_changed")
	err := listener.Listen(ctx)
	if err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
		return err
	}
	log.Info("LISTEN loop exiting", "ctx_err", ctx.Err())
	return ctx.Err()
}
