package rootagent

import (
	"net/http"

	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/systemlog"
)

// handleSystemLogs serves GET /v1/system-logs: allowlisted service logs only,
// with the same filters the product API validated (ADR 0020).
func (h *handler) handleSystemLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		httpjson.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if h.systemLogs == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "logs_unavailable", "system logs are unavailable")
		return
	}
	q, err := systemlog.ParseQuery(r.URL.Query())
	if err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "request is invalid")
		return
	}
	page, err := h.systemLogs.ReadLogs(r.Context(), q)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "system log read failed", "error", err)
		httpjson.WriteError(w, http.StatusServiceUnavailable, "logs_unavailable", "system logs are unavailable")
		return
	}
	httpjson.Write(w, http.StatusOK, page)
}
