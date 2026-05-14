package alert

// chatwoot.go — the Chatwoot Application API alert channel (critical-tier
// WhatsApp delivery, OBS-05). Mirrors the working campanhas-chatifix
// implementation: a WhatsApp message is sent by creating a conversation
// with an embedded initial message via the agent-side Application API
// (POST /api/v1/accounts/{account_id}/conversations) — NOT the public
// widget API.
//
// Resilience + secret-handling follow gateway/internal/emerg/vast/client.go:
//   - a fixed package-level HTTP timeout const (no env knob);
//   - the API auth token touches an *http.Request in exactly ONE
//     method (setAuthHeader) so code review can grep for the header
//     name and find a single site;
//   - error bodies are read through a bounded io.LimitReader and mapped
//     to a sentinel that never embeds the URL, token, or any header;
//   - the whole POST runs inside a per-service gobreaker so a dead
//     Chatwoot trips fast instead of stalling the alert fan-out.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sony/gobreaker/v2"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

// chatwootHTTPTimeout caps every Chatwoot POST. Fixed package-level
// constant (not env-tunable), same rationale as the vast client's
// httpTimeout: an alert send blocking longer than this is a Chatwoot
// outage the breaker should absorb, not something to wait on.
const chatwootHTTPTimeout = 30 * time.Second

// chatwootBreakerFailures is the consecutive-failure count that trips
// the Chatwoot circuit breaker. Matches breaker.DefaultOptions (D-A3).
const chatwootBreakerFailures = 3

// ChatwootConfig is the subset of config.Config the Chatwoot client
// needs. Passed as its own struct (rather than the whole *config.Config)
// so the client has no dependency on the config package and is trivial
// to construct in tests.
type ChatwootConfig struct {
	APIURL    string // CHATWOOT_API_URL, e.g. https://crm.ifixtelecom.com.br
	APIToken  string // CHATWOOT_API_TOKEN
	AccountID string // CHATWOOT_ONCALL_ACCOUNT_ID
	InboxID   string // CHATWOOT_ONCALL_INBOX_ID
	ContactID string // CHATWOOT_ONCALL_CONTACT_ID
}

// ChatwootClient is the Chatwoot Application API alert channel. Construct
// via NewChatwootClient (production) or NewChatwootClientWithBaseURL
// (tests). Safe for concurrent use — *http.Client and the gobreaker are
// both goroutine-safe.
type ChatwootClient struct {
	apiToken   string
	baseURL    string
	accountID  string
	inboxID    string
	contactID  string
	httpClient *http.Client
	cb         *gobreaker.CircuitBreaker[*http.Response]
}

// compile-time assertion: ChatwootClient implements Channel.
var _ Channel = (*ChatwootClient)(nil)

// NewChatwootClient wires a ChatwootClient against cfg.APIURL with the
// fixed 30s timeout and its own "chatwoot" circuit breaker. The token is
// held in the struct only for request signing — it is never logged.
func NewChatwootClient(cfg ChatwootConfig) *ChatwootClient {
	return &ChatwootClient{
		apiToken:   cfg.APIToken,
		baseURL:    strings.TrimRight(cfg.APIURL, "/"),
		accountID:  cfg.AccountID,
		inboxID:    cfg.InboxID,
		contactID:  cfg.ContactID,
		httpClient: &http.Client{Timeout: chatwootHTTPTimeout},
		cb:         newChatwootBreaker(),
	}
}

// NewChatwootClientWithBaseURL is the test constructor — pass an
// httptest.Server.URL to point the client at a local mock.
func NewChatwootClientWithBaseURL(cfg ChatwootConfig, baseURL string) *ChatwootClient {
	c := NewChatwootClient(cfg)
	c.baseURL = strings.TrimRight(baseURL, "/")
	return c
}

// newChatwootBreaker builds the per-service circuit breaker. One breaker
// per external service (the gobreaker pattern from breaker.go) so a dead
// Chatwoot opens its own breaker without affecting ClickUp or Brevo.
func newChatwootBreaker() *gobreaker.CircuitBreaker[*http.Response] {
	return gobreaker.NewCircuitBreaker[*http.Response](gobreaker.Settings{
		Name: "chatwoot",
		ReadyToTrip: func(c gobreaker.Counts) bool {
			return c.ConsecutiveFailures >= chatwootBreakerFailures
		},
	})
}

// Name implements Channel.
func (c *ChatwootClient) Name() string { return "chatwoot" }

// chatwootConversationReq is the Application API request body for
// "create a conversation with an embedded initial message".
type chatwootConversationReq struct {
	InboxID   string `json:"inbox_id"`
	ContactID string `json:"contact_id"`
	Status    string `json:"status"`
	Message   struct {
		Content string `json:"content"`
	} `json:"message"`
}

// Send implements Channel: POSTs a new conversation carrying the alert
// text to the on-call inbox/contact, inside the circuit breaker. The
// Title and Body are joined into the single message content. Increments
// obs.AlertSendsTotal{chatwoot, ok|err}.
func (c *ChatwootClient) Send(ctx context.Context, msg Message) error {
	var reqBody chatwootConversationReq
	reqBody.InboxID = c.inboxID
	reqBody.ContactID = c.contactID
	reqBody.Status = "open"
	reqBody.Message.Content = msg.Title + "\n" + msg.Body

	payload, err := json.Marshal(reqBody)
	if err != nil {
		obs.AlertSendsTotal.WithLabelValues("chatwoot", "err").Inc()
		return fmt.Errorf("chatwoot: marshal request: %w", err)
	}

	// One breaker per external service: a dead Chatwoot trips this and
	// fails fast instead of stalling the alerter fan-out (T-07-12). The
	// breaker's fn returns an error for a non-2xx status — otherwise
	// gobreaker would treat an HTTP 500 as a "success" (the transport
	// call returned) and never trip. The non-2xx body is drained +
	// closed INSIDE fn so a tripping response does not leak a
	// connection; on success the live *http.Response is returned for
	// the caller to drain.
	resp, err := c.cb.Execute(func() (*http.Response, error) {
		u := fmt.Sprintf("%s/api/v1/accounts/%s/conversations", c.baseURL, c.accountID)
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
		if r.StatusCode < 200 || r.StatusCode >= 300 {
			serr := c.statusError(r) // drains + closes r.Body
			_ = r.Body.Close()
			return nil, serr
		}
		return r, nil
	})
	if err != nil {
		obs.AlertSendsTotal.WithLabelValues("chatwoot", "err").Inc()
		// err is a transport error, a secret-free status sentinel, or
		// gobreaker.ErrOpenState — none carries the URL or token
		// (T-07-11).
		return fmt.Errorf("chatwoot: send failed: %w", err)
	}
	defer resp.Body.Close()

	// Drain the (small) success body to keep the connection reusable.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 16*1024))
	obs.AlertSendsTotal.WithLabelValues("chatwoot", "ok").Inc()
	return nil
}

// setAuthHeader is the ONE place the Chatwoot API auth token touches an
// *http.Request. Keeping the assignment behind a method lets code review
// grep the header name and confirm exactly one site — and confirm the
// token never flows into a log or error string (threat T-07-11).
func (c *ChatwootClient) setAuthHeader(req *http.Request) {
	req.Header.Set("api_access_token", c.apiToken)
}

// statusError reads up to 16 KiB of the error body (bounded, so a
// hostile response cannot balloon the error string — T-07-14) and
// returns a sentinel error containing ONLY the HTTP status. The URL,
// token, and headers are deliberately excluded so the error is safe to
// log and wrap (T-07-11).
func (c *ChatwootClient) statusError(resp *http.Response) error {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 16*1024))
	return fmt.Errorf("chatwoot: unexpected HTTP status %d", resp.StatusCode)
}
