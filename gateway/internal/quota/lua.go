// Package quota: Lua wrapper for the atomic RPS+RPM token bucket (D-A1).
//
// CheckBuckets executes the Stripe-canonical bucket script via redis.NewScript
// which transparently handles EVALSHA fast path + EVAL fallback on NOSCRIPT
// (so SCRIPT FLUSH or Redis restart do not require explicit re-load).
package quota

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/redis/go-redis/v9"
)

//go:embed scripts/token_bucket.lua
var tokenBucketSrc string

// tokenBucketScript caches the SHA1 inside the *Script struct; Run() picks
// EVALSHA first and falls back to EVAL if the script was evicted.
var tokenBucketScript = redis.NewScript(tokenBucketSrc)

// BucketResult is the decoded response of a single token-bucket evaluation.
// Allowed indicates whether the request may proceed. FailedWindow is "" when
// allowed, or "rps"/"rpm" to discriminate which window rejected the call
// (D-A4 — used by the middleware to emit the correct OpenAI error code).
type BucketResult struct {
	Allowed      bool
	RemRPS       int
	ResetRPSms   int
	RemRPM       int
	ResetRPMms   int
	FailedWindow string // "" | "rps" | "rpm"
}

// BucketKeyPrefix builds the canonical Redis key prefix for a (tenant,
// route_class) pair: "gw:rate:{tenant_id}:{route_class}".
//
// The Lua script appends ":rps:tokens", ":rps:ts", ":rpm:tokens", ":rpm:ts".
func BucketKeyPrefix(tenantID, routeClass string) string {
	return "gw:rate:" + tenantID + ":" + routeClass
}

// CheckBuckets runs the atomic RPS+RPM check against Redis.
//
// Args:
//
//	tenantID       — canonical tenant identifier (UUID string is typical).
//	routeClass     — one of "chat", "embed", "stt" (see bucket.go RouteClass*).
//	rpsCapacity    — max tokens in the RPS bucket (e.g. tenant.RPSLimit).
//	rpmCapacity    — max tokens in the RPM bucket (e.g. tenant.RPMLimit).
//	rpsRatePerMs   — refill rate for RPS (RPSLimit / 1000).
//	rpmRatePerMs   — refill rate for RPM (RPMLimit / 60000).
//	requested      — tokens to consume this call (usually 1).
//	nowMs          — caller-supplied wall clock in unix millis
//	                 (time.Now().UnixMilli()). The script does NOT call
//	                 redis TIME for portability with NOSCRIPT/EVAL fallback.
//
// The returned BucketResult exposes Allowed plus the bookkeeping needed to
// populate OpenAI X-RateLimit-* headers.
func CheckBuckets(
	ctx context.Context,
	rdb redis.UniversalClient,
	tenantID, routeClass string,
	rpsCapacity, rpmCapacity int,
	rpsRatePerMs, rpmRatePerMs float64,
	requested int,
	nowMs int64,
) (BucketResult, error) {
	prefix := BucketKeyPrefix(tenantID, routeClass)
	keys := []string{
		prefix + ":rps:tokens",
		prefix + ":rps:ts",
		prefix + ":rpm:tokens",
		prefix + ":rpm:ts",
	}
	raw, err := tokenBucketScript.Run(ctx, rdb, keys,
		nowMs, rpsCapacity, rpsRatePerMs, rpmCapacity, rpmRatePerMs, requested,
	).Slice()
	if err != nil {
		return BucketResult{}, fmt.Errorf("quota: lua exec: %w", err)
	}
	if len(raw) != 6 {
		return BucketResult{}, fmt.Errorf("quota: lua returned %d fields, want 6", len(raw))
	}
	allowed, ok := raw[0].(int64)
	if !ok {
		return BucketResult{}, fmt.Errorf("quota: lua field[0] type %T, want int64", raw[0])
	}
	remRPS, ok := raw[1].(int64)
	if !ok {
		return BucketResult{}, fmt.Errorf("quota: lua field[1] type %T, want int64", raw[1])
	}
	resetRPS, ok := raw[2].(int64)
	if !ok {
		return BucketResult{}, fmt.Errorf("quota: lua field[2] type %T, want int64", raw[2])
	}
	remRPM, ok := raw[3].(int64)
	if !ok {
		return BucketResult{}, fmt.Errorf("quota: lua field[3] type %T, want int64", raw[3])
	}
	resetRPM, ok := raw[4].(int64)
	if !ok {
		return BucketResult{}, fmt.Errorf("quota: lua field[4] type %T, want int64", raw[4])
	}
	failedWindow, ok := raw[5].(string)
	if !ok {
		return BucketResult{}, fmt.Errorf("quota: lua field[5] type %T, want string", raw[5])
	}
	return BucketResult{
		Allowed:      allowed == 1,
		RemRPS:       int(remRPS),
		ResetRPSms:   int(resetRPS),
		RemRPM:       int(remRPM),
		ResetRPMms:   int(resetRPM),
		FailedWindow: failedWindow,
	}, nil
}
