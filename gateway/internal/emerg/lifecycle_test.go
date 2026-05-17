// Package emerg (lifecycle_test.go): Plan 06-06 unit tests for the pure
// helpers in lifecycle.go.
//
// The full provisionLifecycle + waitForReadyOrDestroy + reconciler-state
// flow is exercised in
// gateway/internal/integration_test/emerg_provision_happy_test.go (build
// tag `integration`) — this file only covers the synchronous, side-effect-
// free helpers that don't need a Postgres + Redis harness.
package emerg

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
)

// TestFilterBelowCap_Epsilon verifies Pitfall 5: epsilon comparison
// `cap + 0.0001`. Offers exactly at the cap pass; offers above the cap
// + epsilon are rejected.
func TestFilterBelowCap_Epsilon(t *testing.T) {
	cap := 0.40
	offers := []vast.Offer{
		{ID: 1, DphTotal: 0.45},   // above cap → rejected
		{ID: 2, DphTotal: 0.35},   // below cap → kept
		{ID: 3, DphTotal: 0.40},   // exactly at cap → kept (epsilon)
		{ID: 4, DphTotal: 0.4001}, // exactly cap+epsilon → kept (boundary)
		{ID: 5, DphTotal: 0.4002}, // just above cap+epsilon → rejected
	}
	got := filterBelowCap(offers, cap)
	require.Len(t, got, 3, "ids 2, 3, 4 must pass; ids 1 + 5 rejected")
	wantIDs := map[int64]bool{2: true, 3: true, 4: true}
	for _, o := range got {
		require.True(t, wantIDs[o.ID], "unexpected offer ID %d in filtered output", o.ID)
	}
}

// TestFilterBelowCap_EmptyInput — defensive: empty in → empty out, not nil panic.
func TestFilterBelowCap_EmptyInput(t *testing.T) {
	got := filterBelowCap(nil, 0.40)
	require.NotNil(t, got, "should return empty slice, not nil")
	require.Len(t, got, 0)
}

// TestExcludeHost — known primary host removed; unknown (hostID=0) keeps all.
func TestExcludeHost(t *testing.T) {
	offers := []vast.Offer{
		{ID: 1, HostID: 100},
		{ID: 2, HostID: 200},
		{ID: 3, HostID: 100},
		{ID: 4, HostID: 300},
	}
	got := excludeHost(offers, 100)
	require.Len(t, got, 2, "host 100 (ids 1, 3) must be removed")
	for _, o := range got {
		require.NotEqual(t, int64(100), o.HostID)
	}

	// hostID=0 is "unknown" — return input unchanged.
	got2 := excludeHost(offers, 0)
	require.Len(t, got2, 4)
}

// TestMustEventJSON — output must be a valid JSON array containing one
// row with the expected `type` + `payload` keys + a `ts` timestamp.
func TestMustEventJSON(t *testing.T) {
	out := mustEventJSON("offer_accepted", map[string]any{
		"offer_id": int64(123),
		"dph":      0.35,
	})
	var parsed []map[string]any
	require.NoError(t, json.Unmarshal(out, &parsed),
		"output must be a valid JSON array")
	require.Len(t, parsed, 1)
	row := parsed[0]
	require.Equal(t, "offer_accepted", row["type"])
	require.NotNil(t, row["ts"], "ts must be populated for audit timeline")
	payload, ok := row["payload"].(map[string]any)
	require.True(t, ok, "payload key must be an object")
	require.InDelta(t, 0.35, payload["dph"], 0.0001)
	require.EqualValues(t, 123, payload["offer_id"])
}

// TestPgInt8 — wrap returns Valid=true.
func TestPgInt8(t *testing.T) {
	v := pgInt8(12345)
	require.True(t, v.Valid)
	require.Equal(t, int64(12345), v.Int64)
}

// TestPgNumericFromFloat — round-trip via Float64Value.
func TestPgNumericFromFloat(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{0.0, 0.0},
		{0.35, 0.35},
		{0.4001, 0.4001},
		{200.0, 200.0},
		{200.1234, 200.1234},
	}
	for _, c := range cases {
		t.Run("", func(t *testing.T) {
			n := pgNumericFromFloat(c.in)
			require.True(t, n.Valid)
			fv, err := n.Float64Value()
			require.NoError(t, err)
			require.True(t, fv.Valid)
			require.InDelta(t, c.want, fv.Float64, 0.0001)
		})
	}
}

// TestPodHealthURL_RunningWithPort — happy path.
func TestPodHealthURL_RunningWithPort(t *testing.T) {
	r := &Reconciler{}
	inst := vast.Instance{
		PublicIPAddr: "1.2.3.4",
		Ports: map[string][]vast.PortBinding{
			"8000/tcp": {{HostIP: "0.0.0.0", HostPort: "40713"}},
		},
	}
	require.Equal(t, "http://1.2.3.4:40713/v1/models", r.podHealthURL(inst))
}

// TestPodHealthURL_W6_Empty — Pitfall 6 fix: any of (no IP, no ports
// entry, empty bindings, empty HostPort) returns "".
func TestPodHealthURL_W6_Empty(t *testing.T) {
	r := &Reconciler{}

	// No IP.
	require.Equal(t, "", r.podHealthURL(vast.Instance{
		Ports: map[string][]vast.PortBinding{"8000/tcp": {{HostPort: "40713"}}},
	}))

	// No ports map at all.
	require.Equal(t, "", r.podHealthURL(vast.Instance{PublicIPAddr: "1.2.3.4"}))

	// 9100/tcp absent.
	require.Equal(t, "", r.podHealthURL(vast.Instance{
		PublicIPAddr: "1.2.3.4",
		Ports:        map[string][]vast.PortBinding{"22/tcp": {{HostPort: "30000"}}},
	}))

	// Empty bindings list.
	require.Equal(t, "", r.podHealthURL(vast.Instance{
		PublicIPAddr: "1.2.3.4",
		Ports:        map[string][]vast.PortBinding{"8000/tcp": {}},
	}))

	// Empty HostPort string.
	require.Equal(t, "", r.podHealthURL(vast.Instance{
		PublicIPAddr: "1.2.3.4",
		Ports:        map[string][]vast.PortBinding{"8000/tcp": {{HostPort: ""}}},
	}))
}

// TestErrReason — sanity for the error-token mapping used in FSM transition reasons.
func TestErrReason(t *testing.T) {
	require.Equal(t, "offer_race_lost", errReason(ErrOfferRaceLost))
	require.Equal(t, "health_timeout", errReason(ErrHealthTimeout))
	require.Equal(t, "instance_terminal_state", errReason(ErrInstanceTerminal))
	require.Equal(t, "no_offers_below_cap", errReason(ErrNoOffersBelowCap))
	require.Equal(t, "other", errReason(errors.New("unrelated error")))
}

// ---------------------------------------------------------------------
// Plan 06-04 — Strategy B Locked buildCreateRequest unit tests.
//
// Pattern revised per 06-WAVE0-GATES.md Decision 4 (supersedes plan
// must_haves truth #6 verbatim 15-token args slice): runtype=args with
// entrypoint=/bin/bash + args=["-c", <onstart-script>]. The 15
// llama-server flags now live inside the onstart script's final
// `exec /app/llama-server ...` line, NOT in the wire-level Args array.
// Empirical evidence: 06-SPIKE-runtype-args.md Round 2 (Vast CLI
// `--onstart-cmd` does NOT shell-wrap in args runtype; entrypoint
// override is mandatory).
// ---------------------------------------------------------------------

// newReconcilerForBuildTest constructs a minimal Reconciler with Cfg
// populated for the buildCreateRequest payload assertions. The Reconciler
// has no Redis/DB wiring — buildCreateRequest is a pure function on
// r.deps.Cfg and the offer/lifecycleID args.
func newReconcilerForBuildTest(jinjaKey, jinjaSHA string, llamaArgsOverride []string) *Reconciler {
	return &Reconciler{
		deps: Deps{
			Cfg: config.Config{
				EmergencyTemplateImage:       "ghcr.io/ggml-org/llama.cpp:server-cuda-b9128",
				EmergencyJinjaTemplateKey:    jinjaKey,
				EmergencyJinjaTemplateSHA256: jinjaSHA,
				EmergencyLlamaArgs:           llamaArgsOverride,
				MinioEndpoint:                "https://s3.example.com",
				MinioBucket:                  "ai-gateway",
				MinioAccessKey:               "AKID-test",
				MinioSecretKey:               "SK-test",
				WeightsQwenKey:               "qwen3.5-27b-Q4_K_M/v1.0.0/model.gguf",
				WeightsQwenSHA256:            "abc123deadbeef",
			},
		},
	}
}

// TestBuildCreateRequest_StrategyB_args verifies the Strategy B Locked
// payload shape (06-WAVE0-GATES.md Decision 4 — supersedes the verbatim
// 15-token Args slice in plan must_haves):
//
//   - Image == EmergencyTemplateImage (NOT ghcr.io/ifixtelecom/ifix-ai-pod)
//   - Runtype == "args"
//   - Entrypoint == "/bin/bash" (REQUIRED — spike Round 2 evidence)
//   - Args has exactly 2 elements: ["-c", <onstart-script>]
//   - Args[1] contains the inline `exec /app/llama-server` with all
//     15 llama-server CLI flags (not in the wire Args slice — bug fix
//     STATE.md:85)
//   - Label uses lifecycle ID
//   - Disk == 40 (WAVE0-GATES Decision 1 — 40 GB opens more spot hosts)
//   - Env map purged of Whisper/BGE-M3 keys (LLM-only emergency pod
//     per CONTEXT.md `<deferred>` line 171)
func TestBuildCreateRequest_StrategyB_args(t *testing.T) {
	r := newReconcilerForBuildTest("emerg-onstart/templates/foo.jinja", "deadbeefSHA", nil)
	req := r.buildCreateRequest(vast.Offer{ID: 999}, 7)

	require.Equal(t, "ghcr.io/ggml-org/llama.cpp:server-cuda-b9128", req.Image)
	require.Equal(t, "args", req.Runtype)
	require.Equal(t, "/bin/bash", req.Entrypoint)
	require.Len(t, req.Args, 2, "Strategy B Locked: args=[\"-c\", <script>] only (2 elements)")
	require.Equal(t, "-c", req.Args[0])
	require.Contains(t, req.Args[1], "exec /app/llama-server", "onstart MUST end with exec /app/llama-server so PID 1 == llama-server (spike Round 2 pattern)")
	require.Contains(t, req.Args[1], "--host 0.0.0.0", "default llama-server args MUST be embedded in onstart")
	require.Contains(t, req.Args[1], "--jinja", "default llama-server args MUST be embedded in onstart")
	require.Contains(t, req.Args[1], "/weights/qwen/model.gguf", "onstart MUST reference Qwen weights path")
	require.Contains(t, req.Args[1], "WEIGHTS_QWEN_SHA256", "onstart MUST sha256 verify Qwen weights")
	require.Equal(t, "ifix-emerg-lifecycle-7", req.Label)
	require.Equal(t, 40, req.Disk)

	// LLM-only emergency pod — no Whisper or BGE-M3 env keys.
	_, hasWhisperKey := req.Env["WEIGHTS_WHISPER_KEY"]
	_, hasBgeKey := req.Env["WEIGHTS_BGE_M3_KEY"]
	require.False(t, hasWhisperKey, "WEIGHTS_WHISPER_KEY must be removed — emergency pod is LLM-only (CONTEXT.md deferred line 171)")
	require.False(t, hasBgeKey, "WEIGHTS_BGE_M3_KEY must be removed — emergency pod is LLM-only")

	// MinIO + Qwen creds preserved.
	require.Equal(t, "https://s3.example.com", req.Env["MINIO_ENDPOINT"])
	require.Equal(t, "ai-gateway", req.Env["MINIO_BUCKET"])
	require.Equal(t, "AKID-test", req.Env["MINIO_ACCESS_KEY"])
	require.Equal(t, "SK-test", req.Env["MINIO_SECRET_KEY"])
	require.Equal(t, "qwen3.5-27b-Q4_K_M/v1.0.0/model.gguf", req.Env["WEIGHTS_QWEN_KEY"])
	require.Equal(t, "abc123deadbeef", req.Env["WEIGHTS_QWEN_SHA256"])

	// B2 mode (non-empty Jinja key) — env carries Jinja key + sha256 so
	// onstart-shell can fetch + verify.
	require.Equal(t, "emerg-onstart/templates/foo.jinja", req.Env["EMERGENCY_JINJA_TEMPLATE_KEY"])
	require.Equal(t, "deadbeefSHA", req.Env["EMERGENCY_JINJA_TEMPLATE_SHA256"])
}

// TestBuildCreateRequest_JSONShape verifies the wire-level JSON shape
// of the request body. Critical assertions:
//
//   - Top-level "args" key present, "image_args" + "args_str" absent
//     (VERIFIED via vast-cli/vast.py:2509 RESEARCH.md Pitfall 5 — the
//     server only reads `args`)
//   - "entrypoint" key present at top level
//   - "onstart" key absent or empty (Strategy B runs the script via
//     args=["-c", ...], NOT via the onstart field — Vast `--onstart-cmd`
//     does not shell-wrap in args runtype per spike Round 1)
//   - "runtype" == "args"
func TestBuildCreateRequest_JSONShape(t *testing.T) {
	r := newReconcilerForBuildTest("emerg-onstart/templates/foo.jinja", "deadbeefSHA", nil)
	req := r.buildCreateRequest(vast.Offer{ID: 1}, 42)

	raw, err := json.Marshal(req)
	require.NoError(t, err)
	js := string(raw)

	// Decode + introspect top-level keys.
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &top))

	require.Contains(t, top, "image", "top-level image key must exist")
	require.Contains(t, top, "runtype", "top-level runtype key must exist")
	require.Contains(t, top, "entrypoint", "top-level entrypoint key must exist (Strategy B requirement)")
	require.Contains(t, top, "args", "top-level args key must exist (Strategy B requirement)")
	require.Contains(t, top, "env", "top-level env key must exist")
	require.Contains(t, top, "disk", "top-level disk key must exist")

	require.NotContains(t, top, "image_args", "wire field is `args`, NOT image_args (vast-cli/vast.py:2509)")
	require.NotContains(t, top, "args_str", "wire field is `args`, NOT args_str")

	// Onstart field — omitempty in struct, but the current DTO has it as
	// a non-omitempty string. Assert it is empty when present.
	if onstartRaw, ok := top["onstart"]; ok {
		var s string
		require.NoError(t, json.Unmarshal(onstartRaw, &s))
		require.Empty(t, s, "Strategy B: onstart field must be empty — script lives in args[1] (Vast onstart-cmd does not shell-wrap in args runtype, spike Round 1)")
	}

	require.Contains(t, js, `"runtype":"args"`, "raw JSON sanity check")
	require.Contains(t, js, `"entrypoint":"/bin/bash"`, "raw JSON sanity check")
}

// TestBuildCreateRequest_DeterministicJSON verifies that two successive
// calls with identical Cfg produce byte-identical JSON. No time.Now, no
// rand. Map ordering would defeat this naively — json.Marshal sorts map
// keys alphabetically, so determinism comes for free as long as the
// inputs are stable.
func TestBuildCreateRequest_DeterministicJSON(t *testing.T) {
	r := newReconcilerForBuildTest("emerg-onstart/templates/foo.jinja", "deadbeefSHA", nil)
	offer := vast.Offer{ID: 999}

	first, err := json.Marshal(r.buildCreateRequest(offer, 7))
	require.NoError(t, err)
	for i := 0; i < 20; i++ {
		next, err := json.Marshal(r.buildCreateRequest(offer, 7))
		require.NoError(t, err)
		require.Equal(t, string(first), string(next), "buildCreateRequest must be deterministic across calls")
	}
}

// TestBuildCreateRequest_JinjaB1Mode — Cfg.EmergencyJinjaTemplateKey
// empty (B1 fallback path) means no Jinja env keys forwarded. The
// onstart's `if [[ -n "${EMERGENCY_JINJA_TEMPLATE_KEY:-}" ]]` block
// short-circuits and llama-server runs WITHOUT --chat-template-file,
// falling back to image-embedded template. NOTE: production config
// defaults non-empty (B2 LOCKED per WAVE0-GATES Decision 1) — this
// test exists to validate the runtime override path for operator
// emergencies where Jinja becomes unavailable.
func TestBuildCreateRequest_JinjaB1Mode(t *testing.T) {
	r := newReconcilerForBuildTest("", "", nil)
	req := r.buildCreateRequest(vast.Offer{ID: 1}, 1)

	_, hasKey := req.Env["EMERGENCY_JINJA_TEMPLATE_KEY"]
	_, hasSHA := req.Env["EMERGENCY_JINJA_TEMPLATE_SHA256"]
	require.False(t, hasKey, "EMERGENCY_JINJA_TEMPLATE_KEY must be absent in B1 mode")
	require.False(t, hasSHA, "EMERGENCY_JINJA_TEMPLATE_SHA256 must be absent in B1 mode")
}

// TestBuildCreateRequest_JinjaB2Mode — Cfg.EmergencyJinjaTemplateKey
// non-empty (B2 production default per WAVE0-GATES Decision 1) means
// both Jinja env keys forwarded. The onstart shell script will fetch
// + sha256-verify the Jinja template from MinIO.
func TestBuildCreateRequest_JinjaB2Mode(t *testing.T) {
	r := newReconcilerForBuildTest(
		"emerg-onstart/templates/qwen3.5-27b-tool-calling-XYZ.jinja",
		"sha256-hex-value",
		nil,
	)
	req := r.buildCreateRequest(vast.Offer{ID: 1}, 1)

	require.Equal(t, "emerg-onstart/templates/qwen3.5-27b-tool-calling-XYZ.jinja", req.Env["EMERGENCY_JINJA_TEMPLATE_KEY"])
	require.Equal(t, "sha256-hex-value", req.Env["EMERGENCY_JINJA_TEMPLATE_SHA256"])
}

// TestBuildCreateRequest_LlamaArgsOverride — operator can override the
// hard-coded llama-server flag slice via EMERGENCY_LLAMA_ARGS env CSV
// (Cfg.EmergencyLlamaArgs). The onstart script's final `exec
// /app/llama-server ...` line uses the override slice when non-nil/
// non-empty; otherwise the 13-flag default. This test verifies the
// override path lands inside the onstart script.
func TestBuildCreateRequest_LlamaArgsOverride(t *testing.T) {
	override := []string{"--port", "9999", "--verbose"}
	r := newReconcilerForBuildTest("", "", override)
	req := r.buildCreateRequest(vast.Offer{ID: 1}, 1)

	require.Len(t, req.Args, 2)
	script := req.Args[1]
	require.Contains(t, script, "exec /app/llama-server --port 9999 --verbose",
		"override slice MUST appear in onstart exec line")
	require.NotContains(t, script, "--host 0.0.0.0",
		"override REPLACES default flags entirely; default --host must not leak")
	require.NotContains(t, script, "--jinja",
		"override REPLACES default flags entirely; default --jinja must not leak")
}

// TestEmergencyOnstart_Under1500Chars — Pitfall 4 RESEARCH.md:426
// enforcement. Vast API hard limit is 4048 chars; plan must_haves
// truth #4 sets 1500 char safety margin so growth (new env var or
// extra sha256 check) does not unexpectedly cross the boundary.
// If this fails, gzip+base64 the script before assembling Args.
func TestEmergencyOnstart_Under1500Chars(t *testing.T) {
	r := newReconcilerForBuildTest("emerg-onstart/templates/foo.jinja", "deadbeefSHA", nil)
	req := r.buildCreateRequest(vast.Offer{ID: 1}, 1)
	require.Len(t, req.Args, 2)
	require.Less(t, len(req.Args[1]), 1500,
		"onstart script must stay under 1500 chars (Vast 4048 limit, margin per plan must_haves truth #4); gzip+base64 if growth needed")
}

// TestEmergencyOnstart_StartsWithSetE — script MUST begin with `set -e`
// so any failed step (mc download fail, sha256 mismatch) aborts the
// container with a non-zero exit. Without set -e, a silent sha256
// mismatch would still let llama-server start on tampered weights.
func TestEmergencyOnstart_StartsWithSetE(t *testing.T) {
	r := newReconcilerForBuildTest("emerg-onstart/templates/foo.jinja", "deadbeefSHA", nil)
	req := r.buildCreateRequest(vast.Offer{ID: 1}, 1)
	require.Len(t, req.Args, 2)
	require.True(t, strings.HasPrefix(req.Args[1], "set -e"),
		"onstart MUST start with `set -e` so download/sha256 failures abort container (T-06-03 mitigation)")
}

// TestEmergencyOnstart_NoLegacyImage — defensive guard: the legacy
// `ghcr.io/ifixtelecom/ifix-ai-pod` image must NOT appear anywhere
// in the request (Image field nor environment). Phase 6 D-08-B
// (Strategy B Locked) eliminates the custom GHCR image.
func TestEmergencyOnstart_NoLegacyImage(t *testing.T) {
	r := newReconcilerForBuildTest("emerg-onstart/templates/foo.jinja", "deadbeefSHA", nil)
	req := r.buildCreateRequest(vast.Offer{ID: 1}, 1)
	raw, err := json.Marshal(req)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "ifix-ai-pod",
		"Strategy B Locked: legacy ifix-ai-pod image must be gone (CONTEXT.md D-08-B + STATE.md:85 bug fix)")
}
