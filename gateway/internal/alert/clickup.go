package alert

// clickup.go — the ClickUp v2 API alert channel (a task per critical /
// warning alert, OBS-04). Re-implements the cobrancas-api resilience
// stack (AdaptiveRateLimiter + CircuitBreaker + withRetry) in Go using
// the already-vendored cenkalti/backoff/v5 + sony/gobreaker/v2 — no new
// HTTP-client dependency.
//
// Three resilience layers, outermost to innermost:
//
//  1. backoff.Retry — classifies the result of each attempt. A 4xx
//     EXCEPT 429 is backoff.Permanent (stop immediately — ClickUp
//     personal tokens are static, so a 401 is unrecoverable and
//     retrying it is a credential-guessing storm, Pitfall 6 / T-07-13).
//     A 429 honors X-RateLimit-Reset via backoff.RetryAfter. A 5xx or a
//     network error is plain-retryable.
//  2. gobreaker — one "clickup" circuit breaker. Only 5xx / network
//     failures count toward tripping it (a 4xx is a client-side fault,
//     not a ClickUp-health signal — same IsSuccessful philosophy as
//     breaker.go). A dead ClickUp trips fast (T-07-12).
//  3. adaptiveRateLimiter — reads X-RateLimit-Remaining /
//     X-RateLimit-Reset off every response and sleeps before the next
//     call when the token's window is exhausted.
//
// Secret-handling follows the vast client: the static token touches an
// *http.Request in exactly one method (setAuthHeader), and every error
// is a sentinel mapped from the status code — never the URL or token.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/sony/gobreaker/v2"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

// clickupHTTPTimeout caps every ClickUp POST. Fixed package-level const,
// same rationale as the vast client's httpTimeout.
const clickupHTTPTimeout = 30 * time.Second

// clickupBreakerFailures is the consecutive-(5xx/network)-failure count
// that trips the ClickUp circuit breaker. Matches breaker.DefaultOptions.
const clickupBreakerFailures = 3

// clickupMaxRetries bounds the backoff loop. A static-token 401 stops
// at try 1 via backoff.Permanent; this ceiling only ever applies to the
// 5xx / 429 retryable path.
const clickupMaxRetries = 4

// clickupBaseURL is the production ClickUp API root. Overridable via
// NewClickUpClientWithBaseURL for httptest fixtures.
const clickupBaseURL = "https://api.clickup.com"

// clickupHTTPError is the typed error carrying the HTTP status out of
// the breaker-gated request func. A typed error (not a string match)
// lets both the gobreaker IsSuccessful filter and the backoff
// classifier read resp.StatusCode cleanly. Its Error() text contains
// ONLY the status — never the URL or token (T-07-11).
type clickupHTTPError struct {
	status int
}

func (e *clickupHTTPError) Error() string {
	return "clickup: unexpected HTTP status " + strconv.Itoa(e.status)
}

// ClickUpConfig is the subset of config.Config the ClickUp client needs.
type ClickUpConfig struct {
	APIToken string // CLICKUP_API_TOKEN (static personal token)
	ListID   string // CLICKUP_ALERT_LIST_ID
}

// ClickUpClient is the ClickUp v2 task-creation alert channel. Construct
// via NewClickUpClient (production) or NewClickUpClientWithBaseURL
// (tests). Safe for concurrent use.
type ClickUpClient struct {
	apiToken   string
	listID     string
	baseURL    string
	httpClient *http.Client
	cb         *gobreaker.CircuitBreaker[*http.Response]
	rl         *adaptiveRateLimiter
}

// compile-time assertion: ClickUpClient implements Channel.
var _ Channel = (*ClickUpClient)(nil)

// adaptiveRateLimiter mirrors cobrancas-api's AdaptiveRateLimiter: it
// records the X-RateLimit-Remaining / X-RateLimit-Reset headers from
// each response and, when the token's window is exhausted (remaining
// <= 0), makes the next caller wait until the reset instant. ClickUp
// returns these headers on every response.
type adaptiveRateLimiter struct {
	mu        sync.Mutex
	remaining int
	resetAt   time.Time
	seen      bool // true once the first response's headers were observed
}

// waitIfExhausted blocks until the rate-limit window resets when the
// last observed response reported zero remaining quota. Respects ctx
// cancellation. A no-op until the first response has been observed.
func (a *adaptiveRateLimiter) waitIfExhausted(ctx context.Context) error {
	a.mu.Lock()
	exhausted := a.seen && a.remaining <= 0
	resetAt := a.resetAt
	a.mu.Unlock()
	if !exhausted {
		return nil
	}
	d := time.Until(resetAt)
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// observe records the X-RateLimit-* headers from a response.
func (a *adaptiveRateLimiter) observe(h http.Header) {
	rem := h.Get("X-RateLimit-Remaining")
	reset := h.Get("X-RateLimit-Reset")
	if rem == "" && reset == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seen = true
	if rem != "" {
		if n, err := strconv.Atoi(rem); err == nil {
			a.remaining = n
		}
	}
	if reset != "" {
		if secs, err := strconv.ParseInt(reset, 10, 64); err == nil {
			a.resetAt = time.Unix(secs, 0)
		}
	}
}

// NewClickUpClient wires a ClickUpClient against the production ClickUp
// API root with the fixed 30s timeout, its own "clickup" circuit
// breaker, and an adaptive rate limiter.
func NewClickUpClient(cfg ClickUpConfig) *ClickUpClient {
	return &ClickUpClient{
		apiToken:   cfg.APIToken,
		listID:     cfg.ListID,
		baseURL:    clickupBaseURL,
		httpClient: &http.Client{Timeout: clickupHTTPTimeout},
		cb:         newClickUpBreaker(),
		rl:         &adaptiveRateLimiter{},
	}
}

// NewClickUpClientWithBaseURL is the test constructor — pass an
// httptest.Server.URL to point the client at a local mock.
func NewClickUpClientWithBaseURL(cfg ClickUpConfig, baseURL string) *ClickUpClient {
	c := NewClickUpClient(cfg)
	c.baseURL = strings.TrimRight(baseURL, "/")
	return c
}

// newClickUpBreaker builds the per-service circuit breaker. IsSuccessful
// excludes 4xx from the failure count: a 4xx (incl. 401, 429) is a
// client-side / throttle condition, not a ClickUp-health signal — only
// 5xx + network errors should trip the breaker (same philosophy as
// breaker.IsSuccessful).
func newClickUpBreaker() *gobreaker.CircuitBreaker[*http.Response] {
	return gobreaker.NewCircuitBreaker[*http.Response](gobreaker.Settings{
		Name: "clickup",
		ReadyToTrip: func(c gobreaker.Counts) bool {
			return c.ConsecutiveFailures >= clickupBreakerFailures
		},
		IsSuccessful: func(err error) bool {
			if err == nil {
				return true
			}
			var he *clickupHTTPError
			if errors.As(err, &he) {
				// 4xx (incl. 401 + 429) is NOT a breaker failure.
				return he.status >= 400 && he.status < 500
			}
			// transport / context errors → failure
			return false
		},
	})
}

// Name implements Channel.
func (c *ClickUpClient) Name() string { return "clickup" }

// clickupTaskReq is the ClickUp v2 create-task request body.
type clickupTaskReq struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Send implements Channel: creates a ClickUp task carrying the alert,
// wrapped in backoff.Retry → gobreaker → adaptive rate limiter.
// Increments obs.AlertSendsTotal{clickup, ok|err}.
func (c *ClickUpClient) Send(ctx context.Context, msg Message) error {
	payload, err := json.Marshal(clickupTaskReq{
		Name:        msg.Title,
		Description: msg.Body,
	})
	if err != nil {
		obs.AlertSendsTotal.WithLabelValues("clickup", "err").Inc()
		return fmt.Errorf("clickup: marshal request: %w", err)
	}

	// backoff.Operation: one attempt = rate-limit wait + breaker-gated
	// POST + status classification.
	op := func() (*http.Response, error) {
		if werr := c.rl.waitIfExhausted(ctx); werr != nil {
			// ctx cancelled while waiting on the rate-limit window —
			// permanent, the caller gave up.
			return nil, backoff.Permanent(werr)
		}

		resp, cerr := c.cb.Execute(func() (*http.Response, error) {
			u := fmt.Sprintf("%s/api/v2/list/%s/task", c.baseURL, c.listID)
			req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
			if rerr != nil {
				return nil, rerr
			}
			c.setAuthHeader(req)
			req.Header.Set("Content-Type", "application/json")
			r, derr := c.httpClient.Do(req)
			if derr != nil {
				return nil, derr
			}
			c.rl.observe(r.Header)
			if r.StatusCode < 200 || r.StatusCode >= 300 {
				// Capture the status (and, for 429, the reset header)
				// before draining + closing the body. The typed error
				// carries ONLY the status out (T-07-11).
				status := r.StatusCode
				resetSecs := retryAfterSeconds(r.Header)
				_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 16*1024))
				_ = r.Body.Close()
				if status == http.StatusTooManyRequests {
					return nil, &clickupRateLimitError{resetSecs: resetSecs}
				}
				return nil, &clickupHTTPError{status: status}
			}
			return r, nil
		})
		if cerr != nil {
			return nil, c.classify(cerr)
		}
		return resp, nil
	}

	resp, err := backoff.Retry(ctx, op,
		backoff.WithBackOff(backoff.NewExponentialBackOff()),
		backoff.WithMaxTries(clickupMaxRetries),
	)
	if err != nil {
		obs.AlertSendsTotal.WithLabelValues("clickup", "err").Inc()
		return fmt.Errorf("clickup: send failed: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 16*1024))
	obs.AlertSendsTotal.WithLabelValues("clickup", "ok").Inc()
	return nil
}

// clickupRateLimitError signals a 429 — the backoff classifier turns it
// into backoff.RetryAfter so the next attempt honors X-RateLimit-Reset.
type clickupRateLimitError struct {
	resetSecs int
}

func (e *clickupRateLimitError) Error() string {
	return "clickup: rate limited (429)"
}

// classify maps a breaker-returned error to the backoff verdict
// (Pitfall 6 — the heart of the "401 is not retried" guarantee):
//   - a 4xx EXCEPT 429 → backoff.Permanent (stop immediately);
//   - a 429 → backoff.RetryAfter(reset seconds) (retry, honoring the
//     X-RateLimit-Reset window);
//   - gobreaker.ErrOpenState → permanent (the breaker is open; retrying
//     in-loop would just hammer a known-dead service);
//   - a 5xx or transport error → returned as-is (plain retryable).
func (c *ClickUpClient) classify(err error) error {
	var he *clickupHTTPError
	if errors.As(err, &he) {
		if he.status >= 400 && he.status < 500 {
			// 401 / 403 / 404 / 422 ... — ClickUp tokens are static,
			// so a 4xx is unrecoverable. Stop the retry NOW (T-07-13).
			return backoff.Permanent(err)
		}
		// 5xx wrapped as a typed status error → retryable.
		return err
	}
	var rl *clickupRateLimitError
	if errors.As(err, &rl) {
		secs := rl.resetSecs
		if secs <= 0 {
			secs = 1 // honor a 429 even when the reset header was absent
		}
		return backoff.RetryAfter(secs)
	}
	if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
		// Breaker open — fail fast, do not retry in-loop.
		return backoff.Permanent(err)
	}
	if errors.Is(err, context.Canceled) {
		return backoff.Permanent(err)
	}
	// transport error / 5xx — retryable.
	return err
}

// setAuthHeader is the ONE place the ClickUp static token touches an
// *http.Request. ClickUp uses the RAW token in the auth header — with
// NO prefix scheme (Pattern 3: it is not a bearer-style token). Keeping
// it behind a method lets code review grep the header name and find a
// single site that never flows the token into a log or error (T-07-11).
func (c *ClickUpClient) setAuthHeader(req *http.Request) {
	req.Header.Set("Authorization", c.apiToken)
}

// retryAfterSeconds derives a wait hint from a 429 response. ClickUp
// returns X-RateLimit-Reset as a Unix-seconds instant; we convert it to
// a relative second count. Falls back to a Retry-After header if
// present. Returns 0 when neither header gives a usable value.
func retryAfterSeconds(h http.Header) int {
	if reset := h.Get("X-RateLimit-Reset"); reset != "" {
		if secs, err := strconv.ParseInt(reset, 10, 64); err == nil {
			d := int(time.Until(time.Unix(secs, 0)).Seconds())
			if d > 0 {
				return d
			}
		}
	}
	if ra := h.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
			return secs
		}
	}
	return 0
}
