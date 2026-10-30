package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/accountsapi"
	"github.com/zhongwater123/A-NAS/internal/assistantapi"
	"github.com/zhongwater123/A-NAS/internal/filepreview"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/hoststate"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/localorigin"
	"github.com/zhongwater123/A-NAS/internal/music"
	"github.com/zhongwater123/A-NAS/internal/personalization"
	"github.com/zhongwater123/A-NAS/internal/storage"
	"github.com/zhongwater123/A-NAS/internal/systemlog"
	"github.com/zhongwater123/A-NAS/internal/websearchapi"
)

const sessionCookieName = "anas_session"

type ProductDependencies struct {
	Reader         hoststate.Observer
	DataSource     DataSource
	ProductVersion string
	Accounts       *accounts.Service
	Files          *files.Service
	FilePreviews   *filepreview.Service
	Storage        *storage.Service
	Terminal       http.Handler
	// Containers and Apps serve /api/v1/containers and /api/v1/apps. They
	// manage Docker, so only administrators reach them (ADR 0008).
	Containers http.Handler
	Apps       http.Handler
	// AppShortcuts serves /api/v1/app-shortcuts, the installed apps every
	// signed-in user opens from the desktop.
	AppShortcuts http.Handler
	// Photos serves /api/v1/photos to every signed-in user and enforces the
	// photo Policy itself. It receives only the session token, in the
	// request's context, and finds the caller behind it: the photo service
	// on a NAS (photoservice.Proxy), in development the same authentication
	// in this process (photoservice.Local).
	Photos http.Handler
	// Shares serves member shares; production independently confirms sessions.
	Shares http.Handler
	// Family serves /api/v1/family, 家庭中心, and receives the user through
	// accounts.WithUser with the session token; on a NAS the family service
	// confirms the session itself (ADR 0029).
	Family http.Handler
	// Media serves /api/v1/media to every signed-in user and receives the
	// user through accounts.WithUser; the session token stays in the context
	// for the File Broker.
	Media http.Handler
	// Music is the music library; nil answers music_unavailable.
	Music *music.Service
	// Feedback serves /api/v1/feedback, the list every signed-in user reads
	// and edits, and receives the user through accounts.WithUser.
	Feedback http.Handler
	// Transfers serves /api/v1/transfers and the tus endpoint /api/v1/uploads
	// to every signed-in user and receives the user through
	// accounts.WithUser.
	Transfers http.Handler
	// Assistant serves /api/v1/assistant to every signed-in user, each reaching
	// only their own assistant, and receives the user through
	// accounts.WithUser.
	Assistant http.Handler
	// AssistantKeeper, when set, keeps a member's assistant running while
	// they are signed in, so that opening it does not wait for a cold start
	// (ADR 0024).
	AssistantKeeper assistantapi.Keeper
	// SystemLogs reads the allowlisted service logs for administrators; nil
	// answers logs_unavailable.
	SystemLogs systemlog.Reader
	// WebSearch serves the built-in web search (ADR 0030); nil leaves its
	// routes out.
	WebSearch *websearchapi.API
	// Personalization keeps the device-wide 个性化 settings and wallpaper;
	// nil answers personalization_unavailable.
	Personalization *personalization.Store
	// FilesChanged, when set, is told after a Web upload, move, copy, delete
	// or restore changed a space, so indexes over ordinary files (the media
	// center) catch up without waiting for their periodic scan. The context
	// carries the session token.
	FilesChanged func(ctx context.Context, user accounts.User, spaceID string)
	Logger       *slog.Logger
}

// productHandler serves the product API with the dependencies as given.
type productHandler struct {
	ProductDependencies
	state http.Handler
	mux   *http.ServeMux
	// pictures decodes one uploaded avatar or wallpaper at a time.
	pictures *semaphore.Weighted
}

func NewProduct(dependencies ProductDependencies) http.Handler {
	if dependencies.Logger == nil {
		dependencies.Logger = slog.Default()
	}
	state := newStateHandler(dependencies.Reader, dependencies.DataSource, dependencies.ProductVersion, dependencies.Logger)
	if volumes := dependencies.Storage; volumes != nil {
		state.dataVolumeExpected = func() bool { return volumes.DataVolumeFilesystemUUID() != "" }
	}
	handler := &productHandler{ProductDependencies: dependencies, state: state, mux: http.NewServeMux(), pictures: semaphore.NewWeighted(1)}
	handler.register(handler.table())
	return handler
}

func (h *productHandler) handleTerminal(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	if h.Terminal == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "terminal_unavailable", "terminal service is unavailable")
		return
	}
	h.Terminal.ServeHTTP(w, r)
}

func (h *productHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	h.mux.ServeHTTP(w, r)
}

func (h *productHandler) handleSetupStatus(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	if h.Accounts == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "accounts_unavailable", "account service is unavailable")
		return
	}
	required, err := h.Accounts.SetupRequired(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]bool{"setupRequired": required, "setupAllowed": directLoopback(r)})
}

func (h *productHandler) handleSetupAdministrator(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	// Only the device's own screen or an SSH tunnel may claim a device that
	// is not set up yet, so nobody else on the LAN creates its administrator
	// first. The LAN entry always adds forwarding headers (ADR 0012).
	if !directLoopback(r) {
		httpjson.WriteError(w, http.StatusForbidden, "setup_local_only", "the device can only be set up on its own screen or through an SSH tunnel")
		return
	}
	var request struct {
		Username     string `json:"username"`
		Password     string `json:"password"`
		LocalConsole bool   `json:"localConsole"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &request) {
		return
	}
	if _, err := h.Accounts.SetupAdministrator(r.Context(), request.Username, request.Password); err != nil {
		accountsapi.WriteError(w, r, h.Logger, err)
		return
	}
	session, err := h.authenticate(r, request.Username, request.Password, request.LocalConsole)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	h.setSessionCookie(w, session)
	h.keepAssistant(session.User, session.Token)
	httpjson.Write(w, http.StatusCreated, h.publicSession(r.Context(), session))
}

func (h *productHandler) handleCreateSession(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	var request struct {
		Username     string `json:"username"`
		Password     string `json:"password"`
		LocalConsole bool   `json:"localConsole"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &request) {
		return
	}
	session, err := h.authenticate(r, request.Username, request.Password, request.LocalConsole)
	if err != nil {
		httpjson.WriteError(w, http.StatusUnauthorized, "invalid_credentials", "username or password is invalid")
		return
	}
	h.setSessionCookie(w, session)
	h.keepAssistant(session.User, session.Token)
	httpjson.Write(w, http.StatusOK, h.publicSession(r.Context(), session))
}

func (h *productHandler) handleCurrentSession(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	// The page reads its session on every load; renewing the cookie here keeps
	// a local console signed in past the browser's cookie lifetime cap.
	token := accounts.SessionToken(r.Context())
	if session.LocalConsole() {
		session.Token = token
		h.setSessionCookie(w, session)
	}
	// Also after the Product Service restarted, which forgets kept instances.
	h.keepAssistant(session.User, token)
	httpjson.Write(w, http.StatusOK, h.publicSession(r.Context(), session))
}

// keepAssistant has the member's assistant started in the background and kept
// running while this session lasts, so that opening it does not wait for a
// cold start (ADR 0024).
func (h *productHandler) keepAssistant(user accounts.User, sessionToken string) {
	if h.AssistantKeeper != nil {
		h.AssistantKeeper.Keep(user, sessionToken)
	}
}

// authenticate grants a local console session, which never expires on its
// own, only to a direct loopback connection that asked for one. The Kiosk and
// SSH tunnels connect directly; the LAN entry (Caddy, ADR 0012) always adds
// X-Forwarded-For, so LAN browsers get ordinary sessions.
func (h *productHandler) authenticate(r *http.Request, username, password string, localConsole bool) (accounts.Session, error) {
	ctx := withClientOrigin(r)
	if localConsole && directLoopback(r) {
		return h.Accounts.AuthenticateLocalConsole(ctx, username, password)
	}
	return h.Accounts.Authenticate(ctx, username, password)
}

func directLoopback(r *http.Request) bool {
	if r.Header.Get("Forwarded") != "" || r.Header.Get("X-Forwarded-For") != "" || !localorigin.LoopbackHost(r.Host) {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	return err == nil && ip != nil && ip.IsLoopback()
}

func (h *productHandler) handleDeleteSession(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	token := accounts.SessionToken(r.Context())
	_ = h.Accounts.EndSession(withClientOrigin(r), token)
	if h.AssistantKeeper != nil {
		h.AssistantKeeper.Forget(token)
	}
	//nolint:gosec // G124: the LAN entry is plain HTTP (ADR 0012), where a Secure cookie would not be sent.
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

// maxSessionCookies bounds the session cookies tried for one request.
const maxSessionCookies = 4

// clientSession returns the session of the first session cookie that belongs
// to this client. The browser also sends the cookie to installed apps on other
// ports of the device's address, and an app's page can add cookies of the
// same name for longer paths, which the browser then sends first; so a cookie
// alone proves nothing and a foreign one must not hide the user's own
// (issue #161).
func (h *productHandler) clientSession(r *http.Request) (accounts.Session, string, bool) {
	origin := clientOrigin(r)
	tried := 0
	for _, cookie := range r.Cookies() {
		if cookie.Name != sessionCookieName || strings.TrimSpace(cookie.Value) == "" {
			continue
		}
		if tried++; tried > maxSessionCookies {
			break
		}
		session, err := h.Accounts.ResolveClientSession(r.Context(), cookie.Value, origin)
		if err == nil {
			return session, cookie.Value, true
		}
		if errors.Is(err, accounts.ErrSessionElsewhere) {
			h.Logger.WarnContext(r.Context(), "session refused from another client", "via", origin.Via, "address", origin.Address)
		}
	}
	return accounts.Session{}, "", false
}

// maxCookieLifetime is the longest cookie lifetime Chromium honours.
const maxCookieLifetime = 400 * 24 * time.Hour

func (h *productHandler) setSessionCookie(w http.ResponseWriter, session accounts.Session) {
	expires := session.ExpiresAt
	if session.LocalConsole() {
		expires = time.Now().Add(maxCookieLifetime)
	}
	//nolint:gosec // G124: the LAN entry is plain HTTP (ADR 0012), where a Secure cookie would not be sent.
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: session.Token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Expires: expires,
	})
}

type sessionResponse struct {
	CSRFToken string        `json:"csrfToken"`
	ExpiresAt string        `json:"expiresAt"`
	User      accounts.User `json:"user"`
	// CredentialReset is the latest time an administrator chose the user's
	// password; the user keeps seeing it.
	CredentialReset *accounts.CredentialReset `json:"credentialReset,omitempty"`
}

func (h *productHandler) publicSession(ctx context.Context, session accounts.Session) sessionResponse {
	response := sessionResponse{CSRFToken: session.CSRFToken, ExpiresAt: session.ExpiresAt.UTC().Format(timeFormat), User: session.User}
	// A failed lookup only hides the notice; it never fails the sign-in.
	response.CredentialReset, _ = h.Accounts.LastCredentialReset(ctx, session.User)
	return response
}

const timeFormat = "2006-01-02T15:04:05.999999999Z07:00"

func (h *productHandler) internalError(w http.ResponseWriter, r *http.Request, err error) {
	httpjson.WriteInternal(w, r, h.Logger, err)
}
