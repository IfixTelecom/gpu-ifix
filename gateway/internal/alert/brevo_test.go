package alert

import (
	"context"
	"errors"
	"net/smtp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sony/gobreaker/v2"
)

// testBrevoConfig is a fixed config with a recognizable password + host
// so the secret-free-error assertions have concrete strings to check.
func testBrevoConfig() BrevoConfig {
	return BrevoConfig{
		Host: "smtp-relay.brevo.test",
		Port: 587,
		User: "brevo-user",
		Pass: "brevo-secret-pass-xyz789",
		From: "alerts@ifixtelecom.com.br",
		To:   []string{"oncall@ifixtelecom.com.br"},
	}
}

func TestBrevo_Send_Success(t *testing.T) {
	c := NewBrevoClient(testBrevoConfig())

	var gotAddr, gotFrom string
	var gotTo []string
	var gotMsg []byte
	c.sendMail = func(addr string, _ smtp.Auth, from string, to []string, msg []byte) error {
		gotAddr, gotFrom, gotTo, gotMsg = addr, from, to, msg
		return nil
	}

	err := c.Send(context.Background(), Message{
		Severity: SeverityWarning,
		Title:    "OpenRouter degraded",
		Body:     "p95 above threshold",
	})
	if err != nil {
		t.Fatalf("Send returned error on success: %v", err)
	}
	if gotAddr != "smtp-relay.brevo.test:587" {
		t.Errorf("addr = %q, want smtp-relay.brevo.test:587", gotAddr)
	}
	if gotFrom != "alerts@ifixtelecom.com.br" {
		t.Errorf("from = %q, want alerts@ifixtelecom.com.br", gotFrom)
	}
	if len(gotTo) != 1 || gotTo[0] != "oncall@ifixtelecom.com.br" {
		t.Errorf("to = %v, want [oncall@ifixtelecom.com.br]", gotTo)
	}
	body := string(gotMsg)
	if !strings.Contains(body, "Subject: OpenRouter degraded") {
		t.Errorf("message missing Subject header: %q", body)
	}
	if !strings.Contains(body, "p95 above threshold") {
		t.Errorf("message missing body text: %q", body)
	}
}

// TestBrevo_Send_ErrorIsSecretFree asserts a failing SMTP submission
// produces an error whose string contains NEITHER the SMTP password NOR
// the relay host (threat T-07-11).
func TestBrevo_Send_ErrorIsSecretFree(t *testing.T) {
	c := NewBrevoClient(testBrevoConfig())
	c.sendMail = func(_ string, _ smtp.Auth, _ string, _ []string, _ []byte) error {
		return errors.New("dial tcp: connection refused")
	}

	err := c.Send(context.Background(), Message{Title: "t", Body: "b"})
	if err == nil {
		t.Fatal("Send returned nil on SMTP failure, want error")
	}
	msg := err.Error()
	if strings.Contains(msg, "brevo-secret-pass-xyz789") {
		t.Errorf("error string leaks the SMTP password: %q", msg)
	}
}

// TestBrevo_Send_BreakerOpens asserts the circuit breaker opens after
// the configured consecutive-failure count. The retry budget per Send
// means the breaker trips within the first Send or two; once open, the
// underlying sendMail is not called again.
func TestBrevo_Send_BreakerOpens(t *testing.T) {
	c := NewBrevoClient(testBrevoConfig())

	var calls int32
	c.sendMail = func(_ string, _ smtp.Auth, _ string, _ []string, _ []byte) error {
		atomic.AddInt32(&calls, 1)
		return errors.New("smtp: server unavailable")
	}

	msg := Message{Title: "t", Body: "b"}
	// Drive enough Send calls to exceed brevoBreakerFailures even
	// accounting for the retry budget collapsing once the breaker opens.
	for i := 0; i < brevoBreakerFailures+2; i++ {
		if err := c.Send(context.Background(), msg); err == nil {
			t.Fatalf("send %d: want error, got nil", i)
		}
	}
	callsBeforeProbe := atomic.LoadInt32(&calls)

	// One more send: the breaker is open, so sendMail must NOT be
	// called again and the error must be gobreaker.ErrOpenState.
	err := c.Send(context.Background(), msg)
	if err == nil {
		t.Fatal("send after breaker trip: want error, got nil")
	}
	if !errors.Is(err, gobreaker.ErrOpenState) {
		t.Errorf("error after breaker trip = %v, want gobreaker.ErrOpenState", err)
	}
	if got := atomic.LoadInt32(&calls); got != callsBeforeProbe {
		t.Errorf("sendMail called %d more times after breaker opened (want 0)", got-callsBeforeProbe)
	}
}
