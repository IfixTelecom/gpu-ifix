package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

func runTenant(ctx context.Context, args []string, log *slog.Logger) int {
	if len(args) == 0 || args[0] != "create" {
		fmt.Fprintln(flag.CommandLine.Output(), "Usage: gatewayctl tenant create --name <name> --slug <slug>")
		return 2
	}
	fs := flag.NewFlagSet("tenant create", flag.ExitOnError)
	name := fs.String("name", "", "tenant display name (required)")
	slug := fs.String("slug", "", "tenant slug, url-safe, required")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if strings.TrimSpace(*name) == "" || strings.TrimSpace(*slug) == "" {
		fs.Usage()
		return 2
	}

	_, pool, err := loadAndPool(ctx, log)
	if err != nil {
		fmt.Fprintf(fs.Output(), "error: %v\n", err)
		return 1
	}
	defer pool.Close()

	q := gen.New(pool)
	t, err := q.CreateTenant(ctx, gen.CreateTenantParams{Slug: *slug, Name: *name})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
			fmt.Fprintf(fs.Output(), "error: tenant slug '%s' already exists\n", *slug)
			return 1
		}
		fmt.Fprintf(fs.Output(), "error: create tenant: %v\n", err)
		return 1
	}
	fmt.Printf("id=%s slug=%s name=%q\n", t.ID.String(), t.Slug, t.Name)
	log.Info("tenant created", "tenant_id", t.ID.String(), "slug", t.Slug)
	return 0
}
