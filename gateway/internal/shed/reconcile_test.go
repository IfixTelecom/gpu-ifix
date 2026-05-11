// Package shed (reconcile_test.go): unit tests for the periodic
// reconcile loop (RESEARCH Pitfall 3 mitigation #2). Exercises the
// "redis says X, remoteState says Y" divergence detection paths.
//
// Uses newShedSetWithRedis from subscribe_test.go (same package).
package shed

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

func TestReconcileOnce_DetectsDivergence(t *testing.T) {
	ctx := context.Background()
	s, c, _ := newShedSetWithRedis(t)
	s.ApplyRemoteEvent("local-llm", StateOff)
	if err := redisx.WriteShedState(ctx, c, "local-llm", "on", "seed", 1, nil); err != nil {
		t.Fatalf("seed write: %v", err)
	}
	s.reconcileOnce(ctx, c, slog.Default())
	st, _ := s.RemoteState("local-llm")
	if st != StateOn {
		t.Fatalf("reconcile did not correct remoteState; got %s", st)
	}
}

func TestReconcileOnce_NoRedisStateIsOk(t *testing.T) {
	ctx := context.Background()
	s, c, _ := newShedSetWithRedis(t)
	// No state written to Redis — reconcileOnce should treat each upstream as ok.
	s.reconcileOnce(ctx, c, slog.Default())
	// Nothing should panic; remoteState should remain empty for both managed upstreams.
	if _, ok := s.RemoteState("local-llm"); ok {
		t.Fatal("remoteState should NOT be set for upstreams without Redis state")
	}
}

func TestReconcileOnce_AgreedStateStable(t *testing.T) {
	ctx := context.Background()
	s, c, _ := newShedSetWithRedis(t)
	s.ApplyRemoteEvent("local-llm", StateOn)
	_ = redisx.WriteShedState(ctx, c, "local-llm", "on", "x", 1, nil)
	s.reconcileOnce(ctx, c, slog.Default())
	st, _ := s.RemoteState("local-llm")
	if st != StateOn {
		t.Fatalf("agreed state must stay On; got %s", st)
	}
}

func TestReconcileLoop_StopsOnContextCancel(t *testing.T) {
	s, c, _ := newShedSetWithRedis(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.ReconcileLoop(ctx, c, 30*time.Millisecond, slog.Default())
		close(done)
	}()
	// Let one tick run.
	time.Sleep(80 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ReconcileLoop did not stop within 500ms of ctx cancel")
	}
}

func TestReconcileLoop_NilClientReturnsImmediately(t *testing.T) {
	s, _, _ := newShedSetWithRedis(t)
	done := make(chan struct{})
	go func() {
		s.ReconcileLoop(context.Background(), nil, 30*time.Millisecond, slog.Default())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("ReconcileLoop with nil rdb should return immediately")
	}
}
