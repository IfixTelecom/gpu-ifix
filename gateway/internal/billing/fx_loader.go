package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

// fxSnapshot is the immutable in-memory view served by FXLoader.Get.
type fxSnapshot struct {
	byPair map[string]FXRate
}

// fxQueries isolates the sqlc surface for tests.
type fxQueries interface {
	GetCurrentFX(ctx context.Context, currencyPair string) (gen.AiGatewayFxRate, error)
}

// FXLoader holds the in-memory authoritative snapshot of
// ai_gateway.fx_rates (valid_to IS NULL). Currently only USD/BRL is
// tracked; the snapshot shape is future-proof for additional pairs.
type FXLoader struct {
	pool *pgxpool.Pool
	q    fxQueries
	snap atomic.Pointer[fxSnapshot]
	log  *slog.Logger
}

// NewFXLoader constructs the loader and performs the initial Refresh.
// Missing rows are NOT fatal — ComputeCostBRL falls back to cfg.USDBRLDefault
// so gateway can boot in a fresh environment before operator seeds fx.
func NewFXLoader(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) (*FXLoader, error) {
	l := &FXLoader{
		pool: pool,
		q:    gen.New(pool),
		log:  log.With("module", "FX"),
	}
	if err := l.Refresh(ctx); err != nil {
		return nil, fmt.Errorf("billing: initial fx refresh: %w", err)
	}
	return l, nil
}

// Refresh reads the currently-active row for each known currency_pair and
// atomically swaps the snapshot. A missing row logs WARN and continues;
// ComputeCostBRL falls back to the caller-supplied default.
func (l *FXLoader) Refresh(ctx context.Context) error {
	s := &fxSnapshot{byPair: make(map[string]FXRate, 1)}
	for _, pair := range []string{"USD/BRL"} {
		row, err := l.q.GetCurrentFX(ctx, pair)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				l.log.Warn("no active fx row; loader.Get will return false -> caller falls back to default",
					"pair", pair)
				continue
			}
			obs.GatewayPricesReload.WithLabelValues("error").Inc()
			return fmt.Errorf("billing: fx get %s: %w", pair, err)
		}
		f, ferr := row.Rate.Float64Value()
		if ferr != nil || !f.Valid {
			l.log.Warn("fx row has invalid rate; skipped",
				"pair", row.CurrencyPair, "err", ferr)
			continue
		}
		s.byPair[pair] = FXRate{
			CurrencyPair: row.CurrencyPair,
			Rate:         f.Float64,
			ValidFrom:    row.ValidFrom,
		}
	}
	l.snap.Store(s)
	obs.GatewayPricesReload.WithLabelValues("ok").Inc()
	l.log.Info("fx refreshed", "pairs", len(s.byPair))
	return nil
}

// Get returns the active FXRate for a currency_pair. Lock-free.
func (l *FXLoader) Get(pair string) (FXRate, bool) {
	s := l.snap.Load()
	if s == nil {
		return FXRate{}, false
	}
	r, ok := s.byPair[pair]
	return r, ok
}
