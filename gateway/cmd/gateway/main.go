// Binary gateway serves OpenAI-compatible endpoints fronted by
// multi-tenant authentication, per-request audit logging, idempotency
// keys, and a reverse proxy to the Phase 1 pod. Plan 02-03 wires the
// pgxpool + Redis client + auth middleware. Audit, idempotency, and
// proxy land in 02-04..02-06.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

func main() {
	selfCheck := flag.Bool("self-check", false, "exit 0 immediately (docker healthcheck)")
	flag.Parse()
	if *selfCheck {
		fmt.Println("ok")
		os.Exit(0)
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gateway: config error: %v\n", err)
		os.Exit(2)
	}
	if err := obs.Init(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "gateway: sentry init: %v\n", err)
	}
	defer obs.Flush(2 * time.Second)

	log := newLogger(cfg)
	log.Info("starting gateway",
		"port", cfg.Port,
		"env", cfg.Env,
		"upstream_llm", cfg.UpstreamLLMURL,
		"upstream_stt", cfg.UpstreamSTTURL,
		"upstream_embed", cfg.UpstreamEmbedURL,
		"upstream_health_bridge", cfg.UpstreamHealthBridgeURL,
		"version", obs.BuildVersion,
	)

	// Root context cancelled on SIGTERM/SIGINT.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		log.Info("signal received, shutting down", "signal", sig.String())
		cancel()
	}()

	// Postgres pool — fail-fast. Required for auth + audit.
	pool, err := db.NewPool(ctx, cfg)
	if err != nil {
		log.Error("db pool init failed", "err", err)
		os.Exit(2)
	}
	defer pool.Close()

	// Optional boot-time migration (CONTEXT.md D-D1 — applied at boot OR via
	// gatewayctl). Default off; ops decide via env.
	if os.Getenv("AI_GATEWAY_MIGRATE_ON_BOOT") == "true" {
		if err := db.Up(ctx, pool); err != nil {
			log.Error("migrate up at boot failed", "err", err)
			os.Exit(2)
		}
		log.Info("migrations applied on boot")
	}

	// Partition automation (Plan 02-02 Task 3 + Codex review [LOW] 02-02).
	// Runs after migrations so the parent partitioned tables exist.
	if err := db.EnsurePartitions(ctx, pool, time.Now(), db.DefaultPartitionLookahead); err != nil {
		log.Error("ensure partitions failed", "err", err)
		os.Exit(2)
	}

	// Redis — fail-fast. Required for auth cache + idempotency (Plan 02-06).
	rdb, err := redisx.NewClient(ctx, cfg)
	if err != nil {
		log.Error("redis init failed", "err", err)
		os.Exit(2)
	}
	defer rdb.Close()

	// TouchBuffer: debounced last_used_at updates (Codex review [MEDIUM] 02-03).
	// flushFn uses a SEPARATE short-lived context so shutdown drains via
	// Run ctx cancel propagation but each UPDATE has its own 3s deadline.
	touchFlush := func(fctx context.Context, id uuid.UUID) error {
		return gen.New(pool).TouchKeyLastUsed(fctx, id)
	}
	touchBuf := auth.NewTouchBuffer(touchFlush, auth.DefaultTouchFlushInterval, log,
		obs.ApikeyTouchBufferedTotal.Inc,
		obs.ApikeyTouchFlushTotal.Inc,
	)
	tbCtx, tbCancel := context.WithCancel(ctx)
	go touchBuf.Run(tbCtx)
	defer tbCancel() // triggers final flush on shutdown

	verifier := auth.NewVerifier(pool, rdb, log, touchBuf)

	startedAt := time.Now()
	r := buildRouter(log, startedAt, verifier)

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           http.MaxBytesHandler(r, cfg.MaxBodyBytes),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
	}

	serverErr := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Info("http listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-serverErr:
		log.Error("http server failed", "err", err)
		cancel()
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown error", "err", err)
	}
	wg.Wait()
	log.Info("gateway exited cleanly")
}

// buildRouter assembles the chi router + middleware stack and mounts the
// /health, /metrics, and /v1/* scaffold routes. /v1/* is wrapped in an
// auth-protected chi.Group; /health and /metrics stay unauthenticated.
// Extracted so main_test.go can exercise the exact same wiring.
//
// verifier may be nil for the test variant — in that case the auth group
// is replaced with a passthrough so existing scaffold tests keep working
// without booting Redis/Postgres. Production main always supplies a verifier.
func buildRouter(log *slog.Logger, startedAt time.Time, verifier *auth.Verifier) *chi.Mux {
	r := chi.NewRouter()
	r.Use(httpx.RequestID)
	r.Use(httpx.Logger(log))
	r.Use(httpx.Recoverer(log))

	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":   "ok",
			"version":  obs.BuildVersion,
			"uptime_s": int64(time.Since(startedAt).Seconds()),
		})
	})
	r.Handle("/metrics", obs.Handler())

	// Authenticated /v1/* group. Scaffolds remain 501 until Plan 02-04 lands proxies.
	r.Group(func(pg chi.Router) {
		if verifier != nil {
			pg.Use(auth.Middleware(verifier, log))
		}
		for _, route := range []string{
			"/v1/chat/completions",
			"/v1/embeddings",
			"/v1/audio/transcriptions",
			"/v1/health/upstreams",
		} {
			pg.MethodFunc(http.MethodPost, route, scaffoldNotImplemented)
			pg.MethodFunc(http.MethodGet, route, scaffoldNotImplemented)
		}
	})

	// Any other path also returns an OpenAI envelope (not chi's default 404).
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteOpenAIError(w, http.StatusNotFound,
			"invalid_request_error", "not_found",
			"The requested path was not found.")
	})

	return r
}

func scaffoldNotImplemented(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteOpenAIError(w, http.StatusNotImplemented,
		"api_error", "not_implemented",
		"This route will be wired by subsequent Phase 2 plans.")
}

// Compile-time assertion that pgxpool/redis are imported (used inside main).
// Keeps imports honest if main is restructured.
var (
	_ = (*pgxpool.Pool)(nil)
	_ = (*redis.Client)(nil)
)

// newLogger builds the slog.Logger wrapped in the Redactor so sensitive
// attribute values are globally redacted (D-B7).
func newLogger(cfg config.Config) *slog.Logger {
	lvl := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var inner slog.Handler
	if cfg.Env == "development" {
		inner = slog.NewTextHandler(os.Stdout, opts)
	} else {
		inner = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(httpx.NewRedactor(inner)).With("module", "GATEWAY")
}
