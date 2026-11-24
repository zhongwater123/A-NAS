package phonehotspotapi

import (
	"log/slog"
	"net/http"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/phonehotspot"
)

const Path = "/api/v1/network/phone-hotspot"

type API struct {
	control phonehotspot.Switch
	logger  *slog.Logger
}

func New(control phonehotspot.Switch, logger *slog.Logger) *API {
	return &API{control: control, logger: logger}
}

func (a *API) Routes() []apiroute.Route {
	return []apiroute.Route{
		{Method: http.MethodGet, Pattern: Path, Posture: apiroute.Admin, Handler: a.serve},
		{Method: http.MethodPut, Pattern: Path, Posture: apiroute.Admin, Handler: a.serve},
	}
}

func (a *API) serve(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	w.Header().Set("Cache-Control", "no-store")
	var enabled *bool
	if r.Method == http.MethodPut {
		var request phonehotspot.Request
		if err := httpjson.Decode(w, r, 1024, &request); err != nil || request.Enabled == nil {
			httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "request is invalid")
			return
		}
		enabled = request.Enabled
	}
	if a.control == nil {
		httpjson.Write(w, http.StatusOK, phonehotspot.State{State: phonehotspot.Unavailable, Reason: "dependencies_missing"})
		return
	}
	var state phonehotspot.State
	var err error
	if enabled == nil {
		state, err = a.control.PhoneHotspot(r.Context())
	} else {
		state, err = a.control.SetPhoneHotspot(r.Context(), *enabled)
	}
	if err != nil {
		a.logger.ErrorContext(r.Context(), "phone hotspot request failed", "error", err)
		httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_unavailable", "phone hotspot is unavailable")
		return
	}
	httpjson.Write(w, http.StatusOK, state)
}
