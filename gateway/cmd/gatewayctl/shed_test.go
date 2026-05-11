// Package main: unit tests for the `gatewayctl shed-state` and
// `gatewayctl shed-force` subcommands. These tests focus on flag parsing
// + validation paths that do NOT require a live Redis (the round-trip
// behavior is verified by the integration suite under
// build tag `integration` in shed_integration_test.go).
package main

import (
	"context"
	"log/slog"
	"testing"
)

// TestRunShedForce_InvalidAction asserts that an unknown verb
// (e.g. "bogus") produces a usage error WITHOUT touching Redis.
//
// The action check happens BEFORE flag parsing reaches Redis, so this
// path is exercised even when AI_GATEWAY_REDIS_ADDR is unset.
func TestRunShedForce_InvalidAction(t *testing.T) {
	ctx := context.Background()
	if code := runShedForce(ctx, []string{"bogus", "--upstream", "x"}, slog.Default()); code != 2 {
		t.Fatalf("expected exit 2 for invalid action, got %d", code)
	}
}

// TestRunShedForce_NoArgs asserts that calling shed-force with zero
// args produces a usage error.
func TestRunShedForce_NoArgs(t *testing.T) {
	ctx := context.Background()
	if code := runShedForce(ctx, []string{}, slog.Default()); code != 2 {
		t.Fatalf("expected exit 2 for no args, got %d", code)
	}
}

// TestRunShedForce_MissingUpstream asserts that on/off/clear without
// --upstream produces a usage error.
func TestRunShedForce_MissingUpstream(t *testing.T) {
	ctx := context.Background()
	for _, action := range []string{"on", "off", "clear"} {
		if code := runShedForce(ctx, []string{action}, slog.Default()); code != 2 {
			t.Fatalf("%s without --upstream: expected exit 2, got %d", action, code)
		}
	}
}

// TestRunShedForce_TTLParseError asserts that an unparseable duration
// rejects with usage exit even though the action is valid.
func TestRunShedForce_TTLParseError(t *testing.T) {
	ctx := context.Background()
	if code := runShedForce(ctx, []string{"on", "--upstream", "x", "--ttl", "garbage"}, slog.Default()); code != 2 {
		t.Fatalf("expected exit 2 for unparseable TTL, got %d", code)
	}
}

// TestRunShedState_UnknownFormat asserts that --format=xml (or anything
// other than table|json) returns a usage error.
//
// This test does NOT touch Redis: the format flag is validated against
// a static allow-list BEFORE any client is built.
func TestRunShedState_UnknownFormat(t *testing.T) {
	ctx := context.Background()
	// We intentionally pass an unparseable AI_GATEWAY_REDIS_ADDR so even
	// if the format check did dial Redis, the dial would fail; the test
	// asserts the returned code is the format-validation code (2), not
	// the Redis-failure code (1).
	t.Setenv("AI_GATEWAY_REDIS_ADDR", "127.0.0.1:1")
	t.Setenv("AI_GATEWAY_PG_DSN", "postgres://u:p@127.0.0.1:1/x?sslmode=disable")
	t.Setenv("UPSTREAM_LLM_URL", "http://x")
	t.Setenv("UPSTREAM_STT_URL", "http://x")
	t.Setenv("UPSTREAM_EMBED_URL", "http://x")
	t.Setenv("UPSTREAM_HEALTH_BRIDGE_URL", "http://x")
	if code := runShedState(ctx, []string{"--format", "xml"}, slog.Default()); code != 2 {
		t.Fatalf("expected exit 2 for unknown format, got %d", code)
	}
}
