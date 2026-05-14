// Package admin (audit.go): GET /admin/audit handler. Emits the OBS-07
// paginated audit_log state-change feed the observability dashboard polls
// — newest-first FSM/state-change rows (event_kind IS NOT NULL), one
// compact page at a time. The dashboard never touches Postgres directly;
// it polls this endpoint (plus /admin/metrics and /admin/usage). Clones
// the UsageHandler / MetricsHandler shape exactly: query-interface
// isolation, dual constructor, OpenAI error envelope on bad input,
// admin-metric increment on every branch.
//
// The underlying ListAuditStateChanges query selects only audit_log
// *metadata* columns (ts, route, status, latency, event_kind) — never the
// audit_log_content prompts/responses (threat T-07-09).
//
// Query params:
//
//	limit=<int>    optional; default 50, capped at 200 (threat T-07-08 —
//	               a hostile caller cannot request an unbounded result set).
//	offset=<int>   optional; default 0, must be >= 0.
package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgtype"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

// defaultAuditLimit is the page size when the caller omits ?limit.
const defaultAuditLimit = 50

// maxAuditLimit caps the page size so a hostile caller cannot request an
// unbounded result set (threat T-07-08).
const maxAuditLimit = 200

// AuditResponse is the OBS-07 paginated shape the dashboard polls. Items
// are newest-first (the query's ORDER BY ts DESC).
type AuditResponse struct {
	Items  []AuditRow `json:"items"`
	Limit  int        `json:"limit"`
	Offset int        `json:"offset"`
}

// AuditRow is one audit_log state-change row. Nullable Postgres columns
// (upstream, error_code, event_kind) are rendered as JSON null when unset
// via *string.
type AuditRow struct {
	Ts         string  `json:"ts"`
	RequestID  string  `json:"request_id"`
	TenantID   string  `json:"tenant_id"`
	Route      string  `json:"route"`
	Method     string  `json:"method"`
	Upstream   *string `json:"upstream"`
	StatusCode int16   `json:"status_code"`
	LatencyMs  int32   `json:"latency_ms"`
	ErrorCode  *string `json:"error_code"`
	EventKind  *string `json:"event_kind"`
}

// auditQueries isolates the sqlc surface used by the handler. Test
// injection replaces this with a fake without a real pgxpool.
type auditQueries interface {
	ListAuditStateChanges(ctx context.Context, arg gen.ListAuditStateChangesParams) ([]gen.ListAuditStateChangesRow, error)
}

// AuditHandler serves GET /admin/audit.
type AuditHandler struct {
	q   auditQueries
	log *slog.Logger
}

// NewAuditHandler wires queries + logger. Accepts the concrete *gen.Queries.
func NewAuditHandler(q *gen.Queries, log *slog.Logger) *AuditHandler {
	if log == nil {
		log = slog.Default()
	}
	return &AuditHandler{q: q, log: log.With("module", "ADMIN_AUDIT")}
}

// newAuditHandlerWithQueries is the test constructor: accepts any
// auditQueries (fake or real).
func newAuditHandlerWithQueries(q auditQueries, log *slog.Logger) *AuditHandler {
	if log == nil {
		log = slog.Default()
	}
	return &AuditHandler{q: q, log: log.With("module", "ADMIN_AUDIT")}
}

func (h *AuditHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	limit := defaultAuditLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			httpx.WriteOpenAIError(w, http.StatusBadRequest,
				"invalid_request_error", "invalid_query_param",
				"limit must be a positive integer.")
			obs.GatewayAdminRequests.WithLabelValues("/admin/audit", "4xx").Inc()
			return
		}
		// Cap at maxAuditLimit rather than rejecting — a large limit is
		// not hostile, just clamp it (threat T-07-08).
		if n > maxAuditLimit {
			n = maxAuditLimit
		}
		limit = n
	}

	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			httpx.WriteOpenAIError(w, http.StatusBadRequest,
				"invalid_request_error", "invalid_query_param",
				"offset must be a non-negative integer.")
			obs.GatewayAdminRequests.WithLabelValues("/admin/audit", "4xx").Inc()
			return
		}
		offset = n
	}

	rows, err := h.q.ListAuditStateChanges(ctx, gen.ListAuditStateChangesParams{
		Limit:  int32(limit),
		Offset: int32(offset),
	})
	if err != nil {
		h.log.Error("ListAuditStateChanges failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError,
			"api_error", "audit_query_failed", "")
		obs.GatewayAdminRequests.WithLabelValues("/admin/audit", "5xx").Inc()
		return
	}

	resp := AuditResponse{
		Items:  make([]AuditRow, 0, len(rows)),
		Limit:  limit,
		Offset: offset,
	}
	for _, row := range rows {
		resp.Items = append(resp.Items, AuditRow{
			Ts:         row.Ts.Format("2006-01-02T15:04:05Z07:00"),
			RequestID:  row.RequestID.String(),
			TenantID:   row.TenantID.String(),
			Route:      row.Route,
			Method:     row.Method,
			Upstream:   pgTextPtr(row.Upstream),
			StatusCode: row.StatusCode,
			LatencyMs:  row.LatencyMs,
			ErrorCode:  pgTextPtr(row.ErrorCode),
			EventKind:  pgTextPtr(row.EventKind),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
	obs.GatewayAdminRequests.WithLabelValues("/admin/audit", "2xx").Inc()
}

// pgTextPtr converts a nullable Postgres text column into a *string so
// the JSON encoder renders an unset column as null rather than "".
func pgTextPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	v := t.String
	return &v
}
