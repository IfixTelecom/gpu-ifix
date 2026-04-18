package proxy

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
)

// ErrUpstreamUnreachable is the sentinel reported in the OpenAI envelope
// when httputil.ReverseProxy's internal dial/roundtrip fails.
var ErrUpstreamUnreachable = errors.New("proxy: upstream unreachable")

// ErrorHandler returns a ReverseProxy ErrorHandler that emits a 502
// with the OpenAI error envelope and logs the cause + request id.
func ErrorHandler(upstreamName string, log *slog.Logger) func(http.ResponseWriter, *http.Request, error) {
	log = log.With("module", "PROXY", "upstream", upstreamName)
	return func(w http.ResponseWriter, r *http.Request, err error) {
		log.ErrorContext(r.Context(), "upstream error",
			"err", err,
			"request_id", httpx.RequestIDFrom(r.Context()),
			"path", r.URL.Path,
			"sentinel", ErrUpstreamUnreachable.Error(),
		)
		httpx.WriteOpenAIError(w, http.StatusBadGateway,
			"api_error", "upstream_unreachable",
			"The upstream inference service is temporarily unreachable.")
	}
}
