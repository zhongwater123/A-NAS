// Package personalizationapi serves the device-wide 个性化 settings
// (docs/specs/personalization.md): the desktop wallpaper, which everyone
// signed in reads, and how long the local screen stays on, which its sign-in
// page reads too. Only administrators change either.
package personalizationapi

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"

	"golang.org/x/sync/semaphore"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/personalization"
)

// API serves the settings in store; a nil store answers
// personalization_unavailable.
type API struct {
	store *personalization.Store
	// pictures decodes one uploaded picture at a time, shared with avatars.
	pictures *semaphore.Weighted
	logger   *slog.Logger
}

func New(store *personalization.Store, pictures *semaphore.Weighted, logger *slog.Logger) *API {
	return &API{store: store, pictures: pictures, logger: logger}
}

func (a *API) Routes() []apiroute.Route {
	return []apiroute.Route{
		{Method: http.MethodGet, Pattern: "/api/v1/wallpaper", Posture: apiroute.Session, Handler: a.ready(a.wallpaper)},
		{Method: http.MethodPatch, Pattern: "/api/v1/wallpaper", Posture: apiroute.Admin, Handler: a.ready(a.updateWallpaper)},
		{Method: http.MethodGet, Pattern: "/api/v1/wallpaper/image", Posture: apiroute.Session, Handler: a.ready(a.wallpaperImage)},
		{Method: http.MethodPut, Pattern: "/api/v1/wallpaper/image", Posture: apiroute.Admin, Handler: a.ready(a.setWallpaperImage)},
		{Method: http.MethodDelete, Pattern: "/api/v1/wallpaper/image", Posture: apiroute.Admin, Handler: a.ready(a.resetWallpaper)},
		// The sign-in page on the device's own screen reads this too, and it
		// reveals only how long the screen stays on.
		{Method: http.MethodGet, Pattern: "/api/v1/local-display", Posture: apiroute.Public, Handler: a.ready(a.localDisplay)},
		{Method: http.MethodPut, Pattern: "/api/v1/local-display", Posture: apiroute.Admin, Handler: a.ready(a.updateLocalDisplay)},
	}
}

func (a *API) ready(next apiroute.Handler) apiroute.Handler {
	return func(w http.ResponseWriter, r *http.Request, session accounts.Session) {
		if a.store == nil {
			httpjson.WriteError(w, http.StatusServiceUnavailable, "personalization_unavailable", "personalization settings are unavailable")
			return
		}
		next(w, r, session)
	}
}

func (a *API) wallpaper(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	wallpaper, err := a.store.Wallpaper(r.Context())
	if err != nil {
		httpjson.WriteInternal(w, r, a.logger, err)
		return
	}
	httpjson.Write(w, http.StatusOK, wallpaper)
}

func (a *API) updateWallpaper(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	var request struct {
		Style *string `json:"style"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &request) {
		return
	}
	if request.Style == nil {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	wallpaper, err := a.store.SetWallpaperStyle(r.Context(), *request.Style)
	if errors.Is(err, personalization.ErrInvalidStyle) {
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "validation_failed", err.Error())
		return
	}
	if err != nil {
		httpjson.WriteInternal(w, r, a.logger, err)
		return
	}
	httpjson.Write(w, http.StatusOK, wallpaper)
}

// setWallpaperImage takes the picture as the raw request body. The page
// scales it first, but the stored wallpaper is always made here.
func (a *API) setWallpaperImage(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if !strings.HasPrefix(mediaType, "image/") {
		httpjson.WriteError(w, http.StatusUnsupportedMediaType, "unsupported_image", "send a JPEG, PNG, GIF or WebP picture")
		return
	}
	picture, err := io.ReadAll(http.MaxBytesReader(w, r.Body, personalization.MaxWallpaperUploadBytes))
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		httpjson.WriteError(w, http.StatusRequestEntityTooLarge, "image_too_large", personalization.ErrImageTooLarge.Error())
		return
	}
	if err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "the picture could not be read")
		return
	}
	if a.pictures.Acquire(r.Context(), 1) != nil {
		return
	}
	data, width, height, err := personalization.NormalizeWallpaper(picture)
	a.pictures.Release(1)
	switch {
	case errors.Is(err, personalization.ErrImageTooLarge):
		httpjson.WriteError(w, http.StatusRequestEntityTooLarge, "image_too_large", err.Error())
		return
	case errors.Is(err, personalization.ErrUnsupportedImage):
		httpjson.WriteError(w, http.StatusUnsupportedMediaType, "unsupported_image", personalization.ErrUnsupportedImage.Error())
		return
	case err != nil:
		httpjson.WriteInternal(w, r, a.logger, err)
		return
	}
	wallpaper, err := a.store.SetWallpaperImage(r.Context(), data, width, height)
	if err != nil {
		httpjson.WriteInternal(w, r, a.logger, err)
		return
	}
	httpjson.Write(w, http.StatusOK, wallpaper)
}

func (a *API) resetWallpaper(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	wallpaper, err := a.store.ResetWallpaper(r.Context())
	if err != nil {
		httpjson.WriteInternal(w, r, a.logger, err)
		return
	}
	httpjson.Write(w, http.StatusOK, wallpaper)
}

// wallpaperImage serves the picture with ?v=<version>; a new picture has a
// new version, so browsers keep that URL for good.
func (a *API) wallpaperImage(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	picture, err := a.store.WallpaperPicture(r.Context())
	if errors.Is(err, personalization.ErrNoWallpaperImage) {
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "the default wallpaper is in use")
		return
	}
	if err != nil {
		httpjson.WriteInternal(w, r, a.logger, err)
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

func (a *API) localDisplay(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	display, err := a.store.LocalDisplay(r.Context())
	if err != nil {
		httpjson.WriteInternal(w, r, a.logger, err)
		return
	}
	httpjson.Write(w, http.StatusOK, display)
}

func (a *API) updateLocalDisplay(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	var request struct {
		SleepAfterMinutes *int `json:"sleepAfterMinutes"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &request) {
		return
	}
	if request.SleepAfterMinutes == nil {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	display, err := a.store.SetLocalDisplay(r.Context(), personalization.LocalDisplay{SleepAfterMinutes: *request.SleepAfterMinutes})
	if errors.Is(err, personalization.ErrInvalidSetting) {
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "validation_failed", err.Error())
		return
	}
	if err != nil {
		httpjson.WriteInternal(w, r, a.logger, err)
		return
	}
	httpjson.Write(w, http.StatusOK, display)
}
