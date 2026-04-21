package billing

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgxlisten"
)

// ListenAndReload subscribes to NOTIFY prices_changed AND fx_changed on a
// single dedicated pgx connection (pgxlisten multiplexes; mirrors
// upstreams/listen.go pattern). Each notification triggers the corresponding
// loader.Refresh. Blocks until ctx cancel; pgxlisten handles reconnect
// internally with the 5s ReconnectDelay.
//
// dsn is the libpq connection string (typically cfg.PGDSN). A fresh pgx.Conn
// is created per (re)connect so LISTEN does not pin a pgxpool slot.
//
// Returns ctx.Err() on graceful shutdown; returns the underlying pgxlisten
// error only if ctx is still alive (true unrecoverable failure).
func ListenAndReload(ctx context.Context, dsn string, prices *PricesLoader, fx *FXLoader, log *slog.Logger) error {
	log = log.With("module", "PRICES_LISTEN")
	listener := &pgxlisten.Listener{
		Connect: func(ctx context.Context) (*pgx.Conn, error) {
			return pgx.Connect(ctx, dsn)
		},
		LogError: func(_ context.Context, err error) {
			log.Warn("pgxlisten error", "err", err)
		},
		ReconnectDelay: 5 * time.Second,
	}
	listener.Handle("prices_changed", pgxlisten.HandlerFunc(
		func(ctx context.Context, n *pgconn.Notification, _ *pgx.Conn) error {
			log.Info("prices_changed NOTIFY received", "payload", n.Payload)
			if prices == nil {
				return nil
			}
			if err := prices.Refresh(ctx); err != nil {
				log.Error("prices refresh after NOTIFY failed", "err", err)
				// Returning nil keeps the listener alive — transient DB
				// hiccups must not stop hot-reload after recovery.
				return nil
			}
			return nil
		},
	))
	listener.Handle("fx_changed", pgxlisten.HandlerFunc(
		func(ctx context.Context, n *pgconn.Notification, _ *pgx.Conn) error {
			log.Info("fx_changed NOTIFY received", "payload", n.Payload)
			if fx == nil {
				return nil
			}
			if err := fx.Refresh(ctx); err != nil {
				log.Error("fx refresh after NOTIFY failed", "err", err)
				return nil
			}
			return nil
		},
	))
	log.Info("starting LISTEN prices_changed + fx_changed")
	err := listener.Listen(ctx)
	if err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
		return err
	}
	log.Info("LISTEN loop exiting", "ctx_err", ctx.Err())
	return ctx.Err()
}
