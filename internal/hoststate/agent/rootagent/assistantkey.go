package rootagent

import (
	"net/http"

	"github.com/zhongwater123/A-NAS/internal/assistantkey"
	"github.com/zhongwater123/A-NAS/internal/hoststate/agent/wire"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
)

// handleAssistantKey reads, saves and deletes the administrator's model key of the AI
// assistant (ADR 0024). The Product Service checks the administrator's role; the key is
// never answered back, and no log line carries it.
func (h *handler) handleAssistantKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPut && r.Method != http.MethodDelete {
		w.Header().Set("Allow", "GET, PUT, DELETE")
		httpjson.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if h.assistantKey == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_unavailable", "the assistant is unavailable")
		return
	}
	var (
		state assistantkey.State
		err   error
	)
	switch r.Method {
	case http.MethodGet:
		state, err = h.assistantKey.AssistantKey(r.Context())
	case http.MethodPut:
		var request wire.AssistantKeyRequest
		if err := decodeRequest(r, &request); err != nil || !assistantkey.ValidKey(request.Key) {
			httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "request is invalid")
			return
		}
		state, err = h.assistantKey.SetAssistantKey(r.Context(), request.Key)
		if err == nil {
			h.logger.InfoContext(r.Context(), "assistant key saved")
		}
	default:
		state, err = h.assistantKey.DeleteAssistantKey(r.Context())
		if err == nil {
			h.logger.InfoContext(r.Context(), "assistant key deleted")
		}
	}
	if err != nil {
		h.logger.ErrorContext(r.Context(), "assistant key operation failed", "method", r.Method, "error", err)
		httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_unavailable", "the assistant is unavailable")
		return
	}
	httpjson.Write(w, http.StatusOK, state)
}
