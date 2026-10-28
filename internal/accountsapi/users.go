package accountsapi

import (
	"log/slog"
	"net/http"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
)

// Users serves what the account service keeps beside signing in: the
// accounts administrators manage, the signed-in user's password and
// notifications, viewing grants (ADR 0008) and the spaces a user sees. The
// account service decides who may do what.
type Users struct {
	accounts *accounts.Service
	logger   *slog.Logger
}

// NewUsers serves the accounts of service.
func NewUsers(service *accounts.Service, logger *slog.Logger) *Users {
	return &Users{accounts: service, logger: logger}
}

func (u *Users) Routes() []apiroute.Route {
	get, post, patch, del := http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete
	session, mutation, admin := apiroute.Session, apiroute.Mutation, apiroute.Admin
	// Managing accounts is the administrator's: the guard refuses a member
	// before the service checks again (#270).
	return []apiroute.Route{
		{Method: post, Pattern: "/api/v1/session/password", Posture: mutation, Handler: u.changePassword},
		{Method: get, Pattern: "/api/v1/notifications", Posture: session, Handler: u.notifications},
		{Method: post, Pattern: "/api/v1/notifications/{notificationID}/acknowledge", Posture: mutation, Handler: u.acknowledgeNotification},
		{Method: get, Pattern: "/api/v1/users", Posture: admin, Handler: u.list},
		{Method: post, Pattern: "/api/v1/users", Posture: admin, Handler: u.create},
		{Method: patch, Pattern: "/api/v1/users/{userID}/credential", Posture: admin, Handler: u.resetCredential},
		{Method: del, Pattern: "/api/v1/users/{userID}", Posture: admin, Handler: u.disable},
		{Method: post, Pattern: "/api/v1/users/{userID}/viewing", Posture: admin, Handler: u.startViewing},
		{Method: del, Pattern: "/api/v1/viewing/{grantID}", Posture: admin, Handler: u.endViewing},
		{Method: get, Pattern: "/api/v1/spaces", Posture: session, Handler: u.spaces},
	}
}

func (u *Users) spaces(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	spaces, err := u.accounts.ListSpaces(r.Context(), session.User)
	if err != nil {
		WriteError(w, r, u.logger, err)
		return
	}
	type visibleSpace struct {
		accounts.Space
		Writable bool `json:"writable"`
	}
	items := []visibleSpace{}
	for _, space := range spaces {
		writable, err := u.accounts.CanAccessSpace(r.Context(), session.User, space.ID, true)
		if err != nil {
			WriteError(w, r, u.logger, err)
			return
		}
		items = append(items, visibleSpace{Space: space, Writable: writable && space.Viewing == nil})
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": items})
}

func (u *Users) list(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	users, err := u.accounts.ListUsers(r.Context(), session.User)
	if err != nil {
		WriteError(w, r, u.logger, err)
		return
	}
	if users == nil {
		users = []accounts.User{}
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": users})
}

func (u *Users) create(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &request) {
		return
	}
	user, err := u.accounts.CreateMember(r.Context(), session.User, request.Username, request.Password)
	if err != nil {
		WriteError(w, r, u.logger, err)
		return
	}
	httpjson.Write(w, http.StatusCreated, user)
}

func (u *Users) resetCredential(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		Password string `json:"password"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &request) {
		return
	}
	if err := u.accounts.ResetCredential(r.Context(), session.User, r.PathValue("userID"), request.Password); err != nil {
		WriteError(w, r, u.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (u *Users) disable(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if err := u.accounts.DisableUser(r.Context(), session.User, r.PathValue("userID")); err != nil {
		WriteError(w, r, u.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (u *Users) changePassword(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &request) {
		return
	}
	if err := u.accounts.ChangePassword(r.Context(), session.User, request.CurrentPassword, request.NewPassword); err != nil {
		WriteError(w, r, u.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (u *Users) notifications(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	notifications, err := u.accounts.Notifications(r.Context(), session.User)
	if err != nil {
		WriteError(w, r, u.logger, err)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": notifications})
}

func (u *Users) acknowledgeNotification(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if err := u.accounts.AcknowledgeNotification(r.Context(), session.User, r.PathValue("notificationID")); err != nil {
		WriteError(w, r, u.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (u *Users) startViewing(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		Password string                `json:"password"`
		Reason   string                `json:"reason"`
		Scope    accounts.ViewingScope `json:"scope"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &request) {
		return
	}
	start := u.accounts.StartViewing
	switch request.Scope {
	case "", accounts.ViewingScopeSpace:
	case accounts.ViewingScopeLibrary:
		start = u.accounts.StartLibraryViewing
	default:
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "scope must be space or library")
		return
	}
	grant, err := start(r.Context(), session.User, r.PathValue("userID"), request.Password, request.Reason)
	if err != nil {
		WriteError(w, r, u.logger, err)
		return
	}
	httpjson.Write(w, http.StatusCreated, grant)
}

func (u *Users) endViewing(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if err := u.accounts.EndViewing(r.Context(), session.User, r.PathValue("grantID")); err != nil {
		WriteError(w, r, u.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
