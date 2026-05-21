// Package vast — unit tests for DTO JSON shapes. These tests pin the wire
// format Vast.ai expects so a refactor cannot silently rename the JSON keys
// (`args` vs `image_args` vs `args_str` is the Pitfall 5 trap documented in
// 06-RESEARCH.md line 436).
package vast

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCreateRequest_ArgsOmitempty asserts that the new Args field added in
// plan 06-03 marshals to the JSON key `args` (lowercase, no prefix), and
// that the omitempty tag suppresses the field when zero-valued (so legacy
// ssh/ssh_proxy runtypes do not send a spurious `"args":null`).
func TestCreateRequest_ArgsOmitempty(t *testing.T) {
	t.Run("populated_emits_args_key", func(t *testing.T) {
		req := CreateRequest{Args: []string{"--host", "0.0.0.0"}}
		out, err := json.Marshal(req)
		require.NoError(t, err)
		require.Contains(t, string(out), `"args":["--host","0.0.0.0"]`,
			"Args field MUST serialize to JSON key `args` (Pitfall 5: NOT image_args, NOT args_str)")
	})

	t.Run("zero_value_omits_args_key", func(t *testing.T) {
		req := CreateRequest{} // Args is nil
		out, err := json.Marshal(req)
		require.NoError(t, err)
		require.NotContains(t, string(out), `"args"`,
			"omitempty MUST suppress the args key when Args is nil so ssh/ssh_proxy runtypes do not send it")
	})

	t.Run("wrong_keys_never_appear", func(t *testing.T) {
		req := CreateRequest{Args: []string{"x"}}
		out, err := json.Marshal(req)
		require.NoError(t, err)
		s := string(out)
		require.False(t, strings.Contains(s, "image_args"),
			"image_args is the WRONG key per RESEARCH.md Pitfall 5")
		require.False(t, strings.Contains(s, "args_str"),
			"args_str is the WRONG key per RESEARCH.md Pitfall 5")
	})
}

// TestCreateRequest_EntrypointOmitempty asserts the new Entrypoint field
// added in plan 06-03 (per WAVE0-GATES Decision 4 — spike Round 2 finding
// that Strategy B requires entrypoint override). Marshals to JSON key
// `entrypoint`; omitempty suppresses for legacy runtypes.
func TestCreateRequest_EntrypointOmitempty(t *testing.T) {
	t.Run("populated_emits_entrypoint_key", func(t *testing.T) {
		req := CreateRequest{Entrypoint: "/bin/bash"}
		out, err := json.Marshal(req)
		require.NoError(t, err)
		require.Contains(t, string(out), `"entrypoint":"/bin/bash"`,
			"Entrypoint field MUST serialize to JSON key `entrypoint` (matches vastai CLI --entrypoint)")
	})

	t.Run("zero_value_omits_entrypoint_key", func(t *testing.T) {
		req := CreateRequest{} // Entrypoint is ""
		out, err := json.Marshal(req)
		require.NoError(t, err)
		require.NotContains(t, string(out), `"entrypoint"`,
			"omitempty MUST suppress entrypoint key when zero so ssh/ssh_proxy runtypes do not send it")
	})
}

// TestCreateRequest_StrategyB_FullShape pins the exact wire payload Strategy B
// emits (per 06-SPIKE-runtype-args.md Round 2 + 06-WAVE0-GATES.md Decision 4).
// This is the "golden" shape plan 06-04 buildCreateRequest will produce.
func TestCreateRequest_StrategyB_FullShape(t *testing.T) {
	req := CreateRequest{
		ClientID:   "me",
		Image:      "ghcr.io/ggml-org/llama.cpp:server-cuda-b9128",
		Env:        map[string]string{"-p 8000:8000": "1"},
		Runtype:    "args",
		Entrypoint: "/bin/bash",
		Args:       []string{"-c", "exec /app/llama-server --version"},
		Disk:       40,
		Label:      "ifix-emerg-test",
	}
	out, err := json.Marshal(req)
	require.NoError(t, err)
	s := string(out)
	require.Contains(t, s, `"runtype":"args"`)
	require.Contains(t, s, `"entrypoint":"/bin/bash"`)
	require.Contains(t, s, `"args":["-c","exec /app/llama-server --version"]`)
	require.Contains(t, s, `"image":"ghcr.io/ggml-org/llama.cpp:server-cuda-b9128"`)
	require.Contains(t, s, `"disk":40`)
}

// TestDefaultSearchFilter_NumGPUs covers the num_gpus knob (PRIMARY_NUM_GPUS):
// an explicit count sets num_gpus:{eq:N} (2 for the 2×3090 single-pod topology),
// and a non-positive value falls back to 1 (preserves single-GPU default).
func TestDefaultSearchFilter_NumGPUs(t *testing.T) {
	t.Run("explicit_count", func(t *testing.T) {
		f := DefaultSearchFilter(1.0, 0, "RTX 3090", 2)
		ng := f["num_gpus"].(map[string]any)
		require.Equal(t, 2, ng["eq"], "num_gpus must reflect the requested count")
	})
	t.Run("non_positive_falls_back_to_1", func(t *testing.T) {
		f := DefaultSearchFilter(1.0, 0, "RTX 4090", 0)
		ng := f["num_gpus"].(map[string]any)
		require.Equal(t, 1, ng["eq"], "numGPUs<=0 must default to single GPU")
	})
}

// TestWithMachineAllowlist covers the PRIMARY_VAST_MACHINE_ALLOWLIST preference
// pass: a non-empty allowlist sets machine_id:{in:[...]} (overwriting any
// blocklist notin clause), an empty allowlist is a no-op, and the original
// filter is not mutated (the reconciler reuses it for the broaden-fallback).
func TestWithMachineAllowlist(t *testing.T) {
	t.Run("sets_in_clause_and_overwrites_blocklist", func(t *testing.T) {
		base := DefaultSearchFilter(1.0, 0, "RTX 3090", 1, 111, 222) // blocklist 111,222
		out := WithMachineAllowlist(base, []int64{333, 444})
		mid, ok := out["machine_id"].(map[string]any)
		require.True(t, ok, "machine_id clause must be present")
		require.Contains(t, mid, "in", "allowlist must use the `in` clause")
		require.NotContains(t, mid, "notin", "allowlist overwrites the blocklist `notin`")
		require.ElementsMatch(t, []any{int64(333), int64(444)}, mid["in"])
	})

	t.Run("empty_allowlist_is_noop", func(t *testing.T) {
		base := DefaultSearchFilter(1.0, 0, "RTX 3090", 1, 111)
		out := WithMachineAllowlist(base, nil)
		require.Equal(t, base["machine_id"], out["machine_id"],
			"empty allowlist must leave the blocklist clause untouched")
	})

	t.Run("does_not_mutate_input", func(t *testing.T) {
		base := DefaultSearchFilter(1.0, 0, "RTX 3090", 1, 111, 222)
		_ = WithMachineAllowlist(base, []int64{333})
		mid := base["machine_id"].(map[string]any)
		require.Contains(t, mid, "notin",
			"WithMachineAllowlist must not mutate the input filter (reconciler reuses it for broaden-fallback)")
	})
}
