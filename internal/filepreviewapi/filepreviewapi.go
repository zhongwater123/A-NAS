// Package filepreviewapi serves previews of ordinary files: video frames and
// HEIC/HEIF pictures that browsers cannot show (docs/specs/basic-storage-and-sharing.md).
package filepreviewapi

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/filepreview"
	"github.com/zhongwater123/A-NAS/internal/filesapi"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/mediaworker"
)

// API renders with previews; nil answers preview_unavailable.
type API struct {
	previews *filepreview.Service
	logger   *slog.Logger
}

func New(previews *filepreview.Service, logger *slog.Logger) *API {
	return &API{previews: previews, logger: logger}
}

func (a *API) Routes() []apiroute.Route {
	return []apiroute.Route{
		{Method: http.MethodGet, Pattern: "/api/v1/files/{fileID}/preview", Posture: apiroute.Session, Handler: a.preview},
	}
}

func (a *API) preview(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	// Errors remain non-cacheable. Successful previews can be stored privately,
	// but must revalidate source access before every reuse (including 304).
	w.Header().Set("Cache-Control", "private, no-store")
	if a.previews == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "preview_unavailable", "file preview is unavailable")
		return
	}
	size := 320
	switch r.URL.Query().Get("size") {
	case "", "thumbnail":
	case "preview":
		size = 1600
	default:
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "unknown preview size")
		return
	}
	result, err := a.previews.Render(r.Context(), session.User, r.PathValue("fileID"), size)
	if err != nil {
		switch {
		case errors.Is(err, filepreview.ErrUnsupported), errors.Is(err, mediaworker.ErrUnreadable):
			httpjson.WriteError(w, http.StatusUnprocessableEntity, "preview_unsupported", "this file cannot be previewed")
		case errors.Is(err, mediaworker.ErrUnavailable), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			httpjson.WriteError(w, http.StatusServiceUnavailable, "preview_unavailable", "file preview is unavailable")
		default:
			filesapi.WriteError(w, r, a.logger, err)
		}
		return
	}
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Add("Vary", "Cookie")
	w.Header().Set("ETag", result.ETag)
	w.Header().Set("Content-Type", result.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "inline")
	http.ServeContent(w, r, "preview", time.Time{}, bytes.NewReader(result.Data))
}
