package models

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
)

// RewriteJSONModel attempts to replace the "model" field in a JSON body
// with the resolved target. Returns the (possibly unchanged) body bytes,
// a bool indicating whether a "model" field was found, and any parse
// error. Callers that pass an unreadable body receive (body, false, err).
//
// Design choice: we unmarshal into map[string]json.RawMessage so all
// other fields pass through byte-for-byte. Only the model key is
// touched. Ordering is inevitably Go-map-nondeterministic on re-encode,
// but the OpenAI API is order-independent so downstream clients don't
// notice.
func RewriteJSONModel(body []byte, resolver *Resolver, upstream string) ([]byte, bool, error) {
	if len(body) == 0 {
		return body, false, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return body, false, err
	}
	raw, ok := m["model"]
	if !ok {
		return body, false, nil
	}
	var alias string
	if err := json.Unmarshal(raw, &alias); err != nil {
		return body, false, err
	}
	target := resolver.Resolve(alias, upstream)
	if target == alias {
		// No rewrite needed — return the original body bytes so tests can
		// assert byte-equality for unknown aliases.
		return body, true, nil
	}
	newModel, err := json.Marshal(target)
	if err != nil {
		return body, true, err
	}
	m["model"] = newModel
	out, err := json.Marshal(m)
	if err != nil {
		return body, true, err
	}
	return out, true, nil
}

// Handler wraps an inner handler so that the incoming request body is
// read, JSON-rewritten for the model alias, and passed forward with a
// fresh body reader. Intended for /v1/chat/completions and
// /v1/embeddings. Audio (multipart) route skips this; aliasing for
// Whisper happens pod-side.
func Handler(resolver *Resolver, upstream string, inner http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil {
			inner.ServeHTTP(w, r)
			return
		}
		// Read up to the body cap enforced at server level (25 MiB).
		body, err := io.ReadAll(r.Body)
		if err != nil {
			inner.ServeHTTP(w, r) // best-effort; let proxy error surface
			return
		}
		rewritten, _, _ := RewriteJSONModel(body, resolver, upstream)
		r.Body = io.NopCloser(bytes.NewReader(rewritten))
		r.ContentLength = int64(len(rewritten))
		r.Header.Set("Content-Length", strconv.Itoa(len(rewritten)))
		inner.ServeHTTP(w, r)
	})
}
