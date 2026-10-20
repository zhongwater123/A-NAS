package httpapi

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/personalization"
)

// The wallpaper is the same for every account: anyone signed in reads it,
// only administrators change it.
func (h *productHandler) wallpaperRoutes() {
	h.mux.HandleFunc("GET /api/v1/wallpaper", h.withSession(h.handleWallpaper))
	h.mux.HandleFunc("PATCH /api/v1/wallpaper", h.withMutation(h.handleUpdateWallpaper))
	h.mux.HandleFunc("GET /api/v1/wallpaper/image", h.withSession(h.handleWallpaperImage))
	h.mux.HandleFunc("PUT /api/v1/wallpaper/image", h.withMutation(h.handleSetWallpaperImage))
	h.mux.HandleFunc("DELETE /api/v1/wallpaper/image", h.withMutation(h.handleResetWallpaper))
}

func (h *productHandler) personalizationReady(w http.ResponseWriter) bool {
	if h.personalization == nil {
		writeError(w, http.StatusServiceUnavailable, "personalization_unavailable", "personalization settings are unavailable")
		return false
	}
	return true
}

func (h *productHandler) handleWallpaper(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	if !h.personalizationReady(w) {
		return
	}
	wallpaper, err := h.personalization.Wallpaper(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, wallpaper)
}

func (h *productHandler) handleUpdateWallpaper(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if !h.requireAdministrator(w, session) || !h.personalizationReady(w) {
		return
	}
	var request struct {
		Style *string `json:"style"`
	}
	if err := decodeJSON(r, &request); err != nil || request.Style == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	wallpaper, err := h.personalization.SetWallpaperStyle(r.Context(), *request.Style)
	if errors.Is(err, personalization.ErrInvalidStyle) {
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", err.Error())
		return
	}
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, wallpaper)
}

// handleSetWallpaperImage takes the picture as the raw request body. The page
// scales it first, but the stored wallpaper is always made here.
func (h *productHandler) handleSetWallpaperImage(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if !h.requireAdministrator(w, session) || !h.personalizationReady(w) {
		return
	}
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if !strings.HasPrefix(mediaType, "image/") {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_image", "send a JPEG, PNG, GIF or WebP picture")
		return
	}
	picture, err := io.ReadAll(http.MaxBytesReader(w, r.Body, personalization.MaxWallpaperUploadBytes))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "image_too_large", personalization.ErrImageTooLarge.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "the picture could not be read")
		return
	}
	select {
	case pictureGate <- struct{}{}:
	case <-r.Context().Done():
		return
	}
	data, width, height, err := personalization.NormalizeWallpaper(picture)
	<-pictureGate
	switch {
	case errors.Is(err, personalization.ErrImageTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "image_too_large", err.Error())
		return
	case errors.Is(err, personalization.ErrUnsupportedImage):
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_image", personalization.ErrUnsupportedImage.Error())
		return
	case err != nil:
		h.internalError(w, r, err)
		return
	}
	wallpaper, err := h.personalization.SetWallpaperImage(r.Context(), data, width, height)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, wallpaper)
}

func (h *productHandler) handleResetWallpaper(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if !h.requireAdministrator(w, session) || !h.personalizationReady(w) {
		return
	}
	wallpaper, err := h.personalization.ResetWallpaper(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, wallpaper)
}

// handleWallpaperImage serves the picture with ?v=<version>; a new picture has
// a new version, so browsers keep that URL for good.
func (h *productHandler) handleWallpaperImage(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	if !h.personalizationReady(w) {
		return
	}
	picture, err := h.personalization.WallpaperPicture(r.Context())
	if errors.Is(err, personalization.ErrNoWallpaperImage) {
		writeError(w, http.StatusNotFound, "not_found", "the default wallpaper is in use")
		return
	}
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	if r.URL.Query().Get("v") == picture.Version {
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "private, no-cache")
	}
	w.Header().Set("Content-Type", personalization.WallpaperContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("ETag", `"`+picture.Version+`"`)
	http.ServeContent(w, r, "", picture.UpdatedAt, bytes.NewReader(picture.Data))
}
