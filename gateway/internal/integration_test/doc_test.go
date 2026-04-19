//go:build integration

// Package integration_test exercises the Phase 2 gateway as a system.
// Unit tests under gateway/internal/<package>/*_test.go cover individual
// packages with mocks/fakes; this package spins real Postgres 16 + Redis
// 7 containers via testcontainers-go and asserts the full pipeline.
//
// Run with:
//
//	go test -tags=integration ./gateway/internal/integration_test/... -count=1 -v
//
// The `integration` build tag ensures these tests are OPT-IN. The
// default `go test ./...` in CI (build-gateway.yml, Plan 02-08) skips
// them, so fast unit feedback stays fast. A dedicated integration-test
// job in the same workflow runs this package with docker-in-docker.
package integration
