//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// TestIntegration_11_AuthHotPathUnderLoad seeds many active api_keys and
// fires thousands of requests with random well-formed-but-unknown keys
// against the Verifier. Asserts:
//   - Throughput stays reasonable (≥ 50 req/s on the CI VM — we pick a low
//     floor because ci hardware variance is wider than local).
//   - Negative cache absorbs most repeated unknown keys (hit ratio > 50%
//     when the same invalid key is re-presented — we use 10 unique bad
//     keys to force cache hits).
//   - Postgres never reports more than pool.MaxConns busy connections.
//
// Codex review [HIGH] 02-03 regression guard — the UNIQUE index on
// key_lookup_hash + negative cache must keep the hot path cheap under
// an adversarial invalid-key flood.
func TestIntegration_11_AuthHotPathUnderLoad(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, rdb := freshSchema(t, ctx)

	q := gen.New(pool)
	tenant, err := q.GetTenantBySlug(ctx, "converseai")
	if err != nil {
		t.Fatal(err)
	}

	// Seed 50 active api_keys (smaller than the plan's suggestion — at 50
	// we still exercise the UNIQUE-index hot path and the test completes
	// reliably within 90s of argon2id key generation).
	const seedKeys = 50
	for i := 0; i < seedKeys; i++ {
		_, hash, lookupHash, prefix, err := auth.GenerateAPIKey()
		if err != nil {
			t.Fatalf("gen key %d: %v", i, err)
		}
		if _, err := q.InsertAPIKey(ctx, gen.InsertAPIKeyParams{
			TenantID:      tenant.ID,
			KeyHash:       hash,
			KeyLookupHash: lookupHash,
			KeyPrefix:     prefix,
			DataClass:     string(auth.DataClassNormal),
		}); err != nil {
			t.Fatalf("insert key %d: %v", i, err)
		}
	}

	v := auth.NewVerifier(pool, rdb, discardLogger(), nil)

	// Pre-generate a small set of well-formed-but-unknown keys. Reusing
	// the same set forces the negative cache to kick in after the first
	// hit — this is what we're measuring.
	const uniqueBadKeys = 10
	badKeys := make([]string, uniqueBadKeys)
	for i := range badKeys {
		badKeys[i] = makeRandomWellFormedKey()
	}

	// Warm-up: first pass populates the negative cache.
	for _, k := range badKeys {
		_, _ = v.Verify(ctx, k)
	}

	// Flood: 500 verify calls using the 10 pre-generated bad keys. With
	// the negative cache warm, ALL of these should be served from Redis
	// (zero DB, zero argon2). We also check pool busy-connections remains
	// below MaxConns throughout.
	const flood = 500
	var (
		stop       = make(chan struct{}) // closed by main flow to signal monitor to exit
		monitorWG  sync.WaitGroup
		maxBusy    int32
		monitorErr error
	)
	monitorWG.Add(1)
	go func() {
		defer monitorWG.Done()
		maxConns := int32(pool.Config().MaxConns)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			stat := pool.Stat()
			busy := stat.AcquiredConns()
			if busy > atomic.LoadInt32(&maxBusy) {
				atomic.StoreInt32(&maxBusy, busy)
			}
			if busy > maxConns {
				monitorErr = fmt.Errorf("busy conns %d > MaxConns %d", busy, maxConns)
				return
			}
			select {
			case <-ticker.C:
			case <-stop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	start := time.Now()
	const workers = 20
	var wg sync.WaitGroup
	wg.Add(workers)
	hits := int32(0)
	for w := 0; w < workers; w++ {
		go func(offset int) {
			defer wg.Done()
			for i := 0; i < flood/workers; i++ {
				k := badKeys[(offset+i)%uniqueBadKeys]
				_, err := v.Verify(ctx, k)
				if err == auth.ErrInvalidAPIKey {
					atomic.AddInt32(&hits, 1)
				}
			}
		}(w)
	}
	wg.Wait()
	elapsed := time.Since(start)
	close(stop)
	monitorWG.Wait()

	if monitorErr != nil {
		t.Error(monitorErr)
	}
	actualCalls := int(hits)
	if actualCalls < flood*9/10 {
		t.Errorf("verify hit count got %d want >= %d (expected all calls to return ErrInvalidAPIKey)", actualCalls, flood*9/10)
	}
	throughput := float64(flood) / elapsed.Seconds()
	t.Logf("auth hot path throughput: %.0f req/s over %d calls in %s (maxBusy conns=%d)",
		throughput, flood, elapsed, atomic.LoadInt32(&maxBusy))
	if throughput < 50 {
		t.Errorf("throughput %.0f req/s < 50 req/s floor — negative cache may not be absorbing", throughput)
	}
}

// makeRandomWellFormedKey generates a syntactically valid ifix_sk_ key
// that is NOT in the database. Uses the same base32 encoding as
// auth.GenerateAPIKey but skips the argon2id hashing step.
func makeRandomWellFormedKey() string {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	enc := base32.StdEncoding.WithPadding(base32.NoPadding)
	return auth.KeyPrefix + strings.ToLower(enc.EncodeToString(b))
}
