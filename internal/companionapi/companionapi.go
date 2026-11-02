// Package companionapi exposes member-scoped companion state behind the
// Product Service's existing authentication and CSRF route guards.
package companionapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/companion"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
)

type API struct {
	store  *companion.Store
	logger *slog.Logger
}

func New(store *companion.Store, logger *slog.Logger) *API {
	if logger == nil {
		logger = slog.Default()
	}
	return &API{store, logger}
}
func (a *API) Routes() []apiroute.Route {
	return []apiroute.Route{
		{Method: http.MethodGet, Pattern: "/api/v1/companion", Posture: apiroute.Session, Handler: a.ready(a.read)},
		{Method: http.MethodPost, Pattern: "/api/v1/companion/claim", Posture: apiroute.Mutation, Handler: a.ready(a.claim)},
		{Method: http.MethodPost, Pattern: "/api/v1/companion/heartbeat", Posture: apiroute.Mutation, Handler: a.ready(a.heartbeat)},
		{Method: http.MethodPost, Pattern: "/api/v1/companion/release", Posture: apiroute.Mutation, Handler: a.ready(a.release)},
		{Method: http.MethodPost, Pattern: "/api/v1/companion/actions", Posture: apiroute.Mutation, Handler: a.ready(a.act)},
	}
}
func (a *API) ready(next apiroute.Handler) apiroute.Handler {
	return func(w http.ResponseWriter, r *http.Request, session accounts.Session) {
		if a.store == nil {
			httpjson.WriteError(w, http.StatusServiceUnavailable, "companion_unavailable", "companion state is unavailable")
			return
		}
		next(w, r, session)
	}
}
func (a *API) answer(w http.ResponseWriter, r *http.Request, state companion.State, err error) {
	switch {
	case errors.Is(err, companion.ErrInvalid):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "validation_failed", "invalid companion command")
	case errors.Is(err, companion.ErrConflict):
		httpjson.WriteError(w, http.StatusConflict, "companion_conflict", "refresh the companion state before continuing")
	case err != nil:
		httpjson.WriteInternal(w, r, a.logger, err)
	default:
		httpjson.Write(w, http.StatusOK, state)
	}
}
func (a *API) read(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	state, err := a.store.Read(r.Context(), session.User.ID)
	a.answer(w, r, state, err)
}
func (a *API) claim(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var body struct {
		ClientID *string `json:"clientId"`
		Kind     *string `json:"kind"`
		Revision *int64  `json:"revision"`
	}
	if !httpjson.Read(w, r, 1024, &body) {
		return
	}
	if body.ClientID == nil || body.Kind == nil || body.Revision == nil {
		httpjson.WriteError(w, 400, "invalid_request", "missing companion claim fields")
		return
	}
	state, err := a.store.Claim(r.Context(), session.User.ID, companion.Claim{ClientID: *body.ClientID, Kind: *body.Kind, Revision: *body.Revision})
	a.answer(w, r, state, err)
}
func holder(w http.ResponseWriter, r *http.Request) (companion.Holder, bool) {
	var body struct {
		ClientID *string `json:"clientId"`
		Epoch    *int64  `json:"epoch"`
	}
	if !httpjson.Read(w, r, 1024, &body) {
		return companion.Holder{}, false
	}
	if body.ClientID == nil || body.Epoch == nil {
		httpjson.WriteError(w, 400, "invalid_request", "missing companion lease fields")
		return companion.Holder{}, false
	}
	return companion.Holder{ClientID: *body.ClientID, Epoch: *body.Epoch}, true
}
func (a *API) heartbeat(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	body, ok := holder(w, r)
	if !ok {
		return
	}
	state, err := a.store.Heartbeat(r.Context(), session.User.ID, body)
	a.answer(w, r, state, err)
}
func (a *API) release(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	body, ok := holder(w, r)
	if !ok {
		return
	}
	state, err := a.store.Release(r.Context(), session.User.ID, body)
	a.answer(w, r, state, err)
}
func (a *API) act(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var body companion.Action
	if !httpjson.Read(w, r, 1024, &body) {
		return
	}
	state, err := a.store.Act(r.Context(), session.User.ID, body)
	a.answer(w, r, state, err)
}
