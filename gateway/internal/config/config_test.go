// Package config_test exercises Load() against env var fixtures.
package config_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
)

// allRequired are the six env vars Load() insists on.
var allRequired = []string{
	"AI_GATEWAY_PG_DSN",
	"AI_GATEWAY_REDIS_ADDR",
	"UPSTREAM_LLM_URL",
	"UPSTREAM_STT_URL",
	"UPSTREAM_EMBED_URL",
	"UPSTREAM_HEALTH_BRIDGE_URL",
}

// optionalVars are other vars we may want to clear so one test does not
// bleed state into the next via `os.Environ`.
var optionalVars = []string{
	"GATEWAY_PORT",
	"AI_GATEWAY_PG_MAX_CONNS",
	"AI_GATEWAY_REDIS_PASSWORD",
	"SENTRY_DSN",
	"LOG_LEVEL",
	"ENV",
	"BOOTSTRAP_TENANT_SLUG",
}

func clearAll(t *testing.T) {
	t.Helper()
	for _, v := range allRequired {
		t.Setenv(v, "")
	}
	for _, v := range optionalVars {
		t.Setenv(v, "")
	}
}

func setAllRequired(t *testing.T) {
	t.Helper()
	for _, v := range allRequired {
		t.Setenv(v, "fake-"+v)
	}
}

func TestLoad_MissingRequired(t *testing.T) {
	clearAll(t)
	_, err := config.Load()
	if err == nil {
		t.Fatalf("expected error when all required vars unset, got nil")
	}
	if !errors.Is(err, config.ErrMissingEnv) {
		t.Fatalf("expected errors.Is(err, ErrMissingEnv) true, got err=%v", err)
	}
	for _, name := range allRequired {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("expected error to mention %q, got %q", name, err.Error())
		}
	}
}

func TestLoad_MissingSingleVarNamedInError(t *testing.T) {
	clearAll(t)
	setAllRequired(t)
	t.Setenv("UPSTREAM_STT_URL", "")
	_, err := config.Load()
	if err == nil {
		t.Fatalf("expected error when UPSTREAM_STT_URL unset")
	}
	if !strings.Contains(err.Error(), "UPSTREAM_STT_URL") {
		t.Fatalf("expected error to mention UPSTREAM_STT_URL, got %q", err.Error())
	}
}

func TestLoad_AllRequiredPresent(t *testing.T) {
	clearAll(t)
	setAllRequired(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.PGDSN == "" || cfg.RedisAddr == "" || cfg.UpstreamLLMURL == "" ||
		cfg.UpstreamSTTURL == "" || cfg.UpstreamEmbedURL == "" ||
		cfg.UpstreamHealthBridgeURL == "" {
		t.Fatalf("expected populated required fields, got %+v", cfg)
	}
}

func TestLoad_DefaultPort(t *testing.T) {
	clearAll(t)
	setAllRequired(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != 8080 {
		t.Fatalf("expected default Port=8080, got %d", cfg.Port)
	}
}

func TestLoad_CustomPort(t *testing.T) {
	clearAll(t)
	setAllRequired(t)
	t.Setenv("GATEWAY_PORT", "9999")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != 9999 {
		t.Fatalf("expected Port=9999, got %d", cfg.Port)
	}
}

func TestLoad_PGMaxConnsDefault(t *testing.T) {
	clearAll(t)
	setAllRequired(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.PGMaxConns != 10 {
		t.Fatalf("expected PGMaxConns=10, got %d", cfg.PGMaxConns)
	}
}

func TestLoad_FixedTimeouts(t *testing.T) {
	clearAll(t)
	setAllRequired(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("ReadHeaderTimeout want 10s got %v", cfg.ReadHeaderTimeout)
	}
	if cfg.ReadTimeout != 60*time.Second {
		t.Errorf("ReadTimeout want 60s got %v", cfg.ReadTimeout)
	}
	if cfg.WriteTimeout != 0 {
		t.Errorf("WriteTimeout want 0 got %v", cfg.WriteTimeout)
	}
	if cfg.IdleTimeout != 120*time.Second {
		t.Errorf("IdleTimeout want 120s got %v", cfg.IdleTimeout)
	}
	if cfg.MaxBodyBytes != 25*(1<<20) {
		t.Errorf("MaxBodyBytes want 25 MiB got %d", cfg.MaxBodyBytes)
	}
	if cfg.MaxHeaderBytes != 1<<20 {
		t.Errorf("MaxHeaderBytes want 1 MiB got %d", cfg.MaxHeaderBytes)
	}
	if cfg.RedisKeyPrefix != "gw:" {
		t.Errorf("RedisKeyPrefix want 'gw:' got %q", cfg.RedisKeyPrefix)
	}
}

func TestLoad_SentryOptional(t *testing.T) {
	clearAll(t)
	setAllRequired(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.SentryDSN != "" {
		t.Fatalf("SentryDSN should default to empty, got %q", cfg.SentryDSN)
	}
}

func TestLoad_LogLevelDefaultInfo(t *testing.T) {
	clearAll(t)
	setAllRequired(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel default want 'info' got %q", cfg.LogLevel)
	}
	if cfg.Env != "production" {
		t.Errorf("Env default want 'production' got %q", cfg.Env)
	}
	if cfg.BootstrapTenantSlug != "converseai" {
		t.Errorf("BootstrapTenantSlug default want 'converseai' got %q", cfg.BootstrapTenantSlug)
	}
}
