// Package quota (enforcer.go): chi HTTP middleware that enforces
// rate-limit (RPS/RPM Lua bucket) and daily/monthly quota (Postgres
// lookup). Chain order per CONTEXT.md D-D1:
//
//	auth → idempotency → RateLimitMiddleware → QuotaMiddleware → schedule → tokencount → dispatcher
//
// Fail semantics (D-A2):
//   - rate-limit check: fail-open when cfg.RateLimitFailOpen=true (preserves
//     "failover invisible" on Redis incidents). Increments
//     obs.GatewayRateLimitCheckFailures on every Lua exec error.
//   - quota check: fail-closed (refuses to risk runaway external cost on
//     unknown usage state). Returns 503 quota_check_unavailable.
//
// Replay semantics (D-D1):
//   - idempotency replay SKIPS the RPS/RPM bucket consumption (original
//     request already paid that cost).
//   - idempotency replay STILL CONSUMES the daily/monthly quota (Stripe
//     canonical: every served request is counted).
package quota

import (
	"net/http"
	"strconv"
	"time"

	"log/slog"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/idempotency"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/tenants"
)

// RateLimitMiddleware enforces RPS+RPM atomic via Redis Lua. Runs BEFORE
// QuotaMiddleware per D-D1. Fail-open on Redis transport errors when
// failOpen=true (increments GatewayRateLimitCheckFailures). Skips entirely
// on idempotency replays (D-D1 nuance; replay already paid the bucket on
// the winning attempt).
//
// On rejection emits HTTP 429 with the OpenAI envelope
// (type=rate_limit_error, code=rate_limit_rps|rate_limit_rpm) and the
// Retry-After header sized to the Lua script's reset_ms.
//
// Always sets X-RateLimit-Limit-Requests + X-RateLimit-Remaining-Requests
// headers on allowed responses so OpenAI-compat clients get their budget
// accounting automatically (D-D1 "Claude's Discretion / X-RateLimit-*
// shape").
func RateLimitMiddleware(
	rdb redis.UniversalClient,
	loader *tenants.Loader,
	failOpen bool,
	log *slog.Logger,
) func(http.Handler) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	log = log.With("module", "QUOTA")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// D-D1: replays do not re-consume the RPS/RPM bucket.
			if idempotency.IsReplay(r.Context()) {
				next.ServeHTTP(w, r)
				return
			}

			ac, ok := auth.FromContext(r.Context())
			if !ok {
				httpx.WriteOpenAIError(w, http.StatusUnauthorized,
					"authentication_error", "no_api_key",
					"Authenticated tenant required.")
				return
			}

			tenantID, perr := uuid.Parse(ac.TenantID)
			if perr != nil {
				// auth middleware guarantees a well-formed UUID; defensive.
				log.Warn("rate-limit: tenant_id is not a UUID",
					"tenant_id", ac.TenantID, "err", perr)
				next.ServeHTTP(w, r)
				return
			}

			cfg, err := loader.Get(tenantID)
			if err != nil {
				// Tenant snapshot missing (freshly added, pending refresh).
				// Pass through — auth already confirmed the key is active.
				next.ServeHTTP(w, r)
				return
			}

			routeClass := classifyRoute(r.URL.Path)
			bucketCfg := BucketConfig{RPSCapacity: cfg.RPSLimit, RPMCapacity: cfg.RPMLimit}

			// A bucket with 0 capacity disables the corresponding window
			// (same semantics as 0-tokens quota → disabled). If BOTH are 0
			// we bypass the Lua call entirely to avoid a pointless round-trip.
			if bucketCfg.RPSCapacity <= 0 && bucketCfg.RPMCapacity <= 0 {
				next.ServeHTTP(w, r)
				return
			}

			res, luaErr := CheckBuckets(r.Context(), rdb,
				ac.TenantID, string(routeClass),
				bucketCfg.RPSCapacity, bucketCfg.RPMCapacity,
				bucketCfg.RPSRefillPerMs(), bucketCfg.RPMRefillPerMs(),
				1, time.Now().UnixMilli(),
			)
			if luaErr != nil {
				obs.GatewayRateLimitCheckFailures.WithLabelValues("transport").Inc()
				if failOpen {
					log.Warn("rate-limit Lua error; failing open",
						"tenant", cfg.Slug, "err", luaErr)
					next.ServeHTTP(w, r)
					return
				}
				httpx.WriteOpenAIError(w, http.StatusServiceUnavailable,
					"service_unavailable", "rate_limit_check_unavailable",
					"Rate-limit check unavailable.")
				return
			}

			// Always set OpenAI-compat rate-limit headers (both success and
			// rejection). RPS is the tighter window so it drives the
			// "Remaining" figure most clients display.
			if bucketCfg.RPSCapacity > 0 {
				w.Header().Set("X-RateLimit-Limit-Requests", strconv.Itoa(bucketCfg.RPSCapacity))
				w.Header().Set("X-RateLimit-Remaining-Requests", strconv.Itoa(res.RemRPS))
			}

			if !res.Allowed {
				window := res.FailedWindow
				if window == "" {
					window = "rps"
				}
				obs.GatewayRateLimitRejected.WithLabelValues(cfg.Slug, window).Inc()

				var code, msg string
				var retryMs int
				if window == "rps" {
					code = "rate_limit_rps"
					msg = "Rate limit exceeded: requests per second."
					retryMs = res.ResetRPSms
				} else {
					code = "rate_limit_rpm"
					msg = "Rate limit exceeded: requests per minute."
					retryMs = res.ResetRPMms
				}
				// Retry-After is whole seconds per RFC 7231 §7.1.3; round up so
				// a 200 ms reset still advises the client to wait at least 1 s.
				retrySec := retryMs / 1000
				if retryMs%1000 != 0 || retrySec < 1 {
					retrySec++
				}
				w.Header().Set("Retry-After", strconv.Itoa(retrySec))
				httpx.WriteOpenAIError(w, http.StatusTooManyRequests,
					"rate_limit_error", code, msg)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// QuotaMiddleware enforces daily + monthly quotas per tenant. Fail-closed
// per D-A2: any DB failure returns 503 quota_check_unavailable (refusing
// to risk runaway external cost). Checks even on idempotency replays per
// D-D1 (Stripe canonical: every served request consumes quota).
//
// On rejection emits HTTP 429 with type=insufficient_quota + code matching
// the sentinel (quota_exceeded_daily_tokens, etc.).
func QuotaMiddleware(
	checker *QuotaChecker,
	loader *tenants.Loader,
	log *slog.Logger,
) func(http.Handler) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	log = log.With("module", "QUOTA")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ac, ok := auth.FromContext(r.Context())
			if !ok {
				// RateLimitMiddleware would have handled this already; guard
				// for test wiring that mounts QuotaMiddleware alone.
				next.ServeHTTP(w, r)
				return
			}
			tenantID, perr := uuid.Parse(ac.TenantID)
			if perr != nil {
				next.ServeHTTP(w, r)
				return
			}
			cfg, err := loader.Get(tenantID)
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}
			lim := QuotaLimits{
				DailyTokens:         cfg.DailyQuotaTokens,
				MonthlyTokens:       cfg.MonthlyQuotaTokens,
				DailyAudioMinutes:   cfg.DailyQuotaAudioMinutes,
				MonthlyAudioMinutes: cfg.MonthlyQuotaAudioMinutes,
				DailyEmbeds:         cfg.DailyQuotaEmbeds,
				MonthlyEmbeds:       cfg.MonthlyQuotaEmbeds,
			}
			if qerr := checker.CheckQuotaToday(r.Context(), tenantID, lim); qerr != nil {
				handleQuotaError(w, cfg.Slug, qerr, "daily")
				return
			}
			if qerr := checker.CheckQuotaMonth(r.Context(), tenantID, lim); qerr != nil {
				handleQuotaError(w, cfg.Slug, qerr, "monthly")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// handleQuotaError emits the OpenAI envelope + metric increment for a
// quota-check failure. Check-unavailable collapses to 503; every other
// sentinel maps to 429 insufficient_quota with its code.
func handleQuotaError(w http.ResponseWriter, tenantSlug string, err error, period string) {
	// ErrQuotaCheckUnavailable: fail-closed 503.
	if err == ErrQuotaCheckUnavailable {
		obs.GatewayQuotaCheckFailures.WithLabelValues(period).Inc()
		httpx.WriteOpenAIError(w, http.StatusServiceUnavailable,
			"service_unavailable", "quota_check_unavailable",
			"Quota check unavailable; refusing to risk runaway external cost.")
		return
	}
	// Map sentinel → wire code + dimension label.
	code := ErrorCode(err)
	dimension := dimensionOf(err)
	obs.GatewayQuotaRejected.WithLabelValues(tenantSlug, dimension, period).Inc()
	httpx.WriteOpenAIError(w, http.StatusTooManyRequests,
		"insufficient_quota", code, "Quota exceeded.")
}

// dimensionOf maps a quota sentinel to its dimension label for metrics.
func dimensionOf(err error) string {
	switch err {
	case ErrQuotaExceededDailyTokens, ErrQuotaExceededMonthlyTokens:
		return "tokens"
	case ErrQuotaExceededDailyAudioMinutes, ErrQuotaExceededMonthlyAudioMinutes:
		return "audio_minutes"
	case ErrQuotaExceededDailyEmbeds, ErrQuotaExceededMonthlyEmbeds:
		return "embeds"
	}
	return "unknown"
}

// classifyRoute maps a chi-routed URL path to a RouteClass so rate-limit
// buckets are namespaced by endpoint class. Unknown paths fall back to
// RouteClassChat (safest default; a misrouted request still gets rate
// limited rather than escaping the bucket entirely — mitigates T-04-21).
func classifyRoute(path string) RouteClass {
	switch path {
	case "/v1/chat/completions":
		return RouteClassChat
	case "/v1/embeddings":
		return RouteClassEmbed
	case "/v1/audio/transcriptions":
		return RouteClassSTT
	}
	return RouteClassChat
}
