// Package config loads runtime configuration for the gateway and the
// gatewayctl CLI from environment variables. Load is called once at
// startup; the returned Config is immutable for the lifetime of the
// process (CONTEXT.md D-D3 Plumbing + cobrancas-api src/config.ts pattern).
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the typed view of required + optional env vars.
type Config struct {
	// HTTP
	Port              int           // GATEWAY_PORT (default 8080)
	ReadHeaderTimeout time.Duration // fixed 10s per CONTEXT.md Plumbing
	ReadTimeout       time.Duration // fixed 60s (whisper multipart)
	WriteTimeout      time.Duration // fixed 0 (streaming unbounded — SSE needs no write deadline)
	// Operational note (Codex review [LOW] 02-01):
	//   WriteTimeout=0 is required for SSE chat streams but removes a slow-
	//   client-DoS defense on non-streaming routes. Phase 3's routing layer
	//   should revisit: per-route WriteTimeout (chat=0 for SSE, embeddings=30s,
	//   audio=120s). Tracked as Phase 3 follow-up todo in STATE.md.
	IdleTimeout    time.Duration // fixed 120s
	MaxHeaderBytes int           // fixed 1 MiB
	MaxBodyBytes   int64         // fixed 25 MiB (OpenAI audio limit)

	// Data layer
	PGDSN          string // AI_GATEWAY_PG_DSN (required)
	PGMaxConns     int32  // AI_GATEWAY_PG_MAX_CONNS (default 10)
	RedisAddr      string // AI_GATEWAY_REDIS_ADDR (required, host:port)
	RedisPassword  string // AI_GATEWAY_REDIS_PASSWORD (optional)
	RedisKeyPrefix string // fixed "gw:" (CONTEXT.md Integration Points)

	// Upstreams
	UpstreamLLMURL          string // UPSTREAM_LLM_URL (required)
	UpstreamSTTURL          string // UPSTREAM_STT_URL (required)
	UpstreamEmbedURL        string // UPSTREAM_EMBED_URL (required)
	UpstreamHealthBridgeURL string // UPSTREAM_HEALTH_BRIDGE_URL (required)

	// Observability
	SentryDSN string // SENTRY_DSN (optional; empty = Sentry disabled)
	LogLevel  string // LOG_LEVEL (default info)
	Env       string // ENV (default production)

	// Admin / bootstrap
	BootstrapTenantSlug string // BOOTSTRAP_TENANT_SLUG (default converseai)
}

// ErrMissingEnv is returned by Load when one or more required env vars are unset.
var ErrMissingEnv = errors.New("config: required environment variable not set")

// Load reads env vars into a Config. Returns an error listing any missing
// required variables. Callers should os.Exit(2) after logging the error.
func Load() (Config, error) {
	cfg := Config{
		Port:              atoiOr(os.Getenv("GATEWAY_PORT"), 8080),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,        // 1 MiB
		MaxBodyBytes:      25 * (1 << 20), // 25 MiB

		PGDSN:          os.Getenv("AI_GATEWAY_PG_DSN"),
		PGMaxConns:     int32(atoiOr(os.Getenv("AI_GATEWAY_PG_MAX_CONNS"), 10)),
		RedisAddr:      os.Getenv("AI_GATEWAY_REDIS_ADDR"),
		RedisPassword:  os.Getenv("AI_GATEWAY_REDIS_PASSWORD"),
		RedisKeyPrefix: "gw:",

		UpstreamLLMURL:          os.Getenv("UPSTREAM_LLM_URL"),
		UpstreamSTTURL:          os.Getenv("UPSTREAM_STT_URL"),
		UpstreamEmbedURL:        os.Getenv("UPSTREAM_EMBED_URL"),
		UpstreamHealthBridgeURL: os.Getenv("UPSTREAM_HEALTH_BRIDGE_URL"),

		SentryDSN: os.Getenv("SENTRY_DSN"),
		LogLevel:  envOr("LOG_LEVEL", "info"),
		Env:       envOr("ENV", "production"),

		BootstrapTenantSlug: envOr("BOOTSTRAP_TENANT_SLUG", "converseai"),
	}

	// Iterate in a fixed order so error messages are deterministic — tests
	// assert specific var names in a stable string, and ops appreciate
	// predictable output.
	requiredOrder := []string{
		"AI_GATEWAY_PG_DSN",
		"AI_GATEWAY_REDIS_ADDR",
		"UPSTREAM_LLM_URL",
		"UPSTREAM_STT_URL",
		"UPSTREAM_EMBED_URL",
		"UPSTREAM_HEALTH_BRIDGE_URL",
	}
	required := map[string]string{
		"AI_GATEWAY_PG_DSN":          cfg.PGDSN,
		"AI_GATEWAY_REDIS_ADDR":      cfg.RedisAddr,
		"UPSTREAM_LLM_URL":           cfg.UpstreamLLMURL,
		"UPSTREAM_STT_URL":           cfg.UpstreamSTTURL,
		"UPSTREAM_EMBED_URL":         cfg.UpstreamEmbedURL,
		"UPSTREAM_HEALTH_BRIDGE_URL": cfg.UpstreamHealthBridgeURL,
	}
	var missing []string
	for _, name := range requiredOrder {
		if strings.TrimSpace(required[name]) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("%w: %s", ErrMissingEnv, strings.Join(missing, ", "))
	}
	return cfg, nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func atoiOr(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
