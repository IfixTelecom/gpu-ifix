package breaker

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// Subscribe starts the Pub/Sub loop consuming gw:breaker:events.
// Applies each remote event to the local remoteOpen overlay so Execute
// can short-circuit known-dead upstreams even without local probes
// having run (CONTEXT.md D-D1, cross-replica convergence <1s).
//
// Exits on ctx cancel. On channel drop, reconnects with 1s backoff.
// Designed to be invoked once at boot inside its own goroutine
// (`go set.Subscribe(rootCtx)`).
func (s *Set) Subscribe(ctx context.Context) {
	log := s.log.With("subsystem", "subscribe")
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		ps := redisx.SubscribeBreakerEvents(ctx, s.rdb)
		ch := ps.Channel()
		drained := false
		for !drained {
			select {
			case <-ctx.Done():
				_ = ps.Close()
				return
			case msg, ok := <-ch:
				if !ok {
					drained = true
					break
				}
				var ev redisx.BreakerEvent
				if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
					log.Warn("malformed breaker event", "payload", msg.Payload, "err", err)
					continue
				}
				s.applyRemoteEvent(ev)
				log.Debug("applied remote breaker event",
					"upstream", ev.Upstream, "state", ev.State)
			}
		}
		_ = ps.Close()
		log.Warn("pubsub channel closed; reconnecting")
		select {
		case <-ctx.Done():
			return
		case <-time.After(1 * time.Second):
		}
	}
}
