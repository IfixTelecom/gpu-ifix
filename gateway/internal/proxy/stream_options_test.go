// Tests for injectStreamOptionsIncludeUsage (Pitfall 5 — usage chunks
// required for cost attribution on streaming chat responses).
package proxy

import (
	"encoding/json"
	"testing"
)

func TestInjectStreamOptions_NonStreamingUnchanged(t *testing.T) {
	body := []byte(`{"model":"qwen","stream":false,"messages":[]}`)
	out := injectStreamOptionsIncludeUsage(body)
	if string(out) != string(body) {
		t.Fatalf("non-streaming body altered: %s", out)
	}
}

func TestInjectStreamOptions_AbsentStreamUnchanged(t *testing.T) {
	body := []byte(`{"model":"qwen","messages":[]}`)
	out := injectStreamOptionsIncludeUsage(body)
	if string(out) != string(body) {
		t.Fatalf("body without stream field altered: %s", out)
	}
}

func TestInjectStreamOptions_InjectedOnStreamTrue(t *testing.T) {
	body := []byte(`{"model":"qwen","stream":true,"messages":[]}`)
	out := injectStreamOptionsIncludeUsage(body)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	opts, ok := m["stream_options"].(map[string]any)
	if !ok {
		t.Fatalf("stream_options missing or wrong type: %#v", m["stream_options"])
	}
	if inc, ok := opts["include_usage"].(bool); !ok || !inc {
		t.Fatalf("include_usage not true: %#v", opts)
	}
	// Original fields preserved.
	if m["model"] != "qwen" {
		t.Errorf("model preserved? got %#v", m["model"])
	}
}

func TestInjectStreamOptions_RespectsClientOverride(t *testing.T) {
	// Client explicitly said include_usage=false — gateway MUST NOT override.
	body := []byte(`{"model":"qwen","stream":true,"stream_options":{"include_usage":false}}`)
	out := injectStreamOptionsIncludeUsage(body)
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	opts := m["stream_options"].(map[string]any)
	if opts["include_usage"] != false {
		t.Fatalf("client-set include_usage=false was overridden: %#v", opts)
	}
}

func TestInjectStreamOptions_PreservesOtherStreamOptionsFields(t *testing.T) {
	body := []byte(`{"model":"qwen","stream":true,"stream_options":{"foo":"bar"}}`)
	out := injectStreamOptionsIncludeUsage(body)
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	opts := m["stream_options"].(map[string]any)
	if opts["foo"] != "bar" {
		t.Errorf("sibling stream_options field foo dropped: %#v", opts)
	}
	if opts["include_usage"] != true {
		t.Errorf("include_usage not injected alongside existing options: %#v", opts)
	}
}

func TestInjectStreamOptions_MalformedJSONReturnedUnchanged(t *testing.T) {
	body := []byte(`not json`)
	out := injectStreamOptionsIncludeUsage(body)
	if string(out) != string(body) {
		t.Fatalf("malformed body was altered: %s", out)
	}
}

func TestInjectStreamOptions_EmptyBodyUnchanged(t *testing.T) {
	out := injectStreamOptionsIncludeUsage(nil)
	if len(out) != 0 {
		t.Fatalf("empty body altered: %q", out)
	}
}
