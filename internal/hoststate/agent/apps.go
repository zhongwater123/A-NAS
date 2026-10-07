package agent

import (
	"context"
	"net/http"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/appid"
)

const appsPathPrefix = "/v1/apps/"

type appIdentityDocument struct {
	Username string `json:"username"`
	UID      int    `json:"uid"`
	GID      int    `json:"gid"`
}

type appPreparationDocument struct {
	Folders []string `json:"folders"`
	Shared  bool     `json:"shared"`
}

// handleApps serves the Linux side of App Center installs:
// POST /v1/apps/{id}/identity, /prepare and /release.
func (h *handler) handleApps(w http.ResponseWriter, r *http.Request) {
	if h.apps == nil {
		writeError(w, http.StatusServiceUnavailable, "operation_unavailable", "app operation is unavailable")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	id, operation, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, appsPathPrefix), "/")
	if !ok || !appid.Valid(id) || strings.Contains(operation, "/") {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	switch operation {
	case "identity":
		identity, err := h.apps.AppIdentity(r.Context(), id)
		if err != nil {
			h.logger.ErrorContext(r.Context(), "app identity failed", "app", id, "error", err)
			writeError(w, http.StatusServiceUnavailable, "operation_failed", "app operation failed")
			return
		}
		writeJSON(w, http.StatusOK, appIdentityDocument{Username: identity.Username, UID: identity.UID, GID: identity.GID})
	case "prepare":
		var document appPreparationDocument
		if err := decodeRequest(r, &document); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "request is invalid")
			return
		}
		if err := h.apps.PrepareApp(r.Context(), id, document.Folders, document.Shared); err != nil {
			h.logger.ErrorContext(r.Context(), "prepare app failed", "app", id, "error", err)
			writeError(w, http.StatusServiceUnavailable, "operation_failed", "app operation failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case "release":
		if err := h.apps.ReleaseApp(r.Context(), id); err != nil {
			h.logger.ErrorContext(r.Context(), "release app failed", "app", id, "error", err)
			writeError(w, http.StatusServiceUnavailable, "operation_failed", "app operation failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	}
}

func (c *Client) AppIdentity(ctx context.Context, id string) (appid.Identity, error) {
	var document appIdentityDocument
	if err := c.doJSON(ctx, c.operationClient, http.MethodPost, appsPathPrefix+id+"/identity", struct{}{}, &document); err != nil {
		return appid.Identity{}, err
	}
	return appid.Identity{Username: document.Username, UID: document.UID, GID: document.GID}, nil
}

func (c *Client) PrepareApp(ctx context.Context, id string, folders []string, shared bool) error {
	return c.doJSON(ctx, c.operationClient, http.MethodPost, appsPathPrefix+id+"/prepare",
		appPreparationDocument{Folders: folders, Shared: shared}, nil)
}

func (c *Client) ReleaseApp(ctx context.Context, id string) error {
	return c.doJSON(ctx, c.operationClient, http.MethodPost, appsPathPrefix+id+"/release", struct{}{}, nil)
}

var _ appid.Host = (*Client)(nil)
