package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db"
)

func runMigrate(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage: gatewayctl migrate <subcommand> [flags]

Subcommands:
  up              Apply all pending migrations
  down <n>        Roll back N migrations (default 1; use 0 to roll back all)
  status          Print applied / pending migration list
`)
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	sub := fs.Arg(0)
	_, pool, err := loadAndPool(ctx, log)
	if err != nil {
		fmt.Fprintf(fs.Output(), "error: %v\n", err)
		return 1
	}
	defer pool.Close()

	switch sub {
	case "up", "":
		if err := db.Up(ctx, pool); err != nil {
			fmt.Fprintf(fs.Output(), "migrate up failed: %v\n", err)
			return 1
		}
		log.Info("migrate up complete")
	case "down":
		n := 1
		if fs.NArg() >= 2 {
			if _, scanErr := fmt.Sscanf(fs.Arg(1), "%d", &n); scanErr != nil {
				fmt.Fprintf(fs.Output(), "migrate down: argument must be integer\n")
				return 2
			}
		}
		if err := db.Down(ctx, pool, n); err != nil {
			fmt.Fprintf(fs.Output(), "migrate down failed: %v\n", err)
			return 1
		}
		log.Info("migrate down complete", "n", n)
	case "status":
		if err := db.Status(ctx, pool); err != nil {
			fmt.Fprintf(fs.Output(), "migrate status failed: %v\n", err)
			return 1
		}
	default:
		fs.Usage()
		return 2
	}
	return 0
}
