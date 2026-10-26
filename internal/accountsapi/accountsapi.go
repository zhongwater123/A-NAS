// Package accountsapi is the HTTP side of internal/accounts that is not
// signing in: how account errors are answered, and every account's profile
// (nickname and avatar, docs/specs/account-profile.md).
package accountsapi

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
	"github.com/zhongwater123/A-NAS/internal/avatar"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
)

// WriteError answers an error of the account service.
func WriteError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	switch {
	case errors.Is(err, accounts.ErrSetupComplete):
		httpjson.WriteError(w, http.StatusConflict, "setup_complete", err.Error())
	case errors.Is(err, accounts.ErrUsernameUnavailable), errors.Is(err, accounts.ErrIdentityConflict):
		httpjson.WriteError(w, http.StatusConflict, "username_unavailable", err.Error())
	case errors.Is(err, accounts.ErrViewingActive):
		httpjson.WriteError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, accounts.ErrNicknameUnavailable):
		httpjson.WriteError(w, http.StatusConflict, "nickname_unavailable", err.Error())
	case errors.Is(err, accounts.ErrInvalidNickname):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "invalid_nickname", err.Error())
	case errors.Is(err, accounts.ErrInvalidUsername):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "invalid_username", err.Error())
	case errors.Is(err, accounts.ErrInvalidPassword):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "invalid_password", err.Error())
	case errors.Is(err, accounts.ErrPasswordUnchanged):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "password_unchanged", err.Error())
	case errors.Is(err, accounts.ErrReasonRequired):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "validation_failed", err.Error())
	case errors.Is(err, accounts.ErrInvalidCredentials):
		httpjson.WriteError(w, http.StatusForbidden, "reauthentication_failed", "the password is incorrect")
	case errors.Is(err, accounts.ErrVolumeUnavailable):
		httpjson.WriteError(w, http.StatusLocked, "volume_unavailable", err.Error())
	case errors.Is(err, accounts.ErrSMBDisablePending):
		httpjson.WriteError(w, http.StatusServiceUnavailable, "smb_disable_pending", err.Error())
	case errors.Is(err, accounts.ErrCredentialProvision):
		httpjson.WriteError(w, http.StatusServiceUnavailable, "credential_provision_failed", "account could not be enabled for SMB; repair the host service and retry")
	case errors.Is(err, accounts.ErrForbidden):
		httpjson.WriteError(w, http.StatusForbidden, "forbidden", "operation is forbidden")
	case errors.Is(err, accounts.ErrUserNotFound):
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "user was not found")
	default:
		httpjson.WriteInternal(w, r, logger, err)
	}
}

// Profiles serves every account's nickname and avatar to everyone signed in;
// each account changes only its own.
type Profiles struct {
	accounts *accounts.Service
	// pictures decodes one uploaded picture at a time, shared with the
	// wallpaper; a full-size photo sent straight to the API takes a few
	// hundred milliseconds and about 100 MB.
	pictures *semaphore.Weighted
	logger   *slog.Logger
}

// NewProfiles serves profiles from service; pictures bounds the decodes.
func NewProfiles(service *accounts.Service, pictures *semaphore.Weighted, logger *slog.Logger) *Profiles {
	return &Profiles{accounts: service, pictures: pictures, logger: logger}
}

func (p *Profiles) Routes() []apiroute.Route {
	return []apiroute.Route{
		{Method: http.MethodGet, Pattern: "/api/v1/profiles", Posture: apiroute.Session, Handler: p.list},
		{Method: http.MethodPatch, Pattern: "/api/v1/session/profile", Posture: apiroute.Mutation, Handler: p.update},
		{Method: http.MethodPut, Pattern: "/api/v1/session/avatar", Posture: apiroute.Mutation, Handler: p.setAvatar},
		{Method: http.MethodDelete, Pattern: "/api/v1/session/avatar", Posture: apiroute.Mutation, Handler: p.removeAvatar},
		{Method: http.MethodGet, Pattern: "/api/v1/users/{userID}/avatar", Posture: apiroute.Session, Handler: p.avatar},
	}
}

func (p *Profiles) list(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	profiles, err := p.accounts.Profiles(r.Context(), session.User)
	if err != nil {
		WriteError(w, r, p.logger, err)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": profiles})
}

func (p *Profiles) update(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		Nickname *string `json:"nickname"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &request) {
		return
	}
	if request.Nickname == nil {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	user, err := p.accounts.SetNickname(r.Context(), session.User, *request.Nickname)
	if err != nil {
		WriteError(w, r, p.logger, err)
		return
	}
	httpjson.Write(w, http.StatusOK, user)
}

// setAvatar takes the picture as the raw request body. The page crops it
// first, but the stored avatar is always made here, so no client decides what
// other people's pages load.
func (p *Profiles) setAvatar(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if !strings.HasPrefix(mediaType, "image/") {
		httpjson.WriteError(w, http.StatusUnsupportedMediaType, "unsupported_image", "send a JPEG, PNG, GIF or WebP picture")
		return
	}
	picture, err := io.ReadAll(http.MaxBytesReader(w, r.Body, avatar.MaxUploadBytes))
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		httpjson.WriteError(w, http.StatusRequestEntityTooLarge, "image_too_large", avatar.ErrTooLarge.Error())
		return
	}
	if err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "the picture could not be read")
		return
	}
	if p.pictures.Acquire(r.Context(), 1) != nil {
		return
	}
	normalized, err := avatar.Normalize(picture)
	p.pictures.Release(1)
	switch {
	case errors.Is(err, avatar.ErrTooLarge):
		httpjson.WriteError(w, http.StatusRequestEntityTooLarge, "image_too_large", err.Error())
		return
	case errors.Is(err, avatar.ErrUnsupported):
		httpjson.WriteError(w, http.StatusUnsupportedMediaType, "unsupported_image", avatar.ErrUnsupported.Error())
		return
	case err != nil:
		httpjson.WriteInternal(w, r, p.logger, err)
		return
	}
	user, err := p.accounts.SetAvatar(r.Context(), session.User, normalized, avatar.ContentType)
	if err != nil {
		WriteError(w, r, p.logger, err)
		return
	}
	httpjson.Write(w, http.StatusOK, user)
}

func (p *Profiles) removeAvatar(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	user, err := p.accounts.RemoveAvatar(r.Context(), session.User)
	if err != nil {
		WriteError(w, r, p.logger, err)
		return
	}
	httpjson.Write(w, http.StatusOK, user)
}

// avatar serves an account's avatar to anyone signed in. Pages ask for it
// with ?v=<avatarVersion>; a new avatar has a new version, so the browser
// keeps that URL for good instead of asking again on every page.
func (p *Profiles) avatar(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	picture, err := p.accounts.Avatar(r.Context(), r.PathValue("userID"))
	if errors.Is(err, accounts.ErrAvatarNotFound) {
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "the account has no avatar")
		return
	}
	if err != nil {
		httpjson.WriteInternal(w, r, p.logger, err)
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
