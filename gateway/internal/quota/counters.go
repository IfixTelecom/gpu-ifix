// Package quota: quota lookup helpers wrapping the sqlc-generated Queries.
//
// CheckQuotaToday and CheckQuotaMonth return nil when the tenant is under
// the limit OR a sentinel error from errors.go when a specific dimension is
// exceeded. All other DB failures collapse to ErrQuotaCheckUnavailable so
// the middleware can fail-closed with a single 503 shape (D-A2).
//
// Counter instrumentation (obs.GatewayQuotaCheckFailures) is wired in
// Plan 04-06 at the middleware layer — this package only surfaces the
// sentinel so callers can differentiate the failure reason.
package quota

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// QuotaLimits is the per-tenant quota envelope read from TenantConfig.
// Zero values disable the corresponding dimension check.
type QuotaLimits struct {
	DailyTokens         int64
	MonthlyTokens       int64
	DailyAudioMinutes   int
	MonthlyAudioMinutes int
	DailyEmbeds         int
	MonthlyEmbeds       int
}

// countersQueries isolates the sqlc surface so tests can stub it without
// standing up Postgres. Mirrors the loaderQueries pattern in
// gateway/internal/upstreams/loader.go.
type countersQueries interface {
	GetUsageCountersToday(ctx context.Context, tenantID uuid.UUID) (gen.GetUsageCountersTodayRow, error)
	GetUsageCountersMonth(ctx context.Context, tenantID uuid.UUID) (gen.GetUsageCountersMonthRow, error)
}

// QuotaChecker provides hot-path quota lookups against ai_gateway.usage_counters.
// Returns either a sentinel from errors.go or nil. Fail-closed: if the
// underlying query returns any error (other than pgx.ErrNoRows), returns
// ErrQuotaCheckUnavailable (D-A2 — refuse to risk runaway external cost).
type QuotaChecker struct {
	q   countersQueries
	log *slog.Logger
}

// NewQuotaChecker constructs a checker backed by the supplied gen.Queries
// (typically gen.New(pool)). Accepts the interface so tests can inject a fake.
func NewQuotaChecker(q countersQueries, log *slog.Logger) *QuotaChecker {
	if log == nil {
		log = slog.Default()
	}
	return &QuotaChecker{q: q, log: log.With("module", "QUOTA")}
}

// CheckQuotaToday consults usage_counters for today's row and compares each
// dimension against the corresponding QuotaLimits field. Returns the FIRST
// exceeded dimension's sentinel so the middleware emits a precise error code.
//
// Order of evaluation: tokens → audio_minutes → embeds.
//
// A zero-limit dimension is skipped (0 means "disabled").
func (c *QuotaChecker) CheckQuotaToday(ctx context.Context, tenantID uuid.UUID, lim QuotaLimits) error {
	row, err := c.q.GetUsageCountersToday(ctx, tenantID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // no usage today yet — under all daily limits
		}
		c.log.Warn("quota check failed (today)",
			"tenant_id", tenantID,
			"err", err,
		)
		return ErrQuotaCheckUnavailable
	}
	if lim.DailyTokens > 0 && row.TokensIn+row.TokensOut >= lim.DailyTokens {
		return ErrQuotaExceededDailyTokens
	}
	if lim.DailyAudioMinutes > 0 && int(row.AudioSeconds/60) >= lim.DailyAudioMinutes {
		return ErrQuotaExceededDailyAudioMinutes
	}
	if lim.DailyEmbeds > 0 && int(row.EmbedsCount) >= lim.DailyEmbeds {
		return ErrQuotaExceededDailyEmbeds
	}
	return nil
}

// CheckQuotaMonth consults the SUM of usage_counters rows for the current
// calendar month (America/Sao_Paulo) and compares against monthly limits.
// Zero-limit dimensions are skipped.
func (c *QuotaChecker) CheckQuotaMonth(ctx context.Context, tenantID uuid.UUID, lim QuotaLimits) error {
	row, err := c.q.GetUsageCountersMonth(ctx, tenantID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		c.log.Warn("quota check failed (month)",
			"tenant_id", tenantID,
			"err", err,
		)
		return ErrQuotaCheckUnavailable
	}
	if lim.MonthlyTokens > 0 && row.TokensIn+row.TokensOut >= lim.MonthlyTokens {
		return ErrQuotaExceededMonthlyTokens
	}
	if lim.MonthlyAudioMinutes > 0 && int(row.AudioSeconds/60) >= lim.MonthlyAudioMinutes {
		return ErrQuotaExceededMonthlyAudioMinutes
	}
	if lim.MonthlyEmbeds > 0 && int(row.EmbedsCount) >= lim.MonthlyEmbeds {
		return ErrQuotaExceededMonthlyEmbeds
	}
	return nil
}

// ErrorCode maps a quota/rate-limit sentinel to the wire-format OpenAI code
// string (see pkg/openai/types.go Phase 4 constants; duplicated here as
// plain strings to avoid an import cycle when tests use this helper).
//
// Returns a best-effort "quota_unknown_<T>" fallback so unexpected errors
// never hit the client as an empty code (D-A4).
func ErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrRateLimitRPS):
		return "rate_limit_rps"
	case errors.Is(err, ErrRateLimitRPM):
		return "rate_limit_rpm"
	case errors.Is(err, ErrQuotaExceededDailyTokens):
		return "quota_exceeded_daily_tokens"
	case errors.Is(err, ErrQuotaExceededDailyAudioMinutes):
		return "quota_exceeded_daily_audio_minutes"
	case errors.Is(err, ErrQuotaExceededDailyEmbeds):
		return "quota_exceeded_daily_embeds"
	case errors.Is(err, ErrQuotaExceededMonthlyTokens):
		return "quota_exceeded_monthly_tokens"
	case errors.Is(err, ErrQuotaExceededMonthlyAudioMinutes):
		return "quota_exceeded_monthly_audio_minutes"
	case errors.Is(err, ErrQuotaExceededMonthlyEmbeds):
		return "quota_exceeded_monthly_embeds"
	case errors.Is(err, ErrQuotaCheckUnavailable):
		return "quota_check_unavailable"
	}
	return fmt.Sprintf("quota_unknown_%T", err)
}
