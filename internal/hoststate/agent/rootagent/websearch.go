package rootagent

import (
	"net/http"

	"github.com/zhongwater123/A-NAS/internal/hoststate/agent/wire"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/websearchswitch"
)

// handleWebSearch reads and sets the administrator's switch of web search
// (ADR 0030). The Product Service checks the administrator's role; the
// switch itself only starts or stops one fixed service.
func (h *handler) handleWebSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		w.Header().Set("Allow", "GET, PUT")
		httpjson.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if h.webSearch == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_unavailable", "web search is unavailable")
		return
	}
	var (
		state websearchswitch.State
		err   error
	)
	if r.Method == http.MethodGet {
		state, err = h.webSearch.WebSearch(r.Context())
	} else {
		var request wire.WebSearchRequest
		if err := decodeRequest(r, &request); err != nil {
			httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "request is invalid")
			return
		}
		state, err = h.webSearch.SetWebSearch(r.Context(), request.Enabled)
		if err == nil {
			h.logger.InfoContext(r.Context(), "web search switched", "enabled", request.Enabled)
		}
	}
	if err != nil {
		h.logger.ErrorContext(r.Context(), "web search switch failed", "error", err)
		httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_unavailable", "web search is unavailable")
		return
	}
	httpjson.Write(w, http.StatusOK, state)
}
