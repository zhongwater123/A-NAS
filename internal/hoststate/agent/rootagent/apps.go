package rootagent

import (
	"net/http"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/hoststate/agent/wire"

	"github.com/zhongwater123/A-NAS/internal/appid"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
)

// handleApps serves the Linux side of App Center installs:
// POST /v1/apps/{id}/identity, /prepare and /release.
func (h *handler) handleApps(w http.ResponseWriter, r *http.Request) {
	if h.apps == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_unavailable", "app operation is unavailable")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		httpjson.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	id, operation, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, wire.AppsPathPrefix), "/")
	if !ok || !appid.Valid(id) || strings.Contains(operation, "/") {
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	switch operation {
	case "identity":
		identity, err := h.apps.AppIdentity(r.Context(), id)
		if err != nil {
			h.logger.ErrorContext(r.Context(), "app identity failed", "app", id, "error", err)
			httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_failed", "app operation failed")
			return
		}
		httpjson.Write(w, http.StatusOK, wire.AppIdentityDocument{Username: identity.Username, UID: identity.UID, GID: identity.GID})
	case "prepare":
		var document wire.AppPreparationDocument
		if err := decodeRequest(r, &document); err != nil {
			httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "request is invalid")
			return
		}
		if err := h.apps.PrepareApp(r.Context(), id, document.Folders, document.Shared); err != nil {
			h.logger.ErrorContext(r.Context(), "prepare app failed", "app", id, "error", err)
			httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_failed", "app operation failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case "release":
		if err := h.apps.ReleaseApp(r.Context(), id); err != nil {
			h.logger.ErrorContext(r.Context(), "release app failed", "app", id, "error", err)
			httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_failed", "app operation failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
	}
}
