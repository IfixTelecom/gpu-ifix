// Package httpx (envelope.go): OpenAI-compat error envelope helper.
// Every 4xx/5xx exit path in the gateway goes through WriteOpenAIError so
// the wire format is always the same struct defined in pkg/openai.
package httpx

import (
	"encoding/json"
	"net/http"

	"github.com/ifixtelecom/gpu-ifix/pkg/openai"
)

// WriteOpenAIError emits the standard OpenAI error envelope. Used by
// auth rejection, idempotency conflicts, proxy.ErrorHandler, and any
// handler-local 4xx/5xx path. DO NOT locally redefine openai.ErrorResponse.
func WriteOpenAIError(w http.ResponseWriter, status int, errType, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(openai.ErrorResponse{
		Error: openai.ErrorDetail{Message: msg, Type: errType, Code: code},
	})
}

// TypeForStatus returns the OpenAI `type` field mapped from an HTTP status.
// Used by default paths; callers may override with a more specific type.
func TypeForStatus(status int) string {
	switch {
	case status == http.StatusUnauthorized:
		return "authentication_error"
	case status == http.StatusForbidden:
		return "permission_error"
	case status == http.StatusTooManyRequests:
		return "rate_limit_error"
	case status == http.StatusUnprocessableEntity:
		return "invalid_request_error"
	case status >= 400 && status < 500:
		return "invalid_request_error"
	default:
		return "api_error"
	}
}
