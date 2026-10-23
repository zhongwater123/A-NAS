package httpapi

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/avatar"
)

// pictureGate decodes one uploaded avatar or wallpaper at a time; a
// full-size photo sent straight to the API takes a few hundred milliseconds
// and about 100 MB.
var pictureGate = make(chan struct{}, 1)

func (h *productHandler) profileRoutes() {
	h.mux.HandleFunc("GET /api/v1/profiles", h.withSession(h.handleProfiles))
	h.mux.HandleFunc("PATCH /api/v1/session/profile", h.withMutation(h.handleUpdateProfile))
	h.mux.HandleFunc("PUT /api/v1/session/avatar", h.withMutation(h.handleSetAvatar))
	h.mux.HandleFunc("DELETE /api/v1/session/avatar", h.withMutation(h.handleRemoveAvatar))
	h.mux.HandleFunc("GET /api/v1/users/{userID}/avatar", h.withSession(h.handleAvatar))
}

func (h *productHandler) handleProfiles(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	profiles, err := h.accounts.Profiles(r.Context(), session.User)
	if err != nil {
		h.writeAccountError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": profiles})
}

func (h *productHandler) handleUpdateProfile(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		Nickname *string `json:"nickname"`
	}
	if err := decodeJSON(r, &request); err != nil || request.Nickname == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	user, err := h.accounts.SetNickname(r.Context(), session.User, *request.Nickname)
	if err != nil {
		h.writeAccountError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, user)
}

// handleSetAvatar takes the picture as the raw request body. The page crops
// it first, but the stored avatar is always made here, so no client decides
// what other people's pages load.
func (h *productHandler) handleSetAvatar(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if !strings.HasPrefix(mediaType, "image/") {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_image", "send a JPEG, PNG, GIF or WebP picture")
		return
	}
	picture, err := io.ReadAll(http.MaxBytesReader(w, r.Body, avatar.MaxUploadBytes))
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		writeError(w, http.StatusRequestEntityTooLarge, "image_too_large", avatar.ErrTooLarge.Error())
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
	normalized, err := avatar.Normalize(picture)
	<-pictureGate
	switch {
	case errors.Is(err, avatar.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "image_too_large", err.Error())
		return
	case errors.Is(err, avatar.ErrUnsupported):
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_image", avatar.ErrUnsupported.Error())
		return
	case err != nil:
		h.internalError(w, r, err)
		return
	}
	user, err := h.accounts.SetAvatar(r.Context(), session.User, normalized, avatar.ContentType)
	if err != nil {
		h.writeAccountError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, user)
}

func (h *productHandler) handleRemoveAvatar(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	user, err := h.accounts.RemoveAvatar(r.Context(), session.User)
	if err != nil {
		h.writeAccountError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, user)
}

// handleAvatar serves an account's avatar to anyone signed in. Pages ask for
// it with ?v=<avatarVersion>; a new avatar has a new version, so the browser
// keeps that URL for good instead of asking again on every page.
func (h *productHandler) handleAvatar(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	picture, err := h.accounts.Avatar(r.Context(), r.PathValue("userID"))
	if errors.Is(err, accounts.ErrAvatarNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "the account has no avatar")
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
	w.Header().Set("Content-Type", picture.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("ETag", `"`+picture.Version+`"`)
	http.ServeContent(w, r, "", picture.UpdatedAt, bytes.NewReader(picture.Image))
}
