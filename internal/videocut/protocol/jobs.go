package protocol

import (
	"net/http"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
)

type noticesResponse struct {
	Items any `json:"items"`
}

func (h *handler) notices(w http.ResponseWriter, r *http.Request, user accounts.User) {
	notices, err := h.store.Notices(r.Context(), user.ID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, noticesResponse{Items: notices})
}

// notePlanGrant records the planning grant the Product Service registered
// for this request, as AssistantHeader says (planning.go).
func (h *handler) notePlanGrant(w http.ResponseWriter, r *http.Request, user accounts.User) bool {
	if r.Header.Get(AssistantHeader) != "on" {
		return true
	}
	if err := h.store.RegisterPlanGrant(r.Context(), user.ID, user.Username); err != nil {
		h.fail(w, r, err)
		return false
	}
	return true
}

// planningNotImplemented is notImplemented for the planning routes: the
// planning grant the Product Service registered is noted already, as
// generating will need it.
func (h *handler) planningNotImplemented(w http.ResponseWriter, r *http.Request, user accounts.User) {
	if h.notePlanGrant(w, r, user) {
		notImplemented(w, r, user)
	}
}
