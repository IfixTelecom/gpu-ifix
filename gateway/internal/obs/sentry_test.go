// Unit tests for the Sentry BeforeSend scrub hook (OBS-08). The hook is
// extracted into the package-level beforeSend function so it can be
// exercised without a live Sentry DSN — Init only wires it.
package obs

import (
	"strings"
	"testing"

	sentry "github.com/getsentry/sentry-go"
)

// TestSentryBeforeSend_RedactsRequestBody asserts a request body carrying
// an api key in Request.Data is replaced with the redaction marker — the
// secret string must be absent from the event after BeforeSend (T-07-04).
func TestSentryBeforeSend_RedactsRequestBody(t *testing.T) {
	const secret = "sk-live-DEADBEEF-do-not-leak"
	ev := &sentry.Event{
		Request: &sentry.Request{
			Data: `{"messages":[{"role":"user","content":"hi"}],"api_key":"` + secret + `"}`,
		},
	}

	out := beforeSend(ev, nil)
	if out == nil {
		t.Fatal("beforeSend returned nil — event should still be sent")
	}
	if out.Request.Data != "***REDACTED***" {
		t.Fatalf("Request.Data want %q, got %q", "***REDACTED***", out.Request.Data)
	}
	if strings.Contains(out.Request.Data, secret) {
		t.Fatalf("secret %q leaked into Request.Data", secret)
	}
}

// TestSentryBeforeSend_DropsExtraBodies asserts request_body / response_body
// keys stuffed into event.Extra by any handler are deleted before send.
func TestSentryBeforeSend_DropsExtraBodies(t *testing.T) {
	ev := &sentry.Event{
		Extra: map[string]interface{}{
			"request_body":  `{"prompt":"secret prompt"}`,
			"response_body": `{"completion":"secret completion"}`,
			"keep_me":       "diagnostic-context",
		},
	}

	out := beforeSend(ev, nil)
	if _, ok := out.Extra["request_body"]; ok {
		t.Error("request_body still present in Extra after BeforeSend")
	}
	if _, ok := out.Extra["response_body"]; ok {
		t.Error("response_body still present in Extra after BeforeSend")
	}
	if out.Extra["keep_me"] != "diagnostic-context" {
		t.Errorf("unrelated Extra key was dropped: keep_me=%v", out.Extra["keep_me"])
	}
}

// TestSentryBeforeSend_PreservesHeaderCookieScrub asserts the Phase 2
// behavior is unchanged — sensitive headers still become ***REDACTED***
// and Cookies is still cleared.
func TestSentryBeforeSend_PreservesHeaderCookieScrub(t *testing.T) {
	ev := &sentry.Event{
		Request: &sentry.Request{
			Headers: map[string]string{
				"Authorization": "Bearer sk-live-leak",
				"Content-Type":  "application/json",
			},
			Cookies: "session=abc123",
		},
	}

	out := beforeSend(ev, nil)
	if out.Request.Headers["Authorization"] != "***REDACTED***" {
		t.Fatalf("Authorization header not redacted: %q", out.Request.Headers["Authorization"])
	}
	if out.Request.Headers["Content-Type"] != "application/json" {
		t.Fatalf("non-sensitive header mutated: %q", out.Request.Headers["Content-Type"])
	}
	if out.Request.Cookies != "" {
		t.Fatalf("Cookies not cleared: %q", out.Request.Cookies)
	}
}

// TestSentryBeforeSend_NilSafe asserts the hook does not panic on an event
// with no Request and no Extra (a bare panic event).
func TestSentryBeforeSend_NilSafe(t *testing.T) {
	out := beforeSend(&sentry.Event{}, nil)
	if out == nil {
		t.Fatal("beforeSend returned nil for a bare event")
	}
}
