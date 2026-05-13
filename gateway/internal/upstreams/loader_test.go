// Package upstreams (loader_test.go): Plan 06-08 Task 1 unit tests for
// OverrideTier0 / RestoreTier0 (D-E3 emergency-pod dispatcher integration).
//
// Race-test coverage: 100 concurrent reader goroutines + 1 writer
// alternating Override/Restore — `-race` flag MUST detect any data race
// on the atomic.Pointer[string] under tier0Override["llm"].
package upstreams

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newOverrideFixture builds a Loader with two upstreams: tier-0 local-llm
// + tier-1 openrouter-chat (mirrors the production Phase 3 setup) so
// override interaction with tier-1 fallback can be asserted.
func newOverrideFixture() *Loader {
	return NewLoaderForTest(
		UpstreamConfig{
			Name:    "local-llm",
			Role:    "llm",
			Tier:    0,
			URL:     "http://primary:8000",
			Enabled: true,
			CircuitConfig: CircuitConfig{
				Failures:  3,
				CooldownS: 30,
				Cooldown:  30 * time.Second,
			},
		},
		UpstreamConfig{
			Name:    "openrouter-chat",
			Role:    "llm",
			Tier:    1,
			URL:     "https://openrouter.example/v1",
			Enabled: true,
		},
		UpstreamConfig{
			Name:    "local-stt",
			Role:    "stt",
			Tier:    0,
			URL:     "http://stt:8000",
			Enabled: true,
		},
	)
}

// TestOverrideTier0 — calling OverrideTier0("llm", url) then Resolve("llm",0)
// returns the override URL with Name="emergency_pod_llm" and IsEmergency=true.
// CircuitConfig + auth fields inherited from the underlying tier-0 row so
// the dispatcher's breaker hooks remain intact.
func TestOverrideTier0(t *testing.T) {
	l := newOverrideFixture()

	// Pre-condition: Resolve returns primary.
	u, ok := l.Resolve("llm", 0)
	if !ok {
		t.Fatalf("pre-condition: Resolve(llm,0) not found")
	}
	if u.URL != "http://primary:8000" {
		t.Fatalf("pre-condition: Resolve URL = %q, want http://primary:8000", u.URL)
	}
	if u.IsEmergency {
		t.Fatalf("pre-condition: IsEmergency must be false before override")
	}

	// Activate override.
	l.OverrideTier0("llm", "http://emergency.pod:8000")

	got, ok := l.Resolve("llm", 0)
	if !ok {
		t.Fatalf("Resolve(llm,0) not found after override")
	}
	if got.URL != "http://emergency.pod:8000" {
		t.Errorf("Resolve URL = %q, want http://emergency.pod:8000", got.URL)
	}
	if got.Name != "emergency_pod_llm" {
		t.Errorf("Resolve Name = %q, want emergency_pod_llm", got.Name)
	}
	if !got.IsEmergency {
		t.Errorf("Resolve IsEmergency = false, want true")
	}
	if got.Role != "llm" || got.Tier != 0 {
		t.Errorf("Resolve Role/Tier = %q/%d, want llm/0", got.Role, got.Tier)
	}
	// Inherited fields from the underlying tier-0 row.
	if got.CircuitConfig.Failures != 3 {
		t.Errorf("CircuitConfig.Failures = %d, want 3 (inherited from primary)",
			got.CircuitConfig.Failures)
	}
}

// TestRestoreTier0 — after OverrideTier0, calling RestoreTier0 returns
// Resolve to the primary URL with Name=local-llm and IsEmergency=false.
func TestRestoreTier0(t *testing.T) {
	l := newOverrideFixture()
	l.OverrideTier0("llm", "http://emergency.pod:8000")

	// Sanity: override is active.
	got, _ := l.Resolve("llm", 0)
	if got.URL != "http://emergency.pod:8000" {
		t.Fatalf("override not active before restore")
	}

	l.RestoreTier0("llm")

	got, ok := l.Resolve("llm", 0)
	if !ok {
		t.Fatalf("Resolve(llm,0) not found after restore")
	}
	if got.URL != "http://primary:8000" {
		t.Errorf("post-restore URL = %q, want http://primary:8000", got.URL)
	}
	if got.Name != "local-llm" {
		t.Errorf("post-restore Name = %q, want local-llm", got.Name)
	}
	if got.IsEmergency {
		t.Errorf("post-restore IsEmergency = true, want false")
	}
}

// TestResolveWithOverride_OnlyTier0 — override applies to tier=0 only.
// Resolve("llm", 1) MUST return the openrouter-chat fallback unchanged.
// Critical: the dispatcher's tier-1 fallback path during emergency must
// continue to work if both primary AND emergency pod fail.
func TestResolveWithOverride_OnlyTier0(t *testing.T) {
	l := newOverrideFixture()
	l.OverrideTier0("llm", "http://emergency.pod:8000")

	got, ok := l.Resolve("llm", 1)
	if !ok {
		t.Fatalf("Resolve(llm,1) not found")
	}
	if got.URL != "https://openrouter.example/v1" {
		t.Errorf("tier-1 URL mutated by tier-0 override: %q", got.URL)
	}
	if got.Name != "openrouter-chat" {
		t.Errorf("tier-1 Name mutated: %q", got.Name)
	}
	if got.IsEmergency {
		t.Errorf("tier-1 IsEmergency = true, want false")
	}
}

// TestOverrideTier0_NonExistentRole — overriding a role not in the
// override map (only "llm" in v1 per D-E3) is a silent no-op. Resolve
// continues to return the snapshot row untouched.
func TestOverrideTier0_NonExistentRole(t *testing.T) {
	l := newOverrideFixture()

	// "stt" is not in the v1 override map; OverrideTier0 must be no-op.
	l.OverrideTier0("stt", "http://emergency.stt:8000")

	got, ok := l.Resolve("stt", 0)
	if !ok {
		t.Fatalf("Resolve(stt,0) not found")
	}
	if got.URL != "http://stt:8000" {
		t.Errorf("non-LLM override leaked: URL = %q, want http://stt:8000", got.URL)
	}
	if got.IsEmergency {
		t.Errorf("non-LLM override leaked: IsEmergency = true, want false")
	}

	// Restore is also no-op for unknown role.
	l.RestoreTier0("stt") // must not panic.
}

// TestRestoreTier0_Idempotent — calling RestoreTier0 when no override is
// active is a no-op (Store(nil) on already-nil pointer). Calling
// RestoreTier0 twice in a row is also a no-op.
func TestRestoreTier0_Idempotent(t *testing.T) {
	l := newOverrideFixture()

	// No override active.
	l.RestoreTier0("llm") // no-op
	got, _ := l.Resolve("llm", 0)
	if got.URL != "http://primary:8000" {
		t.Errorf("Resolve URL = %q after no-op restore, want http://primary:8000", got.URL)
	}

	// Activate then restore twice.
	l.OverrideTier0("llm", "http://emergency:8000")
	l.RestoreTier0("llm")
	l.RestoreTier0("llm") // second call is no-op

	got, _ = l.Resolve("llm", 0)
	if got.URL != "http://primary:8000" {
		t.Errorf("Resolve URL = %q after double restore, want http://primary:8000", got.URL)
	}
}

// TestOverrideTier0_Replaces — a second OverrideTier0 call replaces the
// first URL atomically. Use case: leader recovery resumes a lifecycle
// with a different pod URL than the original (rare but valid — the
// resumed instance might be a different Vast.ai contract).
func TestOverrideTier0_Replaces(t *testing.T) {
	l := newOverrideFixture()
	l.OverrideTier0("llm", "http://emergency.first:8000")
	l.OverrideTier0("llm", "http://emergency.second:8000")

	got, _ := l.Resolve("llm", 0)
	if got.URL != "http://emergency.second:8000" {
		t.Errorf("Resolve URL = %q, want http://emergency.second:8000 (second override)", got.URL)
	}
}

// TestOverrideTier0_RaceFreeReads — 100 reader goroutines + 1 writer
// alternating Override/Restore. With -race, any data race on the
// atomic.Pointer[string] surfaces. Without -race this is still useful as
// a smoke test for the lockless path: readers must always observe
// either the primary URL OR the override URL — never garbage.
//
// Run via: `go test -race -run TestOverrideTier0_RaceFreeReads`.
func TestOverrideTier0_RaceFreeReads(t *testing.T) {
	l := newOverrideFixture()

	const numReaders = 100
	const iterations = 1000

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var failures atomic.Int64

	// Spawn readers.
	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					u, ok := l.Resolve("llm", 0)
					if !ok {
						failures.Add(1)
						return
					}
					switch u.URL {
					case "http://primary:8000", "http://emergency.race:8000":
						// expected
					default:
						failures.Add(1)
						t.Errorf("reader observed unexpected URL: %q", u.URL)
						return
					}
				}
			}
		}()
	}

	// Single writer alternates Override/Restore.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			if i%2 == 0 {
				l.OverrideTier0("llm", "http://emergency.race:8000")
			} else {
				l.RestoreTier0("llm")
			}
		}
		close(stop)
	}()

	wg.Wait()

	if failures.Load() > 0 {
		t.Fatalf("race-test detected %d failures", failures.Load())
	}
}

// TestNewLoaderForTest_IncludesOverrideMap — defensive check that
// NewLoaderForTest constructs the override map. A regression where the
// helper was updated without adding the map would silently disable
// OverrideTier0 (no-op for all calls).
func TestNewLoaderForTest_IncludesOverrideMap(t *testing.T) {
	l := NewLoaderForTest(UpstreamConfig{
		Name: "local-llm", Role: "llm", Tier: 0, URL: "http://primary", Enabled: true,
	})
	l.OverrideTier0("llm", "http://check:8000")
	got, _ := l.Resolve("llm", 0)
	if got.URL != "http://check:8000" {
		t.Fatalf("NewLoaderForTest does not init override map; URL = %q", got.URL)
	}
}

// TestNewLoaderInMemory_IncludesOverrideMap — same defensive check for
// the cross-package helper used by dispatcher tests.
func TestNewLoaderInMemory_IncludesOverrideMap(t *testing.T) {
	l := NewLoaderInMemory(UpstreamConfig{
		Name: "local-llm", Role: "llm", Tier: 0, URL: "http://primary", Enabled: true,
	})
	l.OverrideTier0("llm", "http://check:8000")
	got, _ := l.Resolve("llm", 0)
	if got.URL != "http://check:8000" {
		t.Fatalf("NewLoaderInMemory does not init override map; URL = %q", got.URL)
	}
}
