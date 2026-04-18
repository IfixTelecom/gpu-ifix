package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ifixtelecom/gpu-ifix/pkg/openai"
)

func TestEmbeddingsProxy_RoundTrip(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("path got %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(openai.EmbeddingResponse{
			Object: "list", Model: "BAAI/bge-m3",
			Data: []openai.Embedding{{Object: "embedding", Index: 0, Embedding: []float32{0.1, 0.2, 0.3}}},
		})
	}))
	defer upstream.Close()

	rp, err := NewEmbeddingsProxy(upstream.URL, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(wrapWithMiddleware(rp))
	defer gateway.Close()

	req, _ := http.NewRequest("POST", gateway.URL+"/v1/embeddings",
		strings.NewReader(`{"model":"bge-m3","input":["hello"]}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status got %d want 200", resp.StatusCode)
	}
	var out openai.EmbeddingResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if len(out.Data) != 1 || len(out.Data[0].Embedding) != 3 {
		t.Errorf("round-trip broke embedding array: %+v", out)
	}
}

// TestEmbeddingsProxy_BufferedNotStreamed confirms embeddings use the stdlib
// default FlushInterval (0 = buffered) so the whole response body lands in a
// single client-side Write. Codex review [MEDIUM] 02-04 scope change.
func TestEmbeddingsProxy_BufferedNotStreamed(t *testing.T) {
	// ~2 KB single-Write payload, no split writes, no flush calls.
	payload := `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[` +
		strings.Repeat("0.1,", 200) + `0.1]}],"model":"bge-m3"}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(payload))
	}))
	defer upstream.Close()

	rp, err := NewEmbeddingsProxy(upstream.URL, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	// FlushInterval must NOT be -1 on embeddings.
	if rp.FlushInterval < 0 {
		t.Fatalf("FlushInterval=%d — embeddings must use default (0 = buffered), not per-chunk flush", rp.FlushInterval)
	}
	gateway := httptest.NewServer(wrapWithMiddleware(rp))
	defer gateway.Close()

	req, _ := http.NewRequest("POST", gateway.URL+"/v1/embeddings",
		strings.NewReader(`{"model":"bge-m3","input":["hi"]}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != payload {
		t.Errorf("embeddings body round-trip broke; got %d bytes want %d bytes", len(got), len(payload))
	}
}
