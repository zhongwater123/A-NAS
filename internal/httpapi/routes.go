package httpapi

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/accountsapi"
	"github.com/zhongwater123/A-NAS/internal/activityapi"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/appstoreapi"
	"github.com/zhongwater123/A-NAS/internal/assistantapi"
	"github.com/zhongwater123/A-NAS/internal/feedbackapi"
	"github.com/zhongwater123/A-NAS/internal/filepreviewapi"
	"github.com/zhongwater123/A-NAS/internal/filesapi"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/mediaapi"
	"github.com/zhongwater123/A-NAS/internal/musicapi"
	"github.com/zhongwater123/A-NAS/internal/personalizationapi"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
	"github.com/zhongwater123/A-NAS/internal/sharesapi"
	"github.com/zhongwater123/A-NAS/internal/systemlogapi"
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
	get, post, patch, del := http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete
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
		{Method: post, Pattern: "/api/v1/session/password", Posture: mutation, Handler: h.handleChangePassword},
		{Method: get, Pattern: "/api/v1/notifications", Posture: session, Handler: h.handleNotifications},
		{Method: post, Pattern: "/api/v1/notifications/{notificationID}/acknowledge", Posture: mutation, Handler: h.handleAcknowledgeNotification},
		{Method: get, Pattern: "/api/v1/users", Posture: session, Handler: h.handleUsers},
		{Method: post, Pattern: "/api/v1/users", Posture: mutation, Handler: h.handleCreateUser},
		{Method: patch, Pattern: "/api/v1/users/{userID}/credential", Posture: mutation, Handler: h.handleResetUserCredential},
		{Method: del, Pattern: "/api/v1/users/{userID}", Posture: mutation, Handler: h.handleDisableUser},
		{Method: post, Pattern: "/api/v1/users/{userID}/viewing", Posture: mutation, Handler: h.handleStartViewing},
		{Method: del, Pattern: "/api/v1/viewing/{grantID}", Posture: mutation, Handler: h.handleEndViewing},
		{Method: get, Pattern: "/api/v1/spaces", Posture: session, Handler: h.handleSpaces},

		{Method: get, Pattern: "/api/v1/volumes", Posture: session, Handler: h.handleVolumes},
		{Method: post, Pattern: "/api/v1/volumes/{volumeID}/conversion", Posture: admin, Handler: h.handleContinueConversion},
		{Method: get, Pattern: "/api/v1/storage/plans", Posture: admin, Handler: h.handleUnfinishedStoragePlans},
		{Method: post, Pattern: "/api/v1/storage/plans", Posture: admin, Handler: h.handleCreateStoragePlan},
		{Method: post, Pattern: "/api/v1/storage/plans/{planID}/confirm", Posture: admin, Handler: h.handleConfirmStoragePlan},
		{Method: post, Pattern: "/api/v1/storage/plans/{planID}/execute", Posture: admin, Handler: h.handleExecuteStoragePlan},
		{Method: post, Pattern: "/api/v1/storage/plans/{planID}/cancel", Posture: admin, Handler: h.handleCancelStoragePlan},
		{Method: get, Pattern: "/api/v1/operations/{operationID}", Posture: admin, Handler: h.handleOperation},

		{Method: get, Pattern: "/api/v1/terminal", Posture: admin, Handler: h.handleTerminal},
		{Method: get, Pattern: "/api/v1/terminal/session", Posture: admin, Handler: h.handleTerminal},
	}
	logger := h.logger
	for _, domain := range []interface{ Routes() []apiroute.Route }{
		filesapi.New(h.files, h.changed, logger),
		filepreviewapi.New(h.previews, logger),
		accountsapi.NewProfiles(h.accounts, h.pictures, logger),
		personalizationapi.New(h.personalization, h.pictures, logger),
		musicapi.New(h.music, logger),
		activityapi.New(activityapi.Sources{Accounts: h.accounts, Files: h.files, Storage: h.storage, Photos: h.photoActivitySource(), Logger: logger}),
		systemlogapi.New(h.systemLogs, string(h.dataSource), logger),
		taskmanagerapi.New(h.reader, string(h.dataSource), logger),
	} {
		routes = append(routes, domain.Routes()...)
	}
	return append(routes, h.mounts()...)
}

// mounts are the APIs that serve every method beneath their prefix and
// receive the signed-in user through their package's WithUser.
func (h *productHandler) mounts() []apiroute.Route {
	var routes []apiroute.Route
	if h.containers != nil {
		routes = append(routes, apiroute.Mount("/api/v1/containers", apiroute.Admin, serve(h.containers))...)
	}
	if h.apps != nil {
		routes = append(routes, apiroute.Mount("/api/v1/apps", apiroute.Admin, serve(h.apps))...)
	}
	if h.shortcuts != nil {
		routes = append(routes, apiroute.Mount(appstoreapi.ShortcutsPrefix, apiroute.Session, serve(h.shortcuts))...)
	}
	if h.shares != nil {
		routes = append(routes, apiroute.Mount(sharesapi.PathPrefix, apiroute.Session, withUser(h.shares, sharesapi.WithUser))...)
	}
	if h.photos != nil {
		routes = append(routes, h.photoRoute())
	}
	if h.media != nil {
		routes = append(routes, apiroute.Route{Pattern: mediaapi.PathPrefix + "/", Posture: apiroute.Session, Handler: withUser(h.media, mediaapi.WithUser)})
	}
	if h.feedback != nil {
		routes = append(routes, apiroute.Mount(feedbackapi.PathPrefix, apiroute.Session, withUser(h.feedback, feedbackapi.WithUser))...)
	}
	if h.assistant != nil {
		routes = append(routes, apiroute.Route{Pattern: assistantapi.PathPrefix + "/", Posture: apiroute.Session, Handler: withUser(h.assistant, assistantapi.WithUser)})
	}
	if h.transfers != nil {
		for _, prefix := range []string{transfersapi.TransfersPath, transfersapi.UploadsPath} {
			routes = append(routes, apiroute.Mount(prefix, apiroute.Session, withUser(h.transfers, transfersapi.WithUser))...)
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

// checkPosture refuses a route whose posture contradicts its method: a read
// is never a Mutation, a change never only a Session, and a mounted API is a
// Session or Admin one, whose writes then need the CSRF token.
func checkPosture(route apiroute.Route) error {
	switch {
	case route.Method == "" && (route.Posture == apiroute.Public || route.Posture == apiroute.Mutation):
		return fmt.Errorf("route %s is mounted for every method as %s; mount it as session or admin", route.Pattern, route.Posture)
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
		if h.accounts == nil {
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
		if route.Posture == apiroute.Mutation || !apiroute.Safe(r.Method) {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(session.CSRFToken)) != 1 {
				httpjson.WriteError(w, http.StatusForbidden, "csrf_failed", "CSRF token is invalid")
				return
			}
		}
		if route.Posture == apiroute.Admin && session.User.Role != accounts.RoleAdmin {
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
// token, which the request's context already carries.
func serve(next http.Handler) apiroute.Handler {
	return func(w http.ResponseWriter, r *http.Request, _ accounts.Session) { next.ServeHTTP(w, r) }
}

// withUser hands an API the signed-in user through attach.
func withUser(next http.Handler, attach func(context.Context, accounts.User) context.Context) apiroute.Handler {
	return func(w http.ResponseWriter, r *http.Request, session accounts.Session) {
		next.ServeHTTP(w, r.WithContext(attach(r.Context(), session.User)))
	}
}

// photoRoute admits signed-in users to the photo API, which applies its own
// Policy. An in-process photo API receives the caller as a Principal, while a
// Proxy forwards only the session token for the photo service to confirm.
func (h *productHandler) photoRoute() apiroute.Route {
	handler := serve(h.photos)
	if _, proxied := h.photos.(*photosapi.Proxy); !proxied {
		handler = func(w http.ResponseWriter, r *http.Request, session accounts.Session) {
			viewings, err := h.accounts.LibraryViewings(r.Context(), session.User)
			if err != nil {
				h.internalError(w, r, err)
				return
			}
			principal := photosapi.NewPrincipal(session.User.ID, session.User.Username, session.User.Role == accounts.RoleAdmin, viewings)
			h.photos.ServeHTTP(w, r.WithContext(photosapi.WithPrincipal(r.Context(), principal)))
		}
	}
	return apiroute.Route{Pattern: photosapi.PathPrefix + "/", Posture: apiroute.Session, Handler: handler}
}

func (h *productHandler) serveState(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	h.state.ServeHTTP(w, r)
}
