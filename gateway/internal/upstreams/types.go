package upstreams

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// UpstreamConfig is one row of ai_gateway.upstreams resolved to live
// runtime values. URL and AuthBearer are filled from os.Getenv at Refresh
// time; they are NOT persisted in the DB — only the env var names are.
//
// AuthBearer is tagged json:"-" so it is NEVER serialized into log lines,
// /v1/health/upstreams payloads, or gatewayctl JSON output (T-03-04-03
// mitigation in 03-04-PLAN.md threat model).
type UpstreamConfig struct {
	ID            uuid.UUID     `json:"id"`
	Name          string        `json:"name"`
	Role          string        `json:"role"` // "llm" | "stt" | "embed"
	Tier          int           `json:"tier"` // 0 = primary, 1 = fallback
	URL           string        `json:"url"`
	AuthBearer    string        `json:"-"` // resolved; NEVER log/serialize
	AuthBearerEnv string        `json:"auth_bearer_env,omitempty"`
	Enabled       bool          `json:"enabled"`
	Weight        *int32        `json:"weight,omitempty"` // Phase 5 populates
	CircuitConfig CircuitConfig `json:"circuit_config"`
}

// CircuitConfig overrides breaker defaults for a specific upstream. Loaded
// from the JSONB column ai_gateway.upstreams.circuit_config. Zero values
// (0 failures, 0 cooldown) mean "use defaults" — the dispatcher merges
// with breaker.DefaultOptions() at Execute time.
//
// CooldownS is the on-disk representation (seconds, JSON-friendly);
// Cooldown is the parsed time.Duration computed by parseCircuitConfig and
// is NOT serialized back to JSON (json:"-").
type CircuitConfig struct {
	Failures  uint32        `json:"failures,omitempty"`
	Cooldown  time.Duration `json:"-"`
	CooldownS int           `json:"cooldown_s,omitempty"` // DB stores seconds
}

// RoleTier keys the by-role-tier snapshot map. String-serializable for
// debug printing via fmt.Stringer.
type RoleTier struct {
	Role string
	Tier int
}

// String implements fmt.Stringer so RoleTier can be logged inline as
// e.g. "llm:0" without manual concatenation.
func (rt RoleTier) String() string {
	return rt.Role + ":" + strconv.Itoa(rt.Tier)
}

// parseCircuitConfig unmarshals the JSONB column into a CircuitConfig.
// Empty bytes or invalid JSON → zero-value CircuitConfig (dispatcher uses
// breaker defaults). Cooldown is computed from CooldownS (seconds) so
// downstream callers can use the time.Duration directly.
func parseCircuitConfig(raw []byte) CircuitConfig {
	var cc CircuitConfig
	if len(raw) == 0 {
		return cc
	}
	if err := json.Unmarshal(raw, &cc); err != nil {
		return CircuitConfig{} // swallow parse errors; defaults will apply
	}
	cc.Cooldown = time.Duration(cc.CooldownS) * time.Second
	return cc
}
