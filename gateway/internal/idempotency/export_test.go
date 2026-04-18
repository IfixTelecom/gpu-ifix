package idempotency

import (
	"context"
	"time"
)

// SetWaitPollForTests lets external _test packages shrink the wait-poll
// budget so in-flight timeout tests run in milliseconds rather than
// waiting the full 30s production window. Returns a restore function.
func SetWaitPollForTests(budget, interval time.Duration) func() {
	origBudget := waitPollBudget
	origInterval := waitPollInterval
	waitPollBudget = budget
	waitPollInterval = interval
	return func() {
		waitPollBudget = origBudget
		waitPollInterval = origInterval
	}
}

// PlantInFlightForTests writes the IN_FLIGHT sentinel directly so tests
// can simulate a winner that never completes without needing a second
// concurrent handler. Only compiled in _test.go consumers.
func (s *Store) PlantInFlightForTests(ctx context.Context, tenantID, idemKey, winnerReqID, requestHash string) error {
	sentinel := inFlightPrefix + winnerReqID + "|hash=" + requestHash
	return s.redis.Set(ctx, keyFor(tenantID, idemKey), sentinel, inFlightTTL).Err()
}
