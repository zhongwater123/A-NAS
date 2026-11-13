package rootagent

import (
	"errors"
	"net/http"

	"github.com/zhongwater123/A-NAS/internal/hoststate/agent/wire"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/systemupdate"
)

// handleSystemUpdate reads the system update service's state, asks it for an
// operation, and sets the administrator's settings
// (docs/specs/system-update.md). The Product Service checks the
// administrator's role; the service runs one fixed unit.
func (h *handler) handleSystemUpdate(w http.ResponseWriter, r *http.Request) {
	settings := r.URL.Path == wire.SystemUpdateSettingsPath
	allowed := (!settings && (r.Method == http.MethodGet || r.Method == http.MethodPost)) ||
		(settings && r.Method == http.MethodPut)
	if !allowed {
		if settings {
			w.Header().Set("Allow", "PUT")
		} else {
			w.Header().Set("Allow", "GET, POST")
		}
		httpjson.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if h.systemUpdate == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_unavailable", "system update is unavailable")
		return
	}
	var (
		status systemupdate.Status
		err    error
	)
	switch {
	case r.Method == http.MethodGet:
		status, err = h.systemUpdate.SystemUpdate(r.Context())
	case settings:
		var request systemupdate.Settings
		if decodeRequest(r, &request) != nil || request.Validate() != nil {
			httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "request is invalid")
			return
		}
		status, err = h.systemUpdate.SetSystemUpdateSettings(r.Context(), request)
		if err == nil {
			h.logger.InfoContext(r.Context(), "system update settings changed", "autoDownload", request.AutoDownload,
				"autoInstall", request.AutoInstall, "window", request.WindowStart+"-"+request.WindowEnd)
		}
	default:
		var request systemupdate.Request
		if decodeRequest(r, &request) != nil || !systemupdate.Requestable(request.Kind) || request.By == "" {
			httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "request is invalid")
			return
		}
		status, err = h.systemUpdate.RequestSystemUpdate(r.Context(), request)
		if errors.Is(err, systemupdate.ErrBusy) {
			httpjson.WriteError(w, http.StatusConflict, wire.UpdateBusyCode, "a system update operation is running")
			return
		}
		if err == nil {
			h.logger.InfoContext(r.Context(), "system update requested", "kind", request.Kind, "by", request.By)
		}
	}
	if err != nil {
		h.logger.ErrorContext(r.Context(), "system update failed", "error", err)
		httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_unavailable", "system update is unavailable")
		return
	}
	httpjson.Write(w, http.StatusOK, status)
}
