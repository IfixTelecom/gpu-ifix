package alert

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClickUpConfig() ClickUpConfig {
	return ClickUpConfig{
		APIToken: "cu-secret-token-pk_999",
		ListID:   "list-12345",
	}
}

func TestClickUp_Send_Success(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		w.Header().Set("X-RateLimit-Remaining", "99")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"task-1"}`))
	}))
	defer srv.Close()

	c := NewClickUpClientWithBaseURL(testClickUpConfig(), srv.URL)
	err := c.Send(context.Background(), Message{
		Severity: SeverityCritical,
		Title:    "GPU down",
		Body:     "failover active",
	})
	if err != nil {
		t.Fatalf("Send returned error on 200: %v", err)
	}
	if gotPath != "/api/v2/list/list-12345/task" {
		t.Errorf("path = %q, want /api/v2/list/list-12345/task", gotPath)
	}
	// Raw token, NO "Bearer " prefix (Pattern 3).
	if gotAuth != "cu-secret-token-pk_999" {
		t.Errorf("Authorization header = %q, want the raw token (no Bearer prefix)", gotAuth)
	}
	if strings.HasPrefix(gotAuth, "Bearer ") {
		t.Errorf("Authorization header has a Bearer prefix, want raw token: %q", gotAuth)
	}
	if !strings.Contains(gotBody, `"name":"GPU down"`) || !strings.Contains(gotBody, `"description":"failover active"`) {
		t.Errorf("request body missing name/description: %q", gotBody)
	}
}

// TestClickUp_Send_401NotRetried is the Pitfall 6 / T-07-13 guard: a 401
// (a bad or rotated static token) must STOP the retry immediately —
// exactly ONE server hit, no credential-guessing storm — and Send must
// return an error.
func TestClickUp_Send_401NotRetried(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"err":"Token invalid","ECODE":"OAUTH_025"}`))
	}))
	defer srv.Close()

	c := NewClickUpClientWithBaseURL(testClickUpConfig(), srv.URL)
	err := c.Send(context.Background(), Message{Title: "t", Body: "b"})
	if err == nil {
		t.Fatal("Send returned nil on 401, want error")
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("server hit %d times on a 401, want exactly 1 (no retry storm — Pitfall 6)", got)
	}
	// Error must be secret-free.
	msg := err.Error()
	if strings.Contains(msg, "cu-secret-token-pk_999") {
		t.Errorf("error string leaks the API token: %q", msg)
	}
	host := strings.TrimPrefix(srv.URL, "http://")
	if strings.Contains(msg, host) {
		t.Errorf("error string leaks the base URL host %q: %q", host, msg)
	}
}

// TestClickUp_Send_500IsRetried asserts a 5xx response IS retried — the
// server is hit more than once before Send gives up.
func TestClickUp_Send_500IsRetried(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClickUpClientWithBaseURL(testClickUpConfig(), srv.URL)
	err := c.Send(context.Background(), Message{Title: "t", Body: "b"})
	if err == nil {
		t.Fatal("Send returned nil after sustained 500s, want error")
	}
	if got := atomic.LoadInt32(&hits); got < 2 {
		t.Errorf("server hit %d times on 500s, want >1 (5xx must be retried)", got)
	}
}

// TestClickUp_Send_429HonorsRateLimitReset asserts the client honors a
// 429's X-RateLimit-Reset: it retries (more than one hit) and the second
// attempt does not fire before the reset instant.
func TestClickUp_Send_429HonorsRateLimitReset(t *testing.T) {
	var hits int32
	var firstHit, secondHit time.Time
	// reset ~1 second into the future — keeps the test fast while still
	// exercising the wait path.
	resetAt := time.Now().Add(1 * time.Second).Unix()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		switch n {
		case 1:
			firstHit = time.Now()
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetAt, 10))
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			secondHit = time.Now()
			w.Header().Set("X-RateLimit-Remaining", "99")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"task-2"}`))
		}
	}))
	defer srv.Close()

	c := NewClickUpClientWithBaseURL(testClickUpConfig(), srv.URL)
	err := c.Send(context.Background(), Message{Title: "t", Body: "b"})
	if err != nil {
		t.Fatalf("Send returned error after a 429-then-200 sequence: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got < 2 {
		t.Errorf("server hit %d times, want >=2 (429 must be retried)", got)
	}
	// The second attempt must not have fired immediately — it waited on
	// the rate-limit window.
	if waited := secondHit.Sub(firstHit); waited < 500*time.Millisecond {
		t.Errorf("retry fired %v after the 429, want a wait honoring X-RateLimit-Reset", waited)
	}
}
