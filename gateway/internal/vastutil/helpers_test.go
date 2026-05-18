// Package vastutil (helpers_test.go): unit tests ported verbatim from
// gateway/internal/emerg/lifecycle_test.go:26-118 (the 5 pure-helper
// assertions). Adds two net-new tests for the helpers that grew arity
// (or lost their receiver) during extraction:
//
//   - TestBestEffortDestroy_CallsDestroyInstance — fake VastDestroyer
//     captures the instanceID; helper tolerates a non-nil error from
//     the fake.
//   - TestCaptureBreadcrumb_NoOp_WhenNoSentryHub — defensive: helper
//     does not panic when Sentry has never been initialized (ops
//     scripts + this very test binary exercise that path).
//
// Imports kept minimal — zero new external deps in go.mod (T-06.6-SC).
package vastutil

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
)

// ---------------------------------------------------------------------
// 5 pure-helper tests ported from emerg/lifecycle_test.go:26-118 with
// identifier capitalization (free functions are exported now).
// ---------------------------------------------------------------------

// TestFilterBelowCap_Epsilon — Pitfall 5: epsilon comparison
// `cap + 0.0001`. Offers exactly at the cap pass; offers above the cap
// + epsilon are rejected.
func TestFilterBelowCap_Epsilon(t *testing.T) {
	cap := 0.40
	offers := []vast.Offer{
		{ID: 1, DphTotal: 0.45},   // above cap → rejected
		{ID: 2, DphTotal: 0.35},   // below cap → kept
		{ID: 3, DphTotal: 0.40},   // exactly at cap → kept
		{ID: 4, DphTotal: 0.4001}, // exactly cap+epsilon → kept (boundary)
		{ID: 5, DphTotal: 0.4002}, // just above cap+epsilon → rejected
	}
	got := FilterBelowCap(offers, cap)
	require.Len(t, got, 3, "ids 2, 3, 4 must pass; ids 1 + 5 rejected")
	wantIDs := map[int64]bool{2: true, 3: true, 4: true}
	for _, o := range got {
		require.True(t, wantIDs[o.ID], "unexpected offer ID %d in filtered output", o.ID)
	}
}

// TestFilterBelowCap_EmptyInput — defensive: empty in → empty non-nil out.
func TestFilterBelowCap_EmptyInput(t *testing.T) {
	got := FilterBelowCap(nil, 0.40)
	require.NotNil(t, got, "should return empty slice, not nil")
	require.Len(t, got, 0)
}

// TestExcludeHost — known host removed; unknown (hostID=0) keeps all.
func TestExcludeHost(t *testing.T) {
	offers := []vast.Offer{
		{ID: 1, HostID: 100},
		{ID: 2, HostID: 200},
		{ID: 3, HostID: 100},
		{ID: 4, HostID: 300},
	}
	got := ExcludeHost(offers, 100)
	require.Len(t, got, 2, "host 100 (ids 1, 3) must be removed")
	for _, o := range got {
		require.NotEqual(t, int64(100), o.HostID)
	}

	// hostID=0 is "unknown" — return input unchanged.
	got2 := ExcludeHost(offers, 0)
	require.Len(t, got2, 4)
}

// TestMustEventJSON — output must be a valid JSON array containing one
// row with the expected `type` + `payload` keys + a `ts` timestamp.
func TestMustEventJSON(t *testing.T) {
	out := MustEventJSON("offer_accepted", map[string]any{
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
	v := PgInt8(12345)
	require.True(t, v.Valid)
	require.Equal(t, int64(12345), v.Int64)
}

// TestPgNumericFromFloat — round-trip via Float64Value. Verbatim cases
// from emerg/lifecycle_test.go:97-118 (PATTERNS.md:154-176 spec).
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
			n := PgNumericFromFloat(c.in)
			require.True(t, n.Valid)
			fv, err := n.Float64Value()
			require.NoError(t, err)
			require.True(t, fv.Valid)
			require.InDelta(t, c.want, fv.Float64, 0.0001)
		})
	}
}

// ---------------------------------------------------------------------
// New tests for the two helpers that lost their receiver / grew arity.
// ---------------------------------------------------------------------

// fakeVastDestroyer captures the instanceID DestroyInstance was called
// with and optionally returns an error. Used to drive BestEffortDestroy
// coverage without touching the real Vast.ai client.
type fakeVastDestroyer struct {
	calledID int64
	calls    int
	err      error
}

func (f *fakeVastDestroyer) DestroyInstance(_ context.Context, id int64) error {
	f.calledID = id
	f.calls++
	return f.err
}

// TestBestEffortDestroy_CallsDestroyInstance — happy path: the helper
// forwards instanceID to the VastDestroyer impl and returns without
// signalling failure to the caller (errors are logged + swallowed).
func TestBestEffortDestroy_CallsDestroyInstance(t *testing.T) {
	fake := &fakeVastDestroyer{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	BestEffortDestroy(context.Background(), fake, log, 42)
	require.Equal(t, int64(42), fake.calledID, "fake VastDestroyer must observe instanceID=42")
	require.Equal(t, 1, fake.calls)
}

// TestBestEffortDestroy_ErrorSwallowed — Pitfall 8 swallow contract:
// even when DestroyInstance returns a non-nil error, the helper does
// NOT panic and does NOT propagate. Orphan recovery (Plan 07) reconciles
// the leak later.
func TestBestEffortDestroy_ErrorSwallowed(t *testing.T) {
	fake := &fakeVastDestroyer{err: errors.New("vast 500 boom")}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	require.NotPanics(t, func() {
		BestEffortDestroy(context.Background(), fake, log, 99)
	}, "BestEffortDestroy MUST swallow DestroyInstance errors")
	require.Equal(t, int64(99), fake.calledID)
}

// TestBestEffortDestroy_NoOpOnZeroID — instanceID==0 is the "no row was
// created yet" sentinel; helper must short-circuit without calling
// DestroyInstance.
func TestBestEffortDestroy_NoOpOnZeroID(t *testing.T) {
	fake := &fakeVastDestroyer{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	BestEffortDestroy(context.Background(), fake, log, 0)
	require.Equal(t, 0, fake.calls, "instanceID=0 must short-circuit")
}

// TestBestEffortDestroy_NoOpOnNilClient — defensive: nil VastDestroyer
// (e.g. operator forgot to wire vast client; unit test that does not
// stub it) must not panic.
func TestBestEffortDestroy_NoOpOnNilClient(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	require.NotPanics(t, func() {
		BestEffortDestroy(context.Background(), nil, log, 42)
	})
}

// TestCaptureBreadcrumb_NoOp_WhenNoSentryHub — Sentry is not initialized
// inside `go test` by default; AddBreadcrumb against the default hub is
// documented as a no-op. Defensive guard against a future regression
// where the breadcrumb path dereferences a nil hub.
func TestCaptureBreadcrumb_NoOp_WhenNoSentryHub(t *testing.T) {
	require.NotPanics(t, func() {
		CaptureBreadcrumb("test.event", map[string]any{"k": "v"})
	}, "CaptureBreadcrumb MUST tolerate uninitialized Sentry hub")
}
