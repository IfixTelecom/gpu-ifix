package idempotency_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/idempotency"
)

// --- Test helpers ---

func newMW(t *testing.T) (*idempotency.Store, *miniredis.Miniredis, func(http.Handler) http.Handler) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store := idempotency.NewStore(rdb)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return store, mr, idempotency.Middleware(store, log)
}

// withAuth wraps a handler so the request context carries an AuthContext
// for the given tenant (mimics what auth.Middleware would install).
func withAuth(tenantID string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ac := auth.AuthContext{
			TenantID:  tenantID,
			APIKeyID:  "11111111-1111-1111-1111-111111111111",
			DataClass: auth.DataClassNormal,
			KeyPrefix: "ifix_sk_****test",
		}
		ctx := auth.WithContext(r.Context(), ac)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// fakeAuditWriter implements audit.IdempotencyReplayedSetter so we can
// verify the replay path flips the flag via the interface contract.
type fakeAuditWriter struct {
	http.ResponseWriter
	replayFlag bool
}

func (f *fakeAuditWriter) SetIdempotencyReplayed(v bool) { f.replayFlag = v }

// --- Tests ---

func TestMiddleware_NoHeader_PassesThrough(t *testing.T) {
	_, mr, mw := newMW(t)
	called := 0
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called++
		w.WriteHeader(200)
		_, _ = w.Write([]byte("hello"))
	}))
	srv := httptest.NewServer(withAuth("tenantA", h))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"x":1}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if called != 1 {
		t.Fatalf("handler called %d times, want 1", called)
	}
	if len(mr.Keys()) != 0 {
		t.Fatalf("no header → no Redis touch, but keys = %v", mr.Keys())
	}
}

func TestMiddleware_CacheMissFollowedByHit_SameBody(t *testing.T) {
	_, _, mw := newMW(t)
	var upstreamHits int32
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&upstreamHits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"cc-1","choices":[{"text":"hi"}]}`))
	}))
	srv := httptest.NewServer(withAuth("tenantA", h))
	defer srv.Close()

	body := `{"model":"qwen","messages":[{"role":"user","content":"hi"}]}`

	// First POST.
	req1, _ := http.NewRequest("POST", srv.URL+"/v1/chat/completions", strings.NewReader(body))
	req1.Header.Set("Idempotency-Key", "my-key-1")
	req1.Header.Set("Content-Type", "application/json")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	b1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()
	if resp1.StatusCode != 200 {
		t.Fatalf("first status = %d", resp1.StatusCode)
	}
	if resp1.Header.Get("X-Idempotency-Replayed") != "" {
		t.Fatalf("first must not have replay header")
	}

	// Second POST — same key, same body.
	req2, _ := http.NewRequest("POST", srv.URL+"/v1/chat/completions", strings.NewReader(body))
	req2.Header.Set("Idempotency-Key", "my-key-1")
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Fatalf("second status = %d", resp2.StatusCode)
	}
	if resp2.Header.Get("X-Idempotency-Replayed") != "true" {
		t.Fatalf("second must have X-Idempotency-Replayed: true")
	}
	if !bytes.Equal(b1, b2) {
		t.Fatalf("bodies differ: %q vs %q", b1, b2)
	}
	if atomic.LoadInt32(&upstreamHits) != 1 {
		t.Fatalf("upstream hits = %d, want 1", upstreamHits)
	}
}

func TestMiddleware_SameKeyDifferentBody_422(t *testing.T) {
	_, _, mw := newMW(t)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	srv := httptest.NewServer(withAuth("tenantA", h))
	defer srv.Close()

	// First POST establishes the key.
	req1, _ := http.NewRequest("POST", srv.URL+"/v1/chat/completions", strings.NewReader(`{"a":1}`))
	req1.Header.Set("Idempotency-Key", "my-key-2")
	req1.Header.Set("Content-Type", "application/json")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp1.Body)
	resp1.Body.Close()

	// Second POST — same key, DIFFERENT body.
	req2, _ := http.NewRequest("POST", srv.URL+"/v1/chat/completions", strings.NewReader(`{"a":2}`))
	req2.Header.Set("Idempotency-Key", "my-key-2")
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp2.StatusCode)
	}
	body, _ := io.ReadAll(resp2.Body)
	if !strings.Contains(string(body), "idempotency_key_reused_with_different_body") {
		t.Fatalf("body missing code: %s", body)
	}
}

func TestMiddleware_StreamTrue_400(t *testing.T) {
	_, mr, mw := newMW(t)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatalf("handler should not be called on stream+idem rejection")
	}))
	srv := httptest.NewServer(withAuth("tenantA", h))
	defer srv.Close()

	req, _ := http.NewRequest("POST", srv.URL+"/v1/chat/completions", strings.NewReader(`{"stream":true}`))
	req.Header.Set("Idempotency-Key", "streaming-key")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "idempotency_key_unsupported_stream") {
		t.Fatalf("body missing code: %s", body)
	}
	if len(mr.Keys()) != 0 {
		t.Fatalf("Redis must not be touched on stream rejection, keys = %v", mr.Keys())
	}
}

func TestMiddleware_UnsupportedRoute_400(t *testing.T) {
	_, _, mw := newMW(t)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatalf("handler should not be called on unsupported route")
	}))
	srv := httptest.NewServer(withAuth("tenantA", h))
	defer srv.Close()

	req, _ := http.NewRequest("POST", srv.URL+"/v1/embeddings", strings.NewReader(`{"input":"x"}`))
	req.Header.Set("Idempotency-Key", "e-1")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "idempotency_key_unsupported_route") {
		t.Fatalf("body missing code: %s", body)
	}
}

func TestMiddleware_CrossTenantIsolation(t *testing.T) {
	_, _, mw := newMW(t)
	var upstreamHits int32
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&upstreamHits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	srvA := httptest.NewServer(withAuth("tenantA", h))
	defer srvA.Close()
	srvB := httptest.NewServer(withAuth("tenantB", h))
	defer srvB.Close()

	body := `{"m":1}`

	// Tenant A uses key=x.
	reqA, _ := http.NewRequest("POST", srvA.URL+"/v1/chat/completions", strings.NewReader(body))
	reqA.Header.Set("Idempotency-Key", "x")
	respA, _ := http.DefaultClient.Do(reqA)
	io.Copy(io.Discard, respA.Body)
	respA.Body.Close()

	// Tenant B uses same key=x.
	reqB, _ := http.NewRequest("POST", srvB.URL+"/v1/chat/completions", strings.NewReader(body))
	reqB.Header.Set("Idempotency-Key", "x")
	respB, _ := http.DefaultClient.Do(reqB)
	if respB.Header.Get("X-Idempotency-Replayed") == "true" {
		t.Fatalf("tenant B must NOT replay tenant A's cache")
	}
	io.Copy(io.Discard, respB.Body)
	respB.Body.Close()

	if atomic.LoadInt32(&upstreamHits) != 2 {
		t.Fatalf("upstream hits = %d, want 2 (both tenants should have run)", upstreamHits)
	}
}

func TestMiddleware_Status502_NotCached(t *testing.T) {
	_, _, mw := newMW(t)
	var upstreamHits int32
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&upstreamHits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(502)
		_, _ = w.Write([]byte(`{"error":"upstream_unreachable"}`))
	}))
	srv := httptest.NewServer(withAuth("tenantA", h))
	defer srv.Close()

	doReq := func() *http.Response {
		req, _ := http.NewRequest("POST", srv.URL+"/v1/chat/completions", strings.NewReader(`{"a":1}`))
		req.Header.Set("Idempotency-Key", "err-key")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	r1 := doReq()
	io.Copy(io.Discard, r1.Body)
	r1.Body.Close()
	r2 := doReq()
	if r2.Header.Get("X-Idempotency-Replayed") == "true" {
		t.Fatalf("502 must NOT be cached (replay observed on retry)")
	}
	io.Copy(io.Discard, r2.Body)
	r2.Body.Close()
	if atomic.LoadInt32(&upstreamHits) != 2 {
		t.Fatalf("upstream hits = %d, want 2 (502 retry should re-run)", upstreamHits)
	}
}

func TestMiddleware_AuditFlagSet(t *testing.T) {
	_, _, mw := newMW(t)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	// Wire custom: need to inject the fakeAuditWriter as the outer writer.
	// We do this by writing a server handler that manually constructs the chain.
	chain := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// auth context
		ac := auth.AuthContext{
			TenantID:  "tenantA",
			DataClass: auth.DataClassNormal,
		}
		ctx := auth.WithContext(r.Context(), ac)
		// Wrap w in the fake audit writer before letting idempotency middleware run.
		faw := &fakeAuditWriter{ResponseWriter: w}
		h.ServeHTTP(faw, r.WithContext(ctx))
		// Record flag into response header for test inspection.
		// But h has already written the response; we instead stash on header BEFORE next.
		// Cheat: use httptest directly below rather than via httptest.Server.
		_ = faw
	})

	body := `{"q":1}`

	// First pass: store the entry by calling idempotency middleware directly.
	// We use httptest.NewRecorder to observe the writer.
	rec1 := httptest.NewRecorder()
	faw1 := &fakeAuditWriter{ResponseWriter: rec1}
	req1 := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req1.Header.Set("Idempotency-Key", "aflag-key")
	ctx := auth.WithContext(req1.Context(), auth.AuthContext{TenantID: "tenantA", DataClass: auth.DataClassNormal})
	req1 = req1.WithContext(ctx)
	h.ServeHTTP(faw1, req1)
	if faw1.replayFlag {
		t.Fatalf("first request must NOT set replay flag")
	}

	// Second pass: replay — flag should be true.
	rec2 := httptest.NewRecorder()
	faw2 := &fakeAuditWriter{ResponseWriter: rec2}
	req2 := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req2.Header.Set("Idempotency-Key", "aflag-key")
	req2 = req2.WithContext(ctx)
	h.ServeHTTP(faw2, req2)
	if !faw2.replayFlag {
		t.Fatalf("second (replay) request MUST set replay flag via IdempotencyReplayedSetter")
	}
	if rec2.Header().Get("X-Idempotency-Replayed") != "true" {
		t.Fatalf("replay response must have X-Idempotency-Replayed: true")
	}

	// Silence unused warning.
	_ = chain
}

func TestMiddleware_JsonKeyOrderInvariant(t *testing.T) {
	_, _, mw := newMW(t)
	var upstreamHits int32
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&upstreamHits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	srv := httptest.NewServer(withAuth("tenantA", h))
	defer srv.Close()

	// First POST with one ordering.
	req1, _ := http.NewRequest("POST", srv.URL+"/v1/chat/completions", strings.NewReader(`{"a":1,"b":2}`))
	req1.Header.Set("Idempotency-Key", "order-key")
	req1.Header.Set("Content-Type", "application/json")
	r1, _ := http.DefaultClient.Do(req1)
	io.Copy(io.Discard, r1.Body)
	r1.Body.Close()

	// Second POST with reversed key order — should replay.
	req2, _ := http.NewRequest("POST", srv.URL+"/v1/chat/completions", strings.NewReader(`{"b":2,"a":1}`))
	req2.Header.Set("Idempotency-Key", "order-key")
	req2.Header.Set("Content-Type", "application/json")
	r2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	if r2.Header.Get("X-Idempotency-Replayed") != "true" {
		t.Fatalf("key-reordered body should replay")
	}
	if atomic.LoadInt32(&upstreamHits) != 1 {
		t.Fatalf("upstream hits = %d, want 1 (reorder should match)", upstreamHits)
	}
}

func TestMiddleware_ConcurrentSerialization(t *testing.T) {
	_, _, mw := newMW(t)
	var upstreamHits int32
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&upstreamHits, 1)
		time.Sleep(200 * time.Millisecond) // slow handler to ensure racers serialize
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"winner":true}`))
	}))
	srv := httptest.NewServer(withAuth("tenantA", h))
	defer srv.Close()

	const n = 10
	var wg sync.WaitGroup
	bodies := make([][]byte, n)
	statuses := make([]int, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			req, _ := http.NewRequest("POST", srv.URL+"/v1/chat/completions", strings.NewReader(`{"q":"race"}`))
			req.Header.Set("Idempotency-Key", "race-key")
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				errs[i] = err
				return
			}
			defer resp.Body.Close()
			statuses[i] = resp.StatusCode
			bodies[i], _ = io.ReadAll(resp.Body)
		}(i)
	}
	close(start)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("concurrent requests hung past 5s")
	}

	if atomic.LoadInt32(&upstreamHits) != 1 {
		t.Fatalf("upstream hits = %d, want 1 (real serialization)", upstreamHits)
	}
	// All 10 must get status 200 with identical body.
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("req %d err: %v", i, errs[i])
		}
		if statuses[i] != 200 {
			t.Fatalf("req %d status = %d", i, statuses[i])
		}
		if !bytes.Equal(bodies[i], bodies[0]) {
			t.Fatalf("req %d body differs from req 0: %q vs %q", i, bodies[i], bodies[0])
		}
	}
}

func TestMiddleware_ConcurrentHashMismatch422(t *testing.T) {
	_, _, mw := newMW(t)
	winnerStart := make(chan struct{})
	winnerFinish := make(chan struct{})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bb, _ := io.ReadAll(r.Body)
		if strings.Contains(string(bb), `"a":1`) {
			close(winnerStart)
			<-winnerFinish // hold the winner until test releases it
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	srv := httptest.NewServer(withAuth("tenantA", h))
	defer srv.Close()

	// Goroutine A: body {"a":1}, key=k
	go func() {
		req, _ := http.NewRequest("POST", srv.URL+"/v1/chat/completions", strings.NewReader(`{"a":1}`))
		req.Header.Set("Idempotency-Key", "k")
		req.Header.Set("Content-Type", "application/json")
		resp, _ := http.DefaultClient.Do(req)
		if resp != nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()

	// Wait for A to acquire the sentinel and enter the handler.
	select {
	case <-winnerStart:
	case <-time.After(2 * time.Second):
		t.Fatalf("winner never entered handler")
	}

	// Goroutine B: same key, DIFFERENT body {"a":2} — must 422 IMMEDIATELY.
	reqB, _ := http.NewRequest("POST", srv.URL+"/v1/chat/completions", strings.NewReader(`{"a":2}`))
	reqB.Header.Set("Idempotency-Key", "k")
	reqB.Header.Set("Content-Type", "application/json")

	bStart := time.Now()
	respB, err := http.DefaultClient.Do(reqB)
	bElapsed := time.Since(bStart)
	close(winnerFinish) // release A
	if err != nil {
		t.Fatalf("B err: %v", err)
	}
	defer respB.Body.Close()

	if respB.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("B status = %d, want 422", respB.StatusCode)
	}
	body, _ := io.ReadAll(respB.Body)
	if !strings.Contains(string(body), "idempotency_key_reused_with_different_body") {
		t.Fatalf("B body missing conflict code: %s", body)
	}
	if bElapsed > 500*time.Millisecond {
		t.Fatalf("B should be immediate (< 500ms), took %v", bElapsed)
	}
}

func TestMiddleware_InFlightTimeoutReturns409(t *testing.T) {
	store, _, mw := newMW(t)
	// Shrink the wait-poll budget for the test.
	restore := idempotency.SetWaitPollForTests(300*time.Millisecond, 50*time.Millisecond)
	defer restore()

	// We need the hash of the caller's body to plant a matching sentinel.
	callerBody := `{"x":1}`
	h, err := idempotency.HashBody([]byte(callerBody))
	if err != nil {
		t.Fatal(err)
	}
	// Plant an IN_FLIGHT sentinel with a matching hash and no winner that
	// ever completes.
	if err := store.PlantInFlightForTests(context.Background(), "tenantA", "timeout-key", "req-planted", h); err != nil {
		t.Fatal(err)
	}

	// Handler should never be called — middleware observes in-flight, waits, times out.
	handlerCalled := false
	hh := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalled = true
		w.WriteHeader(200)
	}))
	srv := httptest.NewServer(withAuth("tenantA", hh))
	defer srv.Close()

	req, _ := http.NewRequest("POST", srv.URL+"/v1/chat/completions", strings.NewReader(callerBody))
	req.Header.Set("Idempotency-Key", "timeout-key")
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	elapsed := time.Since(start)

	if handlerCalled {
		t.Fatalf("handler must NOT be called when another request is in-flight")
	}
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") != "5" {
		t.Fatalf("Retry-After = %q, want 5", resp.Header.Get("Retry-After"))
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "idempotency_key_in_progress") {
		t.Fatalf("body missing code: %s", body)
	}
	// Should wait ~300ms but not much more.
	if elapsed < 250*time.Millisecond {
		t.Fatalf("returned too early (%v)", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("returned too late (%v)", elapsed)
	}
}

func TestMiddleware_AbortOnUpstream502(t *testing.T) {
	store, mr, mw := newMW(t)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(502)
		_, _ = w.Write([]byte(`{"error":"upstream_unreachable"}`))
	}))
	srv := httptest.NewServer(withAuth("tenantA", h))
	defer srv.Close()

	req, _ := http.NewRequest("POST", srv.URL+"/v1/chat/completions", strings.NewReader(`{"x":1}`))
	req.Header.Set("Idempotency-Key", "abort-key")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	// After the request, the key in Redis must be GONE (Abort was called).
	if _, err := mr.Get("gw:idem:tenantA:abort-key"); err == nil {
		t.Fatalf("expected sentinel deleted after 502, but key still present")
	}

	// Sanity: Store.Get confirms empty too.
	slot, err := store.Get(req.Context(), "tenantA", "abort-key")
	if err != nil {
		t.Fatal(err)
	}
	if slot.Kind != idempotency.SlotEmpty {
		t.Fatalf("expected SlotEmpty, got %v", slot.Kind)
	}
}
