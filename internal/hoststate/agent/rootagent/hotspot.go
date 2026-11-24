package rootagent

import (
	"net/http"

	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/phonehotspot"
)

func (h *handler) handlePhoneHotspot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		w.Header().Set("Allow", "GET, PUT")
		httpjson.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if h.phoneHotspot == nil {
		httpjson.Write(w, http.StatusOK, phonehotspot.State{State: phonehotspot.Unavailable, Reason: "dependencies_missing"})
		return
	}
	var state phonehotspot.State
	var err error
	if r.Method == http.MethodGet {
		state, err = h.phoneHotspot.PhoneHotspot(r.Context())
	} else {
		var request phonehotspot.Request
		if err := decodeRequest(r, &request); err != nil || request.Enabled == nil {
			httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "request is invalid")
			return
		}
		state, err = h.phoneHotspot.SetPhoneHotspot(r.Context(), *request.Enabled)
	}
	if err != nil {
		h.logger.ErrorContext(r.Context(), "phone hotspot operation failed", "error", err)
		httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_unavailable", "phone hotspot is unavailable")
		return
	}
	httpjson.Write(w, http.StatusOK, state)
}
