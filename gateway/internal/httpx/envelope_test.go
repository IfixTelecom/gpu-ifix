// Package httpx_test (envelope_test.go): WriteOpenAIError shape + TypeForStatus.
package httpx_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/pkg/openai"
)

func TestWriteOpenAIError_Shape(t *testing.T) {
	rec := httptest.NewRecorder()
	httpx.WriteOpenAIError(rec, http.StatusUnauthorized,
		"authentication_error", "no_api_key", "Missing API key.")

	resp := rec.Result()
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type want application/json got %q", ct)
	}

	var env openai.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if env.Error.Message != "Missing API key." {
		t.Errorf("Message mismatch: %q", env.Error.Message)
	}
	if env.Error.Type != "authentication_error" {
		t.Errorf("Type mismatch: %q", env.Error.Type)
	}
	if env.Error.Code != "no_api_key" {
		t.Errorf("Code mismatch: %q", env.Error.Code)
	}
}

func TestWriteOpenAIError_Status401(t *testing.T) {
	rec := httptest.NewRecorder()
	httpx.WriteOpenAIError(rec, http.StatusUnauthorized, "authentication_error", "x", "y")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status want 401 got %d", rec.Code)
	}
}

func TestTypeForStatus(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{401, "authentication_error"},
		{403, "permission_error"},
		{429, "rate_limit_error"},
		{422, "invalid_request_error"},
		{400, "invalid_request_error"},
		{404, "invalid_request_error"},
		{500, "api_error"},
		{502, "api_error"},
	}
	for _, c := range cases {
		if got := httpx.TypeForStatus(c.status); got != c.want {
			t.Errorf("TypeForStatus(%d) = %q, want %q", c.status, got, c.want)
		}
	}
}
