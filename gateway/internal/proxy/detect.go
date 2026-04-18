package proxy

import (
	"net/http"
	"strings"
)

// IsSSEResponse returns true iff the response advertises SSE via
// Content-Type: text/event-stream. Plan 02-05's audit tee wraps only
// SSE bodies to capture streamed responses for audit.
func IsSSEResponse(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	ct := resp.Header.Get("Content-Type")
	return strings.HasPrefix(ct, "text/event-stream")
}
