// Package main (billing.go): placeholder dispatchers for `gatewayctl billing`
// and `gatewayctl usage` so main.go compiles after Task 1's dispatch update.
// Real reconcile + usage report semantics land in Task 2 (this same plan).
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
)

// runBilling is wired into the main dispatcher by Task 1; Task 2 replaces
// this body with the full reconcile implementation.
func runBilling(ctx context.Context, args []string, log *slog.Logger) int {
	_ = ctx
	_ = args
	_ = log
	fmt.Fprintln(os.Stderr, "gatewayctl billing: not yet implemented (Plan 04-07 Task 2)")
	return 1
}

// runUsage is wired into the main dispatcher by Task 1; Task 2 replaces this
// body with the full per-tenant breakdown.
func runUsage(ctx context.Context, args []string, log *slog.Logger) int {
	_ = ctx
	_ = args
	_ = log
	fmt.Fprintln(os.Stderr, "gatewayctl usage: not yet implemented (Plan 04-07 Task 2)")
	return 1
}
