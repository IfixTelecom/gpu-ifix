// Package redisx (emerg_test.go): miniredis-backed unit tests for the
// Phase 6 emergency-pod Hash + Pub/Sub helpers + redsync wrapper
// (CONTEXT.md D-B1, D-B2). Mirrors the Phase 3 breaker_test.go layout
// so the test patterns stay consistent across mirror surfaces.
package redisx

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redsync/redsync/v4"
	"github.com/redis/go-redis/v9"
)

// newMiniRedis spins up an in-memory Redis backed by miniredis and
// returns a connected *redis.Client. The cleanup hook closes both.
// Centralised so per-test boilerplate stays one line.
func newMiniRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

func TestEmergStateKey_IsExported(t *testing.T) {
	if got := EmergStateKey(); got != "gw:emerg:state" {
		t.Fatalf("EmergStateKey() = %q, want gw:emerg:state", got)
	}
	if got := EmergLockKey(); got != "gw:emerg:lock" {
		t.Fatalf("EmergLockKey() = %q, want gw:emerg:lock", got)
	}
	if EmergEventsChannel != "gw:emerg:events" {
		t.Fatalf("EmergEventsChannel = %q, want gw:emerg:events", EmergEventsChannel)
	}
}

func TestWriteEmergState_RoundTrip(t *testing.T) {
	rdb := newMiniRedis(t)
	ctx := context.Background()

	enteredUnix := int64(1700000000)
	if err := WriteEmergState(ctx, rdb, "emergency_active", "42", "http://1.2.3.4:9100", "12345", enteredUnix); err != nil {
		t.Fatalf("WriteEmergState: %v", err)
	}

	got := rdb.HGetAll(ctx, EmergStateKey()).Val()
	want := map[string]string{
		"state":           "emergency_active",
		"lifecycle_id":    "42",
		"pod_url":         "http://1.2.3.4:9100",
		"pod_instance_id": "12345",
		"entered_at":      "1700000000",
	}
	if len(got) != len(want) {
		t.Fatalf("HGetAll: got %d fields, want %d. got=%+v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("HGetAll[%q] = %q, want %q", k, got[k], v)
		}
	}
}

func TestWriteEmergState_NilClient(t *testing.T) {
	// Wiring-bug guard: nil client must error fast (not panic).
	err := WriteEmergState(context.Background(), nil, "healthy", "", "", "", 0)
	if err == nil {
		t.Fatalf("WriteEmergState(nil rdb) returned nil err, want non-nil")
	}
}

func TestPublishSubscribeEmergEvent_RoundTrip(t *testing.T) {
	rdb := newMiniRedis(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ps := SubscribeEmergEvents(ctx, rdb)
	t.Cleanup(func() { _ = ps.Close() })
	// Wait for the subscription to register against miniredis Pub/Sub.
	time.Sleep(50 * time.Millisecond)

	ev := EmergEvent{
		Type:        "transition",
		State:       "emergency_provisioning",
		LifecycleID: 42,
		Reason:      "trigger_sustained",
		SinceUnix:   1700000000,
		ReplicaID:   "test-host",
		Payload:     map[string]any{"offer_id": float64(99), "dph": 0.35},
	}
	if err := PublishEmergEvent(ctx, rdb, ev); err != nil {
		t.Fatalf("PublishEmergEvent: %v", err)
	}

	msg, err := ps.ReceiveMessage(ctx)
	if err != nil {
		t.Fatalf("ReceiveMessage: %v", err)
	}
	if msg.Channel != EmergEventsChannel {
		t.Fatalf("msg.Channel = %q, want %q", msg.Channel, EmergEventsChannel)
	}
	var got EmergEvent
	if err := json.Unmarshal([]byte(msg.Payload), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Type != ev.Type || got.State != ev.State || got.LifecycleID != ev.LifecycleID {
		t.Fatalf("event mismatch: got %+v, want %+v", got, ev)
	}
	if got.Reason != ev.Reason || got.SinceUnix != ev.SinceUnix || got.ReplicaID != ev.ReplicaID {
		t.Fatalf("event fields mismatch: got %+v, want %+v", got, ev)
	}
	// Payload is map[string]any; JSON round-trip turns numbers into
	// float64. Compare individual keys.
	if got.Payload["offer_id"].(float64) != 99 {
		t.Fatalf("payload.offer_id = %v, want 99", got.Payload["offer_id"])
	}
}

func TestPublishEmergEvent_NilClient(t *testing.T) {
	err := PublishEmergEvent(context.Background(), nil, EmergEvent{})
	if err == nil {
		t.Fatalf("PublishEmergEvent(nil rdb) returned nil err, want non-nil")
	}
}

func TestNewEmergRedsync_LockUnlock(t *testing.T) {
	rdb := newMiniRedis(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	rs := NewEmergRedsync(rdb)
	if rs == nil {
		t.Fatal("NewEmergRedsync returned nil")
	}

	mtx := rs.NewMutex(EmergLockKey(),
		redsync.WithExpiry(5*time.Second),
		redsync.WithTries(1),
	)
	if err := mtx.LockContext(ctx); err != nil {
		t.Fatalf("LockContext: %v", err)
	}

	// A second mutex on the same key should fail to lock (Tries=1).
	mtx2 := rs.NewMutex(EmergLockKey(),
		redsync.WithExpiry(5*time.Second),
		redsync.WithTries(1),
	)
	if err := mtx2.LockContext(ctx); err == nil {
		t.Fatal("second LockContext succeeded; want failure (lock held)")
	}

	ok, err := mtx.UnlockContext(ctx)
	if err != nil {
		t.Fatalf("UnlockContext: %v", err)
	}
	if !ok {
		t.Fatal("UnlockContext returned ok=false")
	}
}

func TestRedisOpTimeout_Enforced(t *testing.T) {
	// A pre-cancelled context yields context.Canceled or DeadlineExceeded
	// on the first Redis op, never blocks. This protects the FSM hot path
	// from a wedged Redis connection.
	rdb := newMiniRedis(t)
	parent, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := WriteEmergState(parent, rdb, "healthy", "0", "", "", 0)
	if err == nil {
		t.Fatal("WriteEmergState with cancelled ctx returned nil err, want non-nil")
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got err=%v, want context.Canceled or DeadlineExceeded", err)
	}
}
