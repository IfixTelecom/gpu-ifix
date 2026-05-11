package upstreams

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgxpool"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

// snapshot is the immutable view of the upstreams table the Loader serves
// from its hot path. Built fresh on every Refresh and atomically swapped
// in via atomic.Pointer[snapshot] so reads are lock-free.
type snapshot struct {
	byName     map[string]UpstreamConfig
	byRoleTier map[RoleTier]UpstreamConfig
	ordered    []UpstreamConfig
}

// loaderQueries isolates the sqlc surface so tests can stub it without
// standing up a real Postgres pool. Mirrors the resolverQueries pattern
// in gateway/internal/models/resolver.go.
type loaderQueries interface {
	ListEnabledUpstreams(ctx context.Context) ([]gen.AiGatewayUpstream, error)
}

// Loader holds the in-memory authoritative snapshot of ai_gateway.upstreams.
// Readers call Resolve/Get/All on the hot path (atomic.Pointer — lock-free).
// Refresh is called at boot + on each LISTEN/NOTIFY from upstreams_changed.
type Loader struct {
	pool *pgxpool.Pool
	q    loaderQueries
	snap atomic.Pointer[snapshot]
	log  *slog.Logger
}

// NewLoader constructs the Loader and performs the initial Refresh.
// Returns an error if the initial SELECT fails (boot MUST fail-fast if
// the upstreams table is unreadable).
func NewLoader(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) (*Loader, error) {
	l := &Loader{
		pool: pool,
		q:    gen.New(pool),
		log:  log.With("module", "UPSTREAMS"),
	}
	if err := l.Refresh(ctx); err != nil {
		return nil, fmt.Errorf("initial upstreams refresh: %w", err)
	}
	return l, nil
}

// Refresh loads all enabled rows and atomically swaps in a new snapshot.
// Missing env-var values (url_env not set) cause the row to be SKIPPED
// with a warn log — the dispatcher returns 503 if requested. This keeps
// the gateway bootable even when a fallback provider's bearer is not yet
// configured (CONTEXT.md "Plumbing" / 03-04-PLAN must_haves.truths).
func (l *Loader) Refresh(ctx context.Context) error {
	rows, err := l.q.ListEnabledUpstreams(ctx)
	if err != nil {
		obs.UpstreamsReloadTotal.WithLabelValues("error").Inc()
		return fmt.Errorf("list enabled upstreams: %w", err)
	}
	s := &snapshot{
		byName:     make(map[string]UpstreamConfig, len(rows)),
		byRoleTier: make(map[RoleTier]UpstreamConfig, len(rows)),
		ordered:    make([]UpstreamConfig, 0, len(rows)),
	}
	for _, r := range rows {
		// Phase 5 / WR-02: reject upstream names starting with "force:"
		// because they collide with the gw:shed:force:{upstream} Redis
		// namespace used by the shed-force operator override. A row named
		// "force:something" would write its state mirror to
		// gw:shed:force:something — indistinguishable from a real
		// override key and silently filtered out by AllShedStateKeys.
		if strings.HasPrefix(r.Name, "force:") {
			l.log.Warn("upstream name reserved (collides with gw:shed:force:* namespace); row skipped",
				"upstream", r.Name,
				"status", "reserved_name")
			continue
		}
		url := os.Getenv(r.UrlEnv)
		if url == "" {
			l.log.Warn("upstream url env var missing; row skipped",
				"upstream", r.Name,
				"url_env", r.UrlEnv,
				"status", "missing_url_env")
			continue
		}
		authBearerEnv := ""
		authBearer := ""
		if r.AuthBearerEnv.Valid {
			authBearerEnv = r.AuthBearerEnv.String
			authBearer = os.Getenv(authBearerEnv)
			if authBearer == "" {
				l.log.Warn("upstream auth bearer env missing; row kept but auth will be empty",
					"upstream", r.Name,
					"auth_bearer_env", authBearerEnv,
					"status", "missing_auth_bearer_env")
			}
		}
		var weight *int32
		if r.Weight.Valid {
			w := r.Weight.Int32
			weight = &w
		}
		u := UpstreamConfig{
			ID:            r.ID,
			Name:          r.Name,
			Role:          r.Role,
			Tier:          int(r.Tier),
			URL:           url,
			AuthBearer:    authBearer,
			AuthBearerEnv: authBearerEnv,
			Enabled:       r.Enabled,
			Weight:        weight,
			CircuitConfig: parseCircuitConfig(r.CircuitConfig),
		}
		s.byName[u.Name] = u
		s.byRoleTier[RoleTier{Role: u.Role, Tier: u.Tier}] = u
		s.ordered = append(s.ordered, u)
	}
	// Stable order for All() — by (role, tier) so callers see a
	// deterministic listing in /v1/health/upstreams + gatewayctl output.
	sort.SliceStable(s.ordered, func(i, j int) bool {
		if s.ordered[i].Role != s.ordered[j].Role {
			return s.ordered[i].Role < s.ordered[j].Role
		}
		return s.ordered[i].Tier < s.ordered[j].Tier
	})
	l.snap.Store(s)
	obs.UpstreamsReloadTotal.WithLabelValues("ok").Inc()
	l.log.Info("upstreams refreshed", "rows", len(s.byName))
	return nil
}

// Get returns the upstream by name + found flag. Lock-free (atomic.Pointer read).
func (l *Loader) Get(name string) (UpstreamConfig, bool) {
	s := l.snap.Load()
	if s == nil {
		return UpstreamConfig{}, false
	}
	u, ok := s.byName[name]
	return u, ok
}

// Resolve returns the upstream for (role, tier). Hot path used by the
// dispatcher: tier-0 CLOSED → primary; tier-0 OPEN → Resolve(role, 1) for fallback.
func (l *Loader) Resolve(role string, tier int) (UpstreamConfig, bool) {
	s := l.snap.Load()
	if s == nil {
		return UpstreamConfig{}, false
	}
	u, ok := s.byRoleTier[RoleTier{Role: role, Tier: tier}]
	return u, ok
}

// All returns all upstreams ordered by (role, tier). Used by /v1/health/upstreams
// and gatewayctl upstreams list. Returns a defensive copy so callers cannot
// mutate the snapshot's internal slice.
func (l *Loader) All() []UpstreamConfig {
	s := l.snap.Load()
	if s == nil {
		return nil
	}
	out := make([]UpstreamConfig, len(s.ordered))
	copy(out, s.ordered)
	return out
}

// Names returns the list of all upstream names in the current snapshot.
// Used by breaker.Set.Rebuild on hot-reload (Wave 2 — 03-05).
func (l *Loader) Names() []string {
	s := l.snap.Load()
	if s == nil {
		return nil
	}
	out := make([]string, 0, len(s.byName))
	for n := range s.byName {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
