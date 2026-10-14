package httpapi

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/filepreview"
	"github.com/zhongwater123/A-NAS/internal/mediaworker"
)

func (h *productHandler) handleFilePreview(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	// Revalidate authorization on every request, never serve a browser cache
	// after access was revoked or the source file was deleted.
	w.Header().Set("Cache-Control", "private, no-store")
	if h.previews == nil {
		writeError(w, 503, "preview_unavailable", "file preview is unavailable")
		return
	}
	size := 320
	switch r.URL.Query().Get("size") {
	case "", "thumbnail":
	case "preview":
		size = 1600
	default:
		writeError(w, 400, "invalid_request", "unknown preview size")
		return
	}
	result, err := h.previews.Render(r.Context(), session.User, r.PathValue("fileID"), size)
	if err != nil {
		switch {
		case errors.Is(err, filepreview.ErrUnsupported), errors.Is(err, mediaworker.ErrUnreadable):
			writeError(w, 422, "preview_unsupported", "this file cannot be previewed")
		case errors.Is(err, mediaworker.ErrUnavailable), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			writeError(w, 503, "preview_unavailable", "file preview is unavailable")
		default:
			h.writeFileError(w, r, err)
		}
		return
	}
	w.Header().Set("Content-Type", result.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "inline")
	http.ServeContent(w, r, "preview", time.Time{}, bytes.NewReader(result.Data))
}
