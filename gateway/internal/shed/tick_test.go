// Package shed (tick_test.go): unit tests for the FSM ticker goroutine.
// Exercises the composite-signal → fsm.Evaluate plumbing, the
// VramUnknown reduction to 1-of-2, the zero-threshold skip, and the
// ctx.Done() shutdown contract.
//
// Tests do NOT use Redis — TickerDeps.Rdb is nil, which disables the
// shed-force override path and exercises the pure-signals → FSM gate.
// shed-force tests live alongside the integration suite in Plan 05-06.
package shed

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeVram struct {
	val     int64
	unknown bool
}

func (f *fakeVram) ReadMiB() (int64, bool) { return f.val, f.unknown }

// thresholdsLLM is a small helper to keep test bodies compact.
func thresholdsLLM(u string) Thresholds {
	return Thresholds{InflightMax: 8, P95Ms: 2000, VramMiB: 21504}
}

func TestRunOneTick_CompositeSignalDrivesFSM(t *testing.T) {
	s := NewSet(nil, slog.Default(), Options{DefaultArmSeconds: 1, DefaultRecoverSeconds: 1})
	s.Rebuild([]string{"local-llm"})
	reg := NewInflightRegistry([]string{"local-llm"})
	ring := NewLatencyRing(200)
	for i := 0; i < 100; i++ {
		ring.Record(5000) // all p95-busting samples
	}
	// Simulate inflight above cap (8).
	tenant := uuid.UUID{}
	for i := 0; i < 10; i++ {
		reg.Inc("local-llm", tenant)
	}
	lat := map[string]*LatencyRing{"local-llm": ring}
	deps := TickerDeps{
		Set:          s,
		Inflight:     reg,
		Latency:      lat,
		VramReader:   &fakeVram{val: 24000, unknown: false},
		ThresholdSrc: thresholdsLLM,
		Interval:     10 * time.Millisecond,
	}
	ctx := context.Background()
	// Tick 1: signals high -> Off→Armed.
	deps.runOneTick(ctx, time.Unix(1000, 0), slog.Default())
	if f, _ := s.Get("local-llm"); f.State() != StateArmed {
		t.Fatalf("tick1: expected armed, got %s", f.State())
	}
	// Tick 2 (2s later, > ArmSeconds=1): Armed→On.
	deps.runOneTick(ctx, time.Unix(1002, 0), slog.Default())
	if f, _ := s.Get("local-llm"); f.State() != StateOn {
		t.Fatalf("tick2: expected on, got %s", f.State())
	}
}

func TestRunOneTick_VramUnknownReducesGateToOneOfTwo(t *testing.T) {
	s := NewSet(nil, slog.Default(), Options{DefaultArmSeconds: 1, DefaultRecoverSeconds: 1})
	s.Rebuild([]string{"local-llm"})
	reg := NewInflightRegistry([]string{"local-llm"})
	ring := NewLatencyRing(200)
	for i := 0; i < 100; i++ {
		ring.Record(5000)
	}
	tenant := uuid.UUID{}
	for i := 0; i < 10; i++ {
		reg.Inc("local-llm", tenant)
	}
	lat := map[string]*LatencyRing{"local-llm": ring}
	deps := TickerDeps{
		Set:          s,
		Inflight:     reg,
		Latency:      lat,
		VramReader:   &fakeVram{unknown: true},
		ThresholdSrc: thresholdsLLM,
	}
	ctx := context.Background()
	deps.runOneTick(ctx, time.Unix(1000, 0), slog.Default())
	// Inflight + P95 = 2 "real" signals -> Off→Armed even with vram unknown.
	if f, _ := s.Get("local-llm"); f.State() != StateArmed {
		t.Fatalf("expected armed with 2 real signals + vram unknown; got %s", f.State())
	}
}

func TestRunOneTick_NoThresholdsIsSkip(t *testing.T) {
	s := NewSet(nil, slog.Default(), Options{})
	s.Rebuild([]string{"tier1"})
	deps := TickerDeps{
		Set:          s,
		ThresholdSrc: func(u string) Thresholds { return Thresholds{} }, // all zeroes
	}
	ctx := context.Background()
	deps.runOneTick(ctx, time.Unix(1000, 0), slog.Default())
	if f, _ := s.Get("tier1"); f.State() != StateOff {
		t.Fatalf("zero thresholds should leave FSM at Off; got %s", f.State())
	}
}

func TestRunOneTick_NilVramReaderIsSafe(t *testing.T) {
	// When DCGM is disabled (VramReader == nil), the ticker MUST still
	// evaluate the FSM using inflight + p95 only.
	s := NewSet(nil, slog.Default(), Options{DefaultArmSeconds: 1, DefaultRecoverSeconds: 1})
	s.Rebuild([]string{"local-llm"})
	reg := NewInflightRegistry([]string{"local-llm"})
	ring := NewLatencyRing(200)
	for i := 0; i < 100; i++ {
		ring.Record(5000)
	}
	tenant := uuid.UUID{}
	for i := 0; i < 10; i++ {
		reg.Inc("local-llm", tenant)
	}
	lat := map[string]*LatencyRing{"local-llm": ring}
	deps := TickerDeps{
		Set:          s,
		Inflight:     reg,
		Latency:      lat,
		VramReader:   nil, // DCGM disabled
		ThresholdSrc: thresholdsLLM,
	}
	ctx := context.Background()
	deps.runOneTick(ctx, time.Unix(1000, 0), slog.Default())
	// VramUnknown=true via nil reader → 2-of-2 over inflight+p95 still triggers Armed.
	if f, _ := s.Get("local-llm"); f.State() != StateArmed {
		t.Fatalf("nil VramReader should not block evaluation; got %s", f.State())
	}
}

func TestRunTicker_CancelStopsQuickly(t *testing.T) {
	s := NewSet(nil, slog.Default(), Options{})
	s.Rebuild([]string{"local-llm"})
	deps := TickerDeps{
		Set:          s,
		ThresholdSrc: func(u string) Thresholds { return Thresholds{} },
		Interval:     50 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { RunTicker(ctx, deps, slog.Default()); close(done) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("RunTicker did not stop within 500ms of ctx cancel")
	}
}

func TestRunTicker_NilSetReturnsImmediately(t *testing.T) {
	done := make(chan struct{})
	go func() {
		RunTicker(context.Background(), TickerDeps{}, slog.Default())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("RunTicker with nil Set should return immediately")
	}
}
