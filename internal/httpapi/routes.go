package httpapi

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/accountsapi"
	"github.com/zhongwater123/A-NAS/internal/activityapi"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/appstoreapi"
	"github.com/zhongwater123/A-NAS/internal/assistantapi"
	"github.com/zhongwater123/A-NAS/internal/containersapi"
	"github.com/zhongwater123/A-NAS/internal/familyapi"
	"github.com/zhongwater123/A-NAS/internal/feedbackapi"
	"github.com/zhongwater123/A-NAS/internal/filepreviewapi"
	"github.com/zhongwater123/A-NAS/internal/filesapi"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/mediaapi"
	"github.com/zhongwater123/A-NAS/internal/musicapi"
	"github.com/zhongwater123/A-NAS/internal/personalizationapi"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
	"github.com/zhongwater123/A-NAS/internal/sharesapi"
	"github.com/zhongwater123/A-NAS/internal/storageapi"
	"github.com/zhongwater123/A-NAS/internal/systemlogapi"
	"github.com/zhongwater123/A-NAS/internal/taskmanager"
	"github.com/zhongwater123/A-NAS/internal/taskmanagerapi"
	"github.com/zhongwater123/A-NAS/internal/transfersapi"
)

// table is every route of the product API with its posture. A domain's own
// package lists its routes; the APIs mounted under a prefix serve every
// method beneath it themselves.
func (h *productHandler) table() []apiroute.Route {
	const (
		public   = apiroute.Public
		session  = apiroute.Session
		mutation = apiroute.Mutation
		admin    = apiroute.Admin
	)
	get, post, del := http.MethodGet, http.MethodPost, http.MethodDelete
	routes := []apiroute.Route{
		{Method: get, Pattern: "/healthz", Posture: public, Handler: h.serveState},
		{Method: get, Pattern: "/api/v1/system", Posture: session, Handler: h.serveState},
		{Method: get, Pattern: "/api/v1/disks", Posture: session, Handler: h.serveState},
		{Method: get, Pattern: "/api/v1/host-state", Posture: session, Handler: h.serveState},
		{Method: get, Pattern: "/api/v1/metrics", Posture: session, Handler: h.serveState},

		{Method: get, Pattern: "/api/v1/setup/status", Posture: public, Handler: h.handleSetupStatus},
		{Method: post, Pattern: "/api/v1/setup/admin", Posture: public, Handler: h.handleSetupAdministrator},
		{Method: post, Pattern: "/api/v1/session", Posture: public, Handler: h.handleCreateSession},
		{Method: get, Pattern: "/api/v1/session", Posture: session, Handler: h.handleCurrentSession},
		{Method: del, Pattern: "/api/v1/session", Posture: mutation, Handler: h.handleDeleteSession},

		{Method: get, Pattern: "/api/v1/terminal", Posture: admin, Handler: h.handleTerminal},
		{Method: get, Pattern: "/api/v1/terminal/session", Posture: admin, Handler: h.handleTerminal},
	}
	logger := h.Logger
	// The task manager needs a reader that also observes tasks, as the Host
	// Agent's client and the Fake Adapter do; without one it is unavailable.
	tasks, _ := h.Reader.(taskmanager.Reader)
	for _, domain := range []interface{ Routes() []apiroute.Route }{
		accountsapi.NewUsers(h.Accounts, logger),
		accountsapi.NewProfiles(h.Accounts, h.pictures, logger),
		storageapi.New(h.Storage, logger),
		filesapi.New(h.Files, h.FilesChanged, logger),
		filepreviewapi.New(h.FilePreviews, logger),
		personalizationapi.New(h.Personalization, h.pictures, logger),
		musicapi.New(h.Music, logger),
		activityapi.New(activityapi.Sources{Accounts: h.Accounts, Files: h.Files, Storage: h.Storage, Photos: h.photoActivitySource(), Logger: logger}),
		systemlogapi.New(h.SystemLogs, string(h.DataSource), logger),
		taskmanagerapi.New(tasks, string(h.DataSource), logger),
	} {
		routes = append(routes, domain.Routes()...)
	}
	return append(routes, h.mounts()...)
}

// mounts are the APIs that serve every method under their prefix and
// receive the signed-in user through accounts.WithUser. Each lists the
// operations it serves there, which the table expands (apiroute.Expand).
func (h *productHandler) mounts() []apiroute.Route {
	var routes []apiroute.Route
	mount := func(prefix string, posture apiroute.Posture, handler apiroute.Handler, endpoints []apiroute.Endpoint) {
		routes = append(routes, apiroute.Mount(prefix, posture, handler, endpoints)...)
	}
	if h.Containers != nil {
		mount(containersapi.PathPrefix, apiroute.Admin, serve(h.Containers), containersapi.Endpoints())
	}
	if h.Apps != nil {
		mount(appstoreapi.PathPrefix, apiroute.Admin, serve(h.Apps), appstoreapi.Endpoints())
	}
	if h.AppShortcuts != nil {
		mount(appstoreapi.ShortcutsPrefix, apiroute.Session, serve(h.AppShortcuts), appstoreapi.ShortcutEndpoints())
	}
	if h.Shares != nil {
		mount(sharesapi.PathPrefix, apiroute.Session, withUser(h.Shares), sharesapi.Endpoints())
	}
	if h.Family != nil {
		mount(familyapi.PathPrefix, apiroute.Session, withUser(h.Family), familyapi.Endpoints())
	}
	if h.Photos != nil {
		mount(photosapi.PathPrefix, apiroute.Session, serve(h.Photos), photosapi.Endpoints())
	}
	if h.Media != nil {
		mount(mediaapi.PathPrefix, apiroute.Session, withUser(h.Media), mediaapi.Endpoints())
	}
	if h.Feedback != nil {
		mount(feedbackapi.PathPrefix, apiroute.Session, withUser(h.Feedback), feedbackapi.Endpoints())
	}
	if h.Assistant != nil {
		mount(assistantapi.PathPrefix, apiroute.Session, withUser(h.Assistant), assistantapi.Endpoints())
	}
	if h.Transfers != nil {
		for _, prefix := range []string{transfersapi.TransfersPath, transfersapi.UploadsPath} {
			mount(prefix, apiroute.Session, withUser(h.Transfers), transfersapi.Endpoints())
		}
	}
	return routes
}

// register puts the table into the mux behind the guard of each route's
// posture, after checking that every posture fits its method.
func (h *productHandler) register(routes []apiroute.Route) {
	for _, route := range routes {
		if err := checkPosture(route); err != nil {
			panic(err)
		}
		pattern := route.Pattern
		if route.Method != "" {
			pattern = route.Method + " " + pattern
		}
		h.mux.Handle(pattern, h.guard(route))
	}
	h.mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
	})
}

// checkPosture refuses a route whose posture is missing or contradicts its
// method: a route left at the zero posture is not public by accident (#270),
// a read is never a Mutation, a change never only a Session, and a mounted
// API is a Session or Admin one, whose writes then need the CSRF token. A
// mounted route lists the operations it serves.
func checkPosture(route apiroute.Route) error {
	switch {
	case route.Posture < apiroute.Public || route.Posture > apiroute.Admin:
		return fmt.Errorf("route %s %s has no valid posture (%d)", route.Method, route.Pattern, route.Posture)
	case route.Method == "" && (route.Posture == apiroute.Public || route.Posture == apiroute.Mutation):
		return fmt.Errorf("route %s is mounted for every method as %s; mount it as session or admin", route.Pattern, route.Posture)
	case route.Method == "" && len(route.Endpoints) == 0:
		return fmt.Errorf("route %s is mounted for every method but lists no endpoint", route.Pattern)
	case route.Method == "" && slices.ContainsFunc(route.Endpoints, func(endpoint apiroute.Endpoint) bool { return !route.Serves(endpoint) }):
		return fmt.Errorf("route %s lists an endpoint it does not reach", route.Pattern)
	case route.Method != "" && apiroute.Safe(route.Method) && route.Posture == apiroute.Mutation:
		return fmt.Errorf("route %s %s only reads but is a mutation", route.Method, route.Pattern)
	case route.Method != "" && !apiroute.Safe(route.Method) && route.Posture == apiroute.Session:
		return fmt.Errorf("route %s %s changes something but only needs a session; make it a mutation", route.Method, route.Pattern)
	}
	return nil
}

// guard enforces a route's posture: a session cookie of this client, a new
// password where an administrator reset it, the CSRF token for anything but
// a read, and the administrator's role. The handler then finds the session
// token in the request's context for the File Broker, which checks it again.
func (h *productHandler) guard(route apiroute.Route) http.HandlerFunc {
	if route.Posture == apiroute.Public {
		return func(w http.ResponseWriter, r *http.Request) { route.Handler(w, r, accounts.Session{}) }
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if h.Accounts == nil {
			httpjson.WriteError(w, http.StatusServiceUnavailable, "accounts_unavailable", "account service is unavailable")
			return
		}
		session, token, ok := h.clientSession(r)
		if !ok {
			httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", "authentication is required")
			return
		}
		if session.User.MustChangePassword && !allowedDuringPasswordChange(r) {
			httpjson.WriteError(w, http.StatusForbidden, "password_change_required", "choose a new password before continuing")
			return
		}
		// The method decides: checkPosture keeps every Mutation route a
		// write and every Session route with a method a read.
		if !apiroute.Safe(r.Method) {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(session.CSRFToken)) != 1 {
				httpjson.WriteError(w, http.StatusForbidden, "csrf_failed", "CSRF token is invalid")
				return
			}
		}
		if route.Posture == apiroute.Admin && !session.User.ActiveAdministrator() {
			httpjson.WriteError(w, http.StatusForbidden, "forbidden", "administrator permission is required")
			return
		}
		route.Handler(w, r.WithContext(accounts.WithSessionToken(r.Context(), token)), session)
	}
}

// allowedDuringPasswordChange lists what a user whose password an
// administrator reset may do before choosing a new one.
func allowedDuringPasswordChange(r *http.Request) bool {
	switch {
	case r.URL.Path == "/api/v1/session":
		return true
	case r.URL.Path == "/api/v1/session/password" && r.Method == http.MethodPost:
		return true
	case r.URL.Path == "/api/v1/notifications" && r.Method == http.MethodGet:
		return true
	case strings.HasPrefix(r.URL.Path, "/api/v1/notifications/") && r.Method == http.MethodPost:
		return true
	}
	return false
}

// serve hands a request to an API that needs nothing of the session but its
// token, which the request's context already carries. The photo API, in the
// photo service or in development in this process, finds the caller behind
// the token itself (photoservice.Authenticate).
func serve(next http.Handler) apiroute.Handler {
	return func(w http.ResponseWriter, r *http.Request, _ accounts.Session) { next.ServeHTTP(w, r) }
}

// withUser hands an API the signed-in user through attach.
func withUser(next http.Handler) apiroute.Handler {
	return func(w http.ResponseWriter, r *http.Request, session accounts.Session) {
		next.ServeHTTP(w, r.WithContext(accounts.WithUser(r.Context(), session.User)))
	}
}

func (h *productHandler) serveState(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	h.state.ServeHTTP(w, r)
}
