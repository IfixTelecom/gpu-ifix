package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/pkg/openai"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// wrapWithMiddleware applies the httpx.RequestID middleware so ctx has a
// request id when the director fires.
func wrapWithMiddleware(h http.Handler) http.Handler {
	return httpx.RequestID(h)
}

func TestChatProxy_NonStreamingJSONRoundTrip(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path got %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("Authorization leaked to upstream: %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-API-Key") != "" {
			t.Errorf("X-API-Key leaked: %q", r.Header.Get("X-API-Key"))
		}
		if r.Header.Get("X-Request-ID") == "" {
			t.Errorf("no X-Request-ID to upstream")
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "qwen") {
			t.Errorf("body didn't contain model: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{
			ID: "chatcmpl-x", Object: "chat.completion", Created: 1, Model: "qwen",
			Choices: []openai.ChatCompletionChoice{{
				Index:        0,
				Message:      openai.ChatCompletionMessage{Role: "assistant", Content: "pong"},
				FinishReason: "stop",
			}},
		})
	}))
	defer upstream.Close()

	rp, err := NewChatProxy(upstream.URL, discardLogger())
	if err != nil {
		t.Fatal(err)
	}

	gateway := httptest.NewServer(wrapWithMiddleware(rp))
	defer gateway.Close()

	reqBody := `{"model":"qwen","messages":[{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequest("POST", gateway.URL+"/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer ifix_sk_secret")
	req.Header.Set("X-API-Key", "ifix_sk_secret2")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("got %d want 200", resp.StatusCode)
	}
	var out openai.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.ID != "chatcmpl-x" {
		t.Errorf("round-trip ID got %q", out.ID)
	}
}

func TestChatProxy_SSEStreamingFlushesPerChunk(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(200)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("upstream server doesn't support flush")
		}
		for i := 0; i < 3; i++ {
			_, _ = w.Write([]byte("data: {\"chunk\":" + strconv.Itoa(i) + "}\n\n"))
			flusher.Flush()
			time.Sleep(50 * time.Millisecond)
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer upstream.Close()

	rp, err := NewChatProxy(upstream.URL, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if rp.FlushInterval >= 0 {
		t.Fatalf("FlushInterval=%d, want < 0 (-1)", rp.FlushInterval)
	}
	gateway := httptest.NewServer(wrapWithMiddleware(rp))
	defer gateway.Close()

	req, _ := http.NewRequest("POST", gateway.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"qwen","stream":true}`))
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var chunkTimes []time.Time
	rd := bytes.NewBuffer(nil)
	buf := make([]byte, 128)
	start := time.Now()
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			rd.Write(buf[:n])
			chunkTimes = append(chunkTimes, time.Now())
			if strings.Contains(rd.String(), "[DONE]") {
				break
			}
		}
		if err != nil {
			break
		}
	}
	total := time.Since(start)
	if total > 500*time.Millisecond {
		t.Errorf("stream total duration %v — upstream emits over ~150ms + transit; test budget 500ms", total)
	}
	if len(chunkTimes) < 2 {
		t.Errorf("only observed %d read events — expected streaming to produce at least 2", len(chunkTimes))
	}
	if len(chunkTimes) >= 2 {
		gap := chunkTimes[len(chunkTimes)-1].Sub(chunkTimes[0])
		if gap < 30*time.Millisecond {
			t.Errorf("gap between first and last chunk observation: %v — looks buffered, not streamed", gap)
		}
		t.Logf("SSE stream observation gap: %v (>=30ms expected)", gap)
	}
}

func TestChatProxy_UpstreamUnreachable502Envelope(t *testing.T) {
	// Point proxy at a closed port. Any dial fails immediately.
	rp, err := NewChatProxy("http://127.0.0.1:1", discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(wrapWithMiddleware(rp))
	defer gateway.Close()

	req, _ := http.NewRequest("POST", gateway.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"qwen"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("got %d want 502", resp.StatusCode)
	}
	var env openai.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if env.Error.Code != "upstream_unreachable" {
		t.Errorf("code got %q want upstream_unreachable", env.Error.Code)
	}
	if env.Error.Type != "api_error" {
		t.Errorf("type got %q want api_error", env.Error.Type)
	}
}

func TestChatProxy_ToolCallingPassThrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"tools"`) {
			t.Errorf("tools missing from upstream request: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{
			ID: "x", Object: "chat.completion", Created: 1, Model: "qwen",
			Choices: []openai.ChatCompletionChoice{{
				Index: 0,
				Message: openai.ChatCompletionMessage{
					Role: "assistant",
					ToolCalls: []openai.ToolCall{{
						ID: "call_1", Type: "function",
						Function: openai.ToolCallFunction{Name: "get_weather", Arguments: `{"city":"SP"}`},
					}},
				},
				FinishReason: "tool_calls",
			}},
		})
	}))
	defer upstream.Close()

	rp, _ := NewChatProxy(upstream.URL, discardLogger())
	gateway := httptest.NewServer(wrapWithMiddleware(rp))
	defer gateway.Close()

	req, _ := http.NewRequest("POST", gateway.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"qwen","messages":[{"role":"user","content":"weather?"}],"tools":[{"type":"function","function":{"name":"get_weather","parameters":{}}}],"tool_choice":"auto"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := http.DefaultClient.Do(req)
	defer resp.Body.Close()
	var out openai.ChatCompletionResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if len(out.Choices) == 0 || len(out.Choices[0].Message.ToolCalls) == 0 {
		t.Errorf("no tool_calls in response: %+v", out)
	}
	if out.Choices[0].Message.ToolCalls[0].Function.Name != "get_weather" {
		t.Errorf("tool name got %q", out.Choices[0].Message.ToolCalls[0].Function.Name)
	}
}

// TestChatProxy_Non200Passthrough confirms non-200 upstream responses relay
// byte-for-byte (status + body + Content-Type) and that the proxy does NOT
// synthesize upstream_unreachable for upstream-generated 4xx bodies.
// Codex review [LOW] 02-04.
func TestChatProxy_Non200Passthrough(t *testing.T) {
	upstreamBody := `{"error":{"message":"invalid model","type":"invalid_request_error","code":"model_not_found"}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	rp, err := NewChatProxy(upstream.URL, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(wrapWithMiddleware(rp))
	defer gateway.Close()

	req, _ := http.NewRequest("POST", gateway.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"nope"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status got %d want 400 (upstream status preserved)", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type got %q want application/json...", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != upstreamBody {
		t.Errorf("body round-trip broke: got %q want %q", body, upstreamBody)
	}
	// Proxy must NOT have synthesized upstream_unreachable for upstream 4xx.
	if strings.Contains(string(body), "upstream_unreachable") {
		t.Error("proxy synthesized upstream_unreachable for an upstream 4xx — must pass through")
	}
}

// TestChatProxy_InterceptorHookInvoked exercises the formal
// ProxyResponseInterceptor extension point by passing an interceptor at
// construction time. Codex review [HIGH/MEDIUM] 02-05 decoupling.
func TestChatProxy_InterceptorHookInvoked(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	calls := 0
	hook := recordingInterceptor{calls: &calls}

	rp, err := NewChatProxy(upstream.URL, discardLogger(), hook)
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(wrapWithMiddleware(rp))
	defer gateway.Close()

	req, _ := http.NewRequest("POST", gateway.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"qwen"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if calls != 1 {
		t.Errorf("interceptor called %d times, want exactly 1 on a normal 200 round-trip", calls)
	}
}

func TestProxyConstructors_RejectMalformedURL(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"empty", ""},
		{"no-scheme", "not-a-url"},
		{"scheme-only", "http://"},
	}
	for _, c := range cases {
		t.Run("chat/"+c.name, func(t *testing.T) {
			if rp, err := NewChatProxy(c.url, discardLogger()); err == nil {
				t.Errorf("NewChatProxy(%q) expected error, got rp=%v", c.url, rp)
			}
		})
		t.Run("embeddings/"+c.name, func(t *testing.T) {
			if rp, err := NewEmbeddingsProxy(c.url, discardLogger()); err == nil {
				t.Errorf("NewEmbeddingsProxy(%q) expected error, got rp=%v", c.url, rp)
			}
		})
		t.Run("audio/"+c.name, func(t *testing.T) {
			if rp, err := NewAudioProxy(c.url, discardLogger()); err == nil {
				t.Errorf("NewAudioProxy(%q) expected error, got rp=%v", c.url, rp)
			}
		})
	}
}
