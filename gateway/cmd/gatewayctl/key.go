package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

func runKey(ctx context.Context, args []string, log *slog.Logger) int {
	if len(args) == 0 {
		fmt.Fprintln(flag.CommandLine.Output(), "Usage: gatewayctl key create|revoke [flags]")
		return 2
	}
	switch args[0] {
	case "create":
		return runKeyCreate(ctx, args[1:], log)
	case "revoke":
		return runKeyRevoke(ctx, args[1:], log)
	default:
		fmt.Fprintf(flag.CommandLine.Output(), "unknown subcommand: %s\n", args[0])
		return 2
	}
}

func runKeyCreate(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("key create", flag.ExitOnError)
	tenantSlug := fs.String("tenant", "", "tenant slug (required)")
	dataClass := fs.String("data-class", "normal", "normal | sensitive")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *tenantSlug == "" {
		fs.Usage()
		return 2
	}
	if *dataClass != "normal" && *dataClass != "sensitive" {
		fmt.Fprintf(fs.Output(), "--data-class must be 'normal' or 'sensitive'\n")
		return 2
	}

	_, pool, err := loadAndPool(ctx, log)
	if err != nil {
		fmt.Fprintf(fs.Output(), "error: %v\n", err)
		return 1
	}
	defer pool.Close()
	q := gen.New(pool)

	tenant, err := q.GetTenantBySlug(ctx, *tenantSlug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			fmt.Fprintf(fs.Output(), "error: tenant '%s' not found\n", *tenantSlug)
			return 1
		}
		fmt.Fprintf(fs.Output(), "error: lookup tenant: %v\n", err)
		return 1
	}

	raw, hash, lookupHash, prefix, err := auth.GenerateAPIKey()
	if err != nil {
		fmt.Fprintf(fs.Output(), "error: generate key: %v\n", err)
		return 1
	}

	inserted, err := q.InsertAPIKey(ctx, gen.InsertAPIKeyParams{
		TenantID:      tenant.ID,
		KeyHash:       hash,
		KeyLookupHash: lookupHash, // SHA-256 for fast hot-path lookup; Codex review [HIGH] 02-03
		KeyPrefix:     prefix,
		DataClass:     *dataClass, // pgx encodes string → ENUM
	})
	if err != nil {
		fmt.Fprintf(fs.Output(), "error: insert key: %v\n", err)
		return 1
	}

	// IMPORTANT: print the raw key to stdout ONCE. Operator must copy it now.
	// Do NOT log.Info(raw, ...) — the slog redactor only covers known key names.
	fmt.Printf("key=%s\nid=%s\nprefix=%s\ntenant=%s\ndata_class=%s\n",
		raw, inserted.ID.String(), prefix, *tenantSlug, *dataClass)
	log.Info("api key issued",
		"api_key_id", inserted.ID.String(),
		"tenant_id", tenant.ID.String(),
		"tenant_slug", *tenantSlug,
		"data_class", *dataClass,
		"key_prefix", prefix,
	) // NO raw key in log record
	return 0
}

func runKeyRevoke(ctx context.Context, args []string, log *slog.Logger) int {
	fs := flag.NewFlagSet("key revoke", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(fs.Output(), "Usage: gatewayctl key revoke <api_key_id>")
		return 2
	}
	id, err := uuid.Parse(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(fs.Output(), "error: invalid UUID: %v\n", err)
		return 2
	}

	_, pool, err := loadAndPool(ctx, log)
	if err != nil {
		fmt.Fprintf(fs.Output(), "error: %v\n", err)
		return 1
	}
	defer pool.Close()
	q := gen.New(pool)

	existing, err := q.GetAPIKeyByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			fmt.Fprintf(fs.Output(), "error: api_key '%s' not found\n", id.String())
			return 1
		}
		fmt.Fprintf(fs.Output(), "error: lookup key: %v\n", err)
		return 1
	}
	// Status comes back from sqlc as interface{} (Postgres ENUM). Coerce.
	statusStr := ""
	switch s := existing.Status.(type) {
	case string:
		statusStr = s
	case []byte:
		statusStr = string(s)
	}
	if statusStr == "revoked" {
		fmt.Printf("already revoked: id=%s\n", id.String())
		return 0
	}
	if err := q.RevokeAPIKey(ctx, id); err != nil {
		fmt.Fprintf(fs.Output(), "error: revoke key: %v\n", err)
		return 1
	}
	fmt.Printf("revoked: id=%s prefix=%s\n", id.String(), existing.KeyPrefix)
	log.Info("api key revoked", "api_key_id", id.String(), "key_prefix", existing.KeyPrefix)
	return 0
}
