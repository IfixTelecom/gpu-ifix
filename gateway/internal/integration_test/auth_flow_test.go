//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// TestIntegration_02_AuthFlow exercises the end-to-end auth verification
// path: GenerateAPIKey → InsertAPIKey → Verify (cache miss) → Verify
// (cache hit) → Revoke → FlushDB → Verify returns ErrRevokedAPIKey.
func TestIntegration_02_AuthFlow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, rdb := freshSchema(t, ctx)

	q := gen.New(pool)
	tenant, err := q.GetTenantBySlug(ctx, "converseai")
	if err != nil {
		t.Fatal(err)
	}

	// Issue a key via the same code path gatewayctl uses.
	raw, hash, lookupHash, prefix, err := auth.GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	inserted, err := q.InsertAPIKey(ctx, gen.InsertAPIKeyParams{
		TenantID:      tenant.ID,
		KeyHash:       hash,
		KeyLookupHash: lookupHash,
		KeyPrefix:     prefix,
		DataClass:     string(auth.DataClassNormal),
	})
	if err != nil {
		t.Fatal(err)
	}

	v := auth.NewVerifier(pool, rdb, discardLogger(), nil)

	// Valid — cache miss → DB + argon2.
	ac, err := v.Verify(ctx, raw)
	if err != nil {
		t.Fatalf("valid key rejected: %v", err)
	}
	if ac.TenantID != tenant.ID.String() {
		t.Errorf("tenant id got %q want %q", ac.TenantID, tenant.ID.String())
	}
	if ac.DataClass != auth.DataClassNormal {
		t.Errorf("data_class got %q", ac.DataClass)
	}

	// Second call — cache hit → Redis only.
	ac2, err := v.Verify(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	if ac2.TenantID != ac.TenantID {
		t.Errorf("cache returned different tenant: %q vs %q", ac2.TenantID, ac.TenantID)
	}

	// Revoke; invalidate cache so next verify picks up the revoked status.
	if err := q.RevokeAPIKey(ctx, inserted.ID); err != nil {
		t.Fatal(err)
	}
	if err := rdb.FlushDB(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	// After revocation + cache flush, the row no longer satisfies
	// `status = 'active'` in GetActiveKeyByLookupHash → ErrNoRows →
	// ErrInvalidAPIKey. This is D-A4 intentional: revoked keys are
	// indistinguishable from deleted from the caller's perspective.
	_, err = v.Verify(ctx, raw)
	if err != auth.ErrInvalidAPIKey {
		t.Errorf("after revoke got err %v want ErrInvalidAPIKey", err)
	}

	// Wrong key.
	_, err = v.Verify(ctx, "ifix_sk_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != auth.ErrInvalidAPIKey {
		t.Errorf("wrong key got err %v want ErrInvalidAPIKey", err)
	}

	// Malformed key — no DB hit.
	_, err = v.Verify(ctx, "not_ifix_prefix")
	if err != auth.ErrMalformedKey {
		t.Errorf("malformed got err %v want ErrMalformedKey", err)
	}

	// Empty key.
	_, err = v.Verify(ctx, "")
	if err != auth.ErrMissingAPIKey {
		t.Errorf("empty got err %v want ErrMissingAPIKey", err)
	}
}
