package accountsapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
)

// DeviceTokens lets an administrator register, list and revoke the e-ink
// screens of the whole family (ADR 0041). There is no per-user list: a screen
// is not bound to any account.
type DeviceTokens struct {
	accounts *accounts.Service
	logger   *slog.Logger
}

// NewDeviceTokens serves the family's device tokens from service.
func NewDeviceTokens(service *accounts.Service, logger *slog.Logger) *DeviceTokens {
	return &DeviceTokens{accounts: service, logger: logger}
}

// DeviceTokensPath is where an administrator manages device tokens.
//
//nolint:gosec // G101: a route, not a credential.
const DeviceTokensPath = "/api/v1/device-tokens"

func (d *DeviceTokens) Routes() []apiroute.Route {
	return []apiroute.Route{
		{Method: http.MethodGet, Pattern: DeviceTokensPath, Posture: apiroute.Admin, Handler: d.list},
		{Method: http.MethodPost, Pattern: DeviceTokensPath, Posture: apiroute.Admin, Handler: d.create},
		{Method: http.MethodDelete, Pattern: DeviceTokensPath + "/{tokenID}", Posture: apiroute.Admin, Handler: d.revoke},
	}
}

func (d *DeviceTokens) list(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	tokens, err := d.accounts.DeviceTokens(r.Context(), session.User)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": tokens})
}

func (d *DeviceTokens) create(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		Name        string `json:"name"`
		PollSeconds int    `json:"pollSeconds"`
	}
	if !httpjson.Read(w, r, 1<<10, &request) {
		return
	}
	if request.PollSeconds == 0 {
		request.PollSeconds = accounts.DefaultDevicePollSeconds
	}
	token, value, err := d.accounts.CreateDeviceToken(r.Context(), session.User, request.Name, request.PollSeconds)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusCreated, map[string]any{"deviceToken": token, "token": value})
}

func (d *DeviceTokens) revoke(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if err := d.accounts.RevokeDeviceToken(r.Context(), session.User, r.PathValue("tokenID")); err != nil {
		d.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d *DeviceTokens) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, accounts.ErrDeviceTokenLimit):
		httpjson.WriteError(w, http.StatusConflict, "device_token_limit", err.Error())
	case errors.Is(err, accounts.ErrDeviceTokenNotFound):
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "the device token was not found")
	default:
		WriteError(w, r, d.logger, err)
	}
}
