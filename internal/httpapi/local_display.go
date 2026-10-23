package httpapi

import (
	"errors"
	"net/http"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/personalization"
)

// handleLocalDisplay needs no session: the sign-in page on the device's own
// screen reads it too, and it reveals only how long the screen stays on.
func (h *productHandler) handleLocalDisplay(w http.ResponseWriter, r *http.Request) {
	if !h.personalizationReady(w) {
		return
	}
	display, err := h.personalization.LocalDisplay(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, display)
}

func (h *productHandler) handleUpdateLocalDisplay(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if !h.requireAdministrator(w, session) || !h.personalizationReady(w) {
		return
	}
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
	display, err := h.personalization.SetLocalDisplay(r.Context(), personalization.LocalDisplay{SleepAfterMinutes: *request.SleepAfterMinutes})
	if errors.Is(err, personalization.ErrInvalidSetting) {
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "validation_failed", err.Error())
		return
	}
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, display)
}
