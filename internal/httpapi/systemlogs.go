package httpapi

import (
	"net/http"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/systemlog"
)

type systemLogResponse struct {
	systemlog.Page
	DataSource DataSource `json:"dataSource"`
}

// handleSystemLogs serves the allowlisted service logs to administrators. The
// filters are validated here and again by the Host Agent (ADR 0020).
func (h *productHandler) handleSystemLogs(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if !h.requireAdministrator(w, session) {
		return
	}
	if h.systemLogs == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "logs_unavailable", "system logs are unavailable")
		return
	}
	q, err := systemlog.ParseQuery(r.URL.Query())
	if err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_query", "system log filters are invalid")
		return
	}
	page, err := h.systemLogs.ReadLogs(r.Context(), q)
	if err != nil {
		h.logger.WarnContext(r.Context(), "system logs unavailable", "error", err)
		httpjson.WriteError(w, http.StatusServiceUnavailable, "logs_unavailable", "system logs cannot be read right now")
		return
	}
	httpjson.Write(w, http.StatusOK, systemLogResponse{Page: page, DataSource: h.dataSource})
}
