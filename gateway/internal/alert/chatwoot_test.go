package alert

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sony/gobreaker/v2"
)

// testChatwootConfig is a fixed config with a recognizable token + a
// recognizable host so the secret-free-error assertions have concrete
// strings to grep the error message against.
func testChatwootConfig() ChatwootConfig {
	return ChatwootConfig{
		APIToken:  "cw-secret-token-abc123",
		AccountID: "7",
		InboxID:   "42",
		ContactID: "99",
	}
}

func TestChatwoot_Send_Success(t *testing.T) {
	var gotPath, gotToken, gotContentType, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.Header.Get("api_access_token")
		gotContentType = r.Header.Get("Content-Type")
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":123}`))
	}))
	defer srv.Close()

	c := NewChatwootClientWithBaseURL(testChatwootConfig(), srv.URL)
	err := c.Send(context.Background(), Message{
		Severity:    SeverityCritical,
		Title:       "GPU down",
		Body:        "primary unreachable 45s",
		Fingerprint: "emerg:failed_over",
	})
	if err != nil {
		t.Fatalf("Send returned error on 200: %v", err)
	}
	if gotPath != "/api/v1/accounts/7/conversations" {
		t.Errorf("path = %q, want /api/v1/accounts/7/conversations", gotPath)
	}
	if gotToken != "cw-secret-token-abc123" {
		t.Errorf("api_access_token header = %q, want the token", gotToken)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	// Title + Body must be joined into message.content.
	if !strings.Contains(gotBody, "GPU down") || !strings.Contains(gotBody, "primary unreachable 45s") {
		t.Errorf("request body missing title/body: %q", gotBody)
	}
	if !strings.Contains(gotBody, `"status":"open"`) {
		t.Errorf("request body missing status=open: %q", gotBody)
	}
}

// TestChatwoot_Send_ErrorIsSecretFree asserts that a 5xx response
// produces an error whose string contains NEITHER the API token NOR the
// base URL host (threat T-07-11).
func TestChatwoot_Send_ErrorIsSecretFree(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A hostile/large error body — statusError must bound the read
		// and must not reflect it into the error string.
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(strings.Repeat("x", 64*1024)))
	}))
	defer srv.Close()

	c := NewChatwootClientWithBaseURL(testChatwootConfig(), srv.URL)
	err := c.Send(context.Background(), Message{Title: "t", Body: "b"})
	if err == nil {
		t.Fatal("Send returned nil on 500, want error")
	}
	msg := err.Error()
	if strings.Contains(msg, "cw-secret-token-abc123") {
		t.Errorf("error string leaks the API token: %q", msg)
	}
	// srv.URL is like http://127.0.0.1:PORT — assert the host:port does
	// not appear in the error.
	host := strings.TrimPrefix(srv.URL, "http://")
	if strings.Contains(msg, host) {
		t.Errorf("error string leaks the base URL host %q: %q", host, msg)
	}
	if !strings.Contains(msg, "500") {
		t.Errorf("error string should mention the HTTP status: %q", msg)
	}
}

// TestChatwoot_Send_BreakerOpens asserts the circuit breaker opens after
// the configured consecutive-failure count, after which Send fails fast
// with gobreaker.ErrOpenState instead of hitting the server.
func TestChatwoot_Send_BreakerOpens(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewChatwootClientWithBaseURL(testChatwootConfig(), srv.URL)
	msg := Message{Title: "t", Body: "b"}

	// chatwootBreakerFailures consecutive failures trip the breaker.
	for i := 0; i < chatwootBreakerFailures; i++ {
		if err := c.Send(context.Background(), msg); err == nil {
			t.Fatalf("send %d: want error, got nil", i)
		}
	}
	hitsBeforeOpen := hits

	// The next send must short-circuit: the breaker is open, so the
	// server is NOT hit again and the error is ErrOpenState.
	err := c.Send(context.Background(), msg)
	if err == nil {
		t.Fatal("send after breaker trip: want error, got nil")
	}
	if !errors.Is(err, gobreaker.ErrOpenState) {
		t.Errorf("error after breaker trip = %v, want gobreaker.ErrOpenState", err)
	}
	if hits != hitsBeforeOpen {
		t.Errorf("server was hit %d more times after breaker opened (want 0)", hits-hitsBeforeOpen)
	}
}
