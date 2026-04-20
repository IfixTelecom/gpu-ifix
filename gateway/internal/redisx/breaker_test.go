package redisx

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestWriteBreakerState_Roundtrip(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()
	if err := WriteBreakerState(ctx, rdb, "local-llm", "open", 123); err != nil {
		t.Fatal(err)
	}
	got := rdb.HGetAll(ctx, "gw:breaker:local-llm").Val()
	if got["state"] != "open" || got["since_unix"] != "123" {
		t.Fatalf("roundtrip: got %+v", got)
	}
}

func TestPublishAndSubscribe_Roundtrip(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ps := SubscribeBreakerEvents(ctx, rdb)
	t.Cleanup(func() { _ = ps.Close() })
	// Wait for subscription to register against miniredis Pub/Sub.
	time.Sleep(50 * time.Millisecond)
	ev := BreakerEvent{Upstream: "local-llm", State: "open", SinceUnix: 42}
	if err := PublishBreakerEvent(ctx, rdb, ev); err != nil {
		t.Fatal(err)
	}
	msg, err := ps.ReceiveMessage(ctx)
	if err != nil {
		t.Fatalf("ReceiveMessage: %v", err)
	}
	var got BreakerEvent
	if err := json.Unmarshal([]byte(msg.Payload), &got); err != nil {
		t.Fatal(err)
	}
	if got != ev {
		t.Fatalf("got %+v, want %+v", got, ev)
	}
}

func TestBreakerEventsChannel_IsExported(t *testing.T) {
	if got := BreakerEventsChannel(); got != "gw:breaker:events" {
		t.Fatalf("BreakerEventsChannel() = %q, want gw:breaker:events", got)
	}
}
