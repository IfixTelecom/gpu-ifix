// Unit tests for the Lua token bucket script. Uses miniredis/v2 so the
// tests require no infrastructure — miniredis implements EVAL/EVALSHA in
// Go (via yuin/gopher-lua) and the go-redis v9 client talks to it over a
// real RESP socket.
//
// Integration coverage (1000 concurrent goroutines against a real Redis
// container, SC-5) lives in gateway/internal/integration_test/ per Plan
// 04-08; this file covers the semantic correctness of the script shape.
package quota_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/quota"
)

func newMiniClient(t *testing.T) (*redis.Client, func()) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis run: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cleanup := func() {
		_ = rdb.Close()
		mr.Close()
	}
	return rdb, cleanup
}

func TestTokenBucketAllowsBurstThenDenies(t *testing.T) {
	rdb, cleanup := newMiniClient(t)
	defer cleanup()

	ctx := context.Background()
	const capacity = 10
	rpsRate := float64(capacity) / 1000.0 // full refill over 1s
	const rpmCap = 600
	rpmRate := float64(rpmCap) / 60000.0 // full refill over 60s
	now := time.Now().UnixMilli()

	for i := 0; i < capacity; i++ {
		res, err := quota.CheckBuckets(ctx, rdb, "tenant-1", "chat",
			capacity, rpmCap, rpsRate, rpmRate, 1, now)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if !res.Allowed {
			t.Fatalf("call %d: want allowed, got %+v", i, res)
		}
	}
	// 11th call must be denied by the RPS window.
	res, err := quota.CheckBuckets(ctx, rdb, "tenant-1", "chat",
		capacity, rpmCap, rpsRate, rpmRate, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Allowed {
		t.Fatalf("11th call should be denied, got %+v", res)
	}
	if res.FailedWindow != "rps" {
		t.Errorf("FailedWindow: want rps, got %q", res.FailedWindow)
	}
	if res.ResetRPSms <= 0 {
		t.Errorf("ResetRPSms: want >0, got %d", res.ResetRPSms)
	}
}

func TestTokenBucketRefillsOverTime(t *testing.T) {
	rdb, cleanup := newMiniClient(t)
	defer cleanup()

	ctx := context.Background()
	const capacity = 5
	rpsRate := float64(capacity) / 1000.0
	const rpmCap = 600
	rpmRate := float64(rpmCap) / 60000.0

	now := time.Now().UnixMilli()
	for i := 0; i < capacity; i++ {
		res, err := quota.CheckBuckets(ctx, rdb, "t", "chat",
			capacity, rpmCap, rpsRate, rpmRate, 1, now)
		if err != nil {
			t.Fatalf("drain call %d: %v", i, err)
		}
		if !res.Allowed {
			t.Fatalf("drain call %d: want allowed, got %+v", i, res)
		}
	}

	// Advance virtual clock 1.5s — bucket should be full again.
	future := now + 1500
	res, err := quota.CheckBuckets(ctx, rdb, "t", "chat",
		capacity, rpmCap, rpsRate, rpmRate, 1, future)
	if err != nil {
		t.Fatalf("after refill: %v", err)
	}
	if !res.Allowed {
		t.Fatalf("after 1.5s refill: want allowed, got %+v", res)
	}
}

func TestTokenBucketDiscriminatesRPSvsRPM(t *testing.T) {
	rdb, cleanup := newMiniClient(t)
	defer cleanup()

	ctx := context.Background()
	// RPM bucket very small, RPS bucket very large — RPM should trip first.
	const rpsCap = 1000
	rpsRate := float64(rpsCap) / 1000.0
	const rpmCap = 3
	rpmRate := float64(rpmCap) / 60000.0
	now := time.Now().UnixMilli()

	for i := 0; i < rpmCap; i++ {
		res, err := quota.CheckBuckets(ctx, rdb, "tenant-rpm", "chat",
			rpsCap, rpmCap, rpsRate, rpmRate, 1, now)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if !res.Allowed {
			t.Fatalf("call %d: want allowed, got %+v", i, res)
		}
	}
	// 4th call: RPS still has 997 tokens, but RPM is exhausted.
	res, err := quota.CheckBuckets(ctx, rdb, "tenant-rpm", "chat",
		rpsCap, rpmCap, rpsRate, rpmRate, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Allowed {
		t.Fatalf("want denied by rpm, got %+v", res)
	}
	if res.FailedWindow != "rpm" {
		t.Errorf("FailedWindow: want rpm, got %q", res.FailedWindow)
	}
	if res.ResetRPMms <= 0 {
		t.Errorf("ResetRPMms: want >0, got %d", res.ResetRPMms)
	}
	if res.ResetRPSms != 0 {
		t.Errorf("ResetRPSms on rpm-trip: want 0, got %d", res.ResetRPSms)
	}
}

func TestBucketKeyPrefixShape(t *testing.T) {
	got := quota.BucketKeyPrefix("tenant-xyz", "chat")
	want := "gw:rate:tenant-xyz:chat"
	if got != want {
		t.Errorf("BucketKeyPrefix = %q, want %q", got, want)
	}
}
