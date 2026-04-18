// Package httpx_test (redact_test.go): redactor wrapping behaviour.
package httpx_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
)

func newJSONRedactor(buf *bytes.Buffer) *slog.Logger {
	h := httpx.NewRedactor(slog.NewJSONHandler(buf, nil))
	return slog.New(h)
}

func parseRecord(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var m map[string]any
	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatalf("no log line emitted")
	}
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("parse JSON record: %v (buf=%q)", err, line)
	}
	return m
}

func TestRedactor_RedactsAuthorization(t *testing.T) {
	var buf bytes.Buffer
	log := newJSONRedactor(&buf)
	log.Info("ping", slog.String("Authorization", "Bearer secret"))
	rec := parseRecord(t, &buf)
	if rec["Authorization"] != "***REDACTED***" {
		t.Fatalf("Authorization not redacted: %#v", rec)
	}
	if strings.Contains(buf.String(), "secret") {
		t.Fatalf("raw secret leaked to log buffer: %q", buf.String())
	}
}

func TestRedactor_RedactsCaseInsensitive(t *testing.T) {
	cases := []string{
		"authorization",
		"AUTHORIZATION",
		"X-API-Key",
		"x-api-key",
		"Cookie",
		"Proxy-Authorization",
		"api_key",
		"ApiKey",
	}
	for _, k := range cases {
		t.Run(k, func(t *testing.T) {
			var buf bytes.Buffer
			log := newJSONRedactor(&buf)
			log.Info("ping", slog.String(k, "super-secret"))
			if strings.Contains(buf.String(), "super-secret") {
				t.Fatalf("secret leaked for key %q: %q", k, buf.String())
			}
		})
	}
}

func TestRedactor_PassesThroughSafeAttrs(t *testing.T) {
	var buf bytes.Buffer
	log := newJSONRedactor(&buf)
	log.Info("ping", slog.String("tenant_id", "t-1"), slog.String("request_id", "rid"))
	rec := parseRecord(t, &buf)
	if rec["tenant_id"] != "t-1" {
		t.Fatalf("tenant_id altered: %#v", rec)
	}
	if rec["request_id"] != "rid" {
		t.Fatalf("request_id altered: %#v", rec)
	}
}

func TestRedactor_WithAttrs(t *testing.T) {
	var buf bytes.Buffer
	log := newJSONRedactor(&buf).With(slog.String("Authorization", "Bearer secret"))
	log.Info("ping")
	if strings.Contains(buf.String(), "secret") {
		t.Fatalf("secret from With() attrs leaked: %q", buf.String())
	}
	rec := parseRecord(t, &buf)
	if rec["Authorization"] != "***REDACTED***" {
		t.Fatalf("Authorization from With() not redacted: %#v", rec)
	}
}

func TestRedactor_EnabledDelegates(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})
	h := httpx.NewRedactor(inner)
	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatalf("expected Info disabled when inner is LevelWarn")
	}
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Fatalf("expected Error enabled when inner is LevelWarn")
	}
}

func TestIsSensitiveKey(t *testing.T) {
	positive := []string{"Authorization", "X-API-Key", "Cookie", "proxy-authorization", "api_key", "ApiKey"}
	for _, k := range positive {
		if !httpx.IsSensitiveKey(k) {
			t.Errorf("IsSensitiveKey(%q) expected true", k)
		}
	}
	negative := []string{"tenant_id", "request_id", "X-Request-ID", "content-type"}
	for _, k := range negative {
		if httpx.IsSensitiveKey(k) {
			t.Errorf("IsSensitiveKey(%q) expected false", k)
		}
	}
}

func TestRedactor_WithGroupDelegates(t *testing.T) {
	var buf bytes.Buffer
	h := httpx.NewRedactor(slog.NewJSONHandler(&buf, nil))
	g := h.WithGroup("req")
	log := slog.New(g)
	log.Info("ping", slog.String("Authorization", "Bearer secret"))
	if strings.Contains(buf.String(), "secret") {
		t.Fatalf("secret leaked via group: %q", buf.String())
	}
}
