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
	// photo Policy itself. A *photosapi.Proxy confirms the session in the
	// photo service; any other handler receives the caller as a
	// photos.Principal.
	Photos http.Handler
	// Shares serves member shares; production independently confirms sessions.
	Shares http.Handler
	// Media serves /api/v1/media to every signed-in user and receives the
	// user through mediaapi.WithUser; the session token stays in the context
	// for the File Broker.
	Media http.Handler
	// Music is the music library; nil answers music_unavailable.
	Music *music.Service
	// Feedback serves /api/v1/feedback, the list every signed-in user reads
	// and edits, and receives the user through feedbackapi.WithUser.
	Feedback http.Handler
	// Transfers serves /api/v1/transfers and the tus endpoint /api/v1/uploads
	// to every signed-in user and receives the user through
	// transfersapi.WithUser.
	Transfers http.Handler
	// Assistant serves /api/v1/assistant to every signed-in user, each reaching
	// only their own assistant, and receives the user through
	// assistantapi.WithUser.
	Assistant http.Handler
	// SystemLogs reads the allowlisted service logs for administrators; nil
	// answers logs_unavailable.
	SystemLogs systemlog.Reader
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

type productHandler struct {
	state      http.Handler
	reader     hoststate.Observer
	accounts   *accounts.Service
	files      *files.Service
	previews   *filepreview.Service
	storage    *storage.Service
	terminal   http.Handler
	containers http.Handler
	apps       http.Handler
	shortcuts  http.Handler
	photos     http.Handler
	shares     http.Handler
	media      http.Handler
	music      *music.Service
	feedback   http.Handler
	transfers  http.Handler
	assistant  http.Handler
	systemLogs systemlog.Reader
	dataSource DataSource
	changed    func(context.Context, accounts.User, string)
	logger     *slog.Logger
	mux        *http.ServeMux

	personalization *personalization.Store
	// pictures decodes one uploaded avatar or wallpaper at a time.
	pictures *semaphore.Weighted
}

func NewProduct(dependencies ProductDependencies) http.Handler {
	logger := dependencies.Logger
	if logger == nil {
		logger = slog.Default()
	}
	state := newStateHandler(dependencies.Reader, dependencies.DataSource, dependencies.ProductVersion, logger)
	if volumes := dependencies.Storage; volumes != nil {
		state.dataVolumeExpected = func() bool { return volumes.DataVolumeFilesystemUUID() != "" }
	}
	handler := &productHandler{
		state: state, reader: dependencies.Reader, pictures: semaphore.NewWeighted(1),
		accounts: dependencies.Accounts, files: dependencies.Files, storage: dependencies.Storage,
		previews: dependencies.FilePreviews,
		terminal: dependencies.Terminal, containers: dependencies.Containers, apps: dependencies.Apps, shortcuts: dependencies.AppShortcuts,
		shares: dependencies.Shares, photos: dependencies.Photos, media: dependencies.Media, music: dependencies.Music, feedback: dependencies.Feedback,
		transfers: dependencies.Transfers, assistant: dependencies.Assistant, systemLogs: dependencies.SystemLogs, dataSource: dependencies.DataSource,
		personalization: dependencies.Personalization, changed: dependencies.FilesChanged, logger: logger, mux: http.NewServeMux(),
	}
	handler.register(handler.table())
	return handler
}

func (h *productHandler) handleTerminal(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	if h.terminal == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "terminal_unavailable", "terminal service is unavailable")
		return
	}
	h.terminal.ServeHTTP(w, r)
}

func (h *productHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	h.mux.ServeHTTP(w, r)
}

func (h *productHandler) handleSetupStatus(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	if h.accounts == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "accounts_unavailable", "account service is unavailable")
		return
	}
	required, err := h.accounts.SetupRequired(r.Context())
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
	if _, err := h.accounts.SetupAdministrator(r.Context(), request.Username, request.Password); err != nil {
		accountsapi.WriteError(w, r, h.logger, err)
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
	if keeper, ok := h.assistant.(assistantapi.Keeper); ok {
		keeper.Keep(user, sessionToken)
	}
}

// authenticate grants a local console session, which never expires on its
// own, only to a direct loopback connection that asked for one. The Kiosk and
// SSH tunnels connect directly; the LAN entry (Caddy, ADR 0012) always adds
// X-Forwarded-For, so LAN browsers get ordinary sessions.
func (h *productHandler) authenticate(r *http.Request, username, password string, localConsole bool) (accounts.Session, error) {
	ctx := withClientOrigin(r)
	if localConsole && directLoopback(r) {
		return h.accounts.AuthenticateLocalConsole(ctx, username, password)
	}
	return h.accounts.Authenticate(ctx, username, password)
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
	_ = h.accounts.EndSession(withClientOrigin(r), token)
	if keeper, ok := h.assistant.(assistantapi.Keeper); ok {
		keeper.Forget(token)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func (h *productHandler) handleSpaces(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	spaces, err := h.accounts.ListSpaces(r.Context(), session.User)
	if err != nil {
		accountsapi.WriteError(w, r, h.logger, err)
		return
	}
	type visibleSpace struct {
		accounts.Space
		Writable bool `json:"writable"`
	}
	items := []visibleSpace{}
	for _, space := range spaces {
		writable, err := h.accounts.CanAccessSpace(r.Context(), session.User, space.ID, true)
		if err != nil {
			accountsapi.WriteError(w, r, h.logger, err)
			return
		}
		items = append(items, visibleSpace{Space: space, Writable: writable && space.Viewing == nil})
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": items})
}
func (h *productHandler) handleUsers(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	users, err := h.accounts.ListUsers(r.Context(), session.User)
	if err != nil {
		accountsapi.WriteError(w, r, h.logger, err)
		return
	}
	if users == nil {
		users = []accounts.User{}
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": users})
}

func (h *productHandler) handleCreateUser(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &request) {
		return
	}
	user, err := h.accounts.CreateMember(r.Context(), session.User, request.Username, request.Password)
	if err != nil {
		accountsapi.WriteError(w, r, h.logger, err)
		return
	}
	httpjson.Write(w, http.StatusCreated, user)
}

func (h *productHandler) handleResetUserCredential(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		Password string `json:"password"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &request) {
		return
	}
	if err := h.accounts.ResetCredential(r.Context(), session.User, r.PathValue("userID"), request.Password); err != nil {
		accountsapi.WriteError(w, r, h.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *productHandler) handleDisableUser(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if err := h.accounts.DisableUser(r.Context(), session.User, r.PathValue("userID")); err != nil {
		accountsapi.WriteError(w, r, h.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *productHandler) handleCreateStoragePlan(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	if h.storage == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "storage_unavailable", "storage service is unavailable")
		return
	}
	var request struct {
		DiskIDs []string       `json:"diskIds"`
		Layout  storage.Layout `json:"layout"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &request) {
		return
	}
	plan, err := h.storage.PlanVolume(r.Context(), request.DiskIDs, request.Layout)
	if err != nil {
		h.writeStorageError(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusCreated, plan)
}

func (h *productHandler) handleVolumes(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	if h.storage == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "storage_unavailable", "storage service is unavailable")
		return
	}
	volumes, err := h.storage.ListVolumes(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	if volumes == nil {
		volumes = []storage.Volume{}
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": volumes})
}

// handleContinueConversion starts converting the data volume to its layout
// now, after a shutdown or an error stopped the conversion.
func (h *productHandler) handleContinueConversion(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	if h.storage == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "storage_unavailable", "storage service is unavailable")
		return
	}
	volumes, err := h.storage.ListVolumes(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	if len(volumes) == 0 || volumes[0].ID != r.PathValue("volumeID") {
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "storage resource was not found")
		return
	}
	if err := h.storage.ContinueConversion(r.Context()); err != nil {
		h.writeStorageError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (h *productHandler) handleConfirmStoragePlan(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	if h.storage == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "storage_unavailable", "storage service is unavailable")
		return
	}
	var request struct {
		ConfirmationPhrase string `json:"confirmationPhrase"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &request) {
		return
	}
	plan, err := h.storage.ConfirmPlan(r.Context(), r.PathValue("planID"), request.ConfirmationPhrase)
	if err != nil {
		h.writeStorageError(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, plan)
}

func (h *productHandler) handleExecuteStoragePlan(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	if h.storage == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "storage_unavailable", "storage service is unavailable")
		return
	}
	plan, err := h.storage.ExecutePlan(r.Context(), r.PathValue("planID"))
	if err != nil {
		h.writeStorageError(w, r, err)
		return
	}
	// The Host Agent goes on with a running plan after this response;
	// GET /api/v1/operations/{planId} follows it.
	status := http.StatusOK
	if plan.State == storage.PlanStateRunning {
		status = http.StatusAccepted
	}
	httpjson.Write(w, status, plan)
}

// handleUnfinishedStoragePlans lists the plans that still need the
// administrator, so that the settings page finds them again after a reload.
func (h *productHandler) handleUnfinishedStoragePlans(w http.ResponseWriter, _ *http.Request, _ accounts.Session) {
	if h.storage == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "storage_unavailable", "storage service is unavailable")
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": h.storage.UnfinishedPlans()})
}

func (h *productHandler) handleCancelStoragePlan(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	if h.storage == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "storage_unavailable", "storage service is unavailable")
		return
	}
	plan, err := h.storage.CancelPlan(r.Context(), r.PathValue("planID"))
	if err != nil {
		h.writeStorageError(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, plan)
}

func (h *productHandler) handleOperation(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	if h.storage == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "storage_unavailable", "storage service is unavailable")
		return
	}
	plan, err := h.storage.GetPlan(r.PathValue("operationID"))
	if err != nil {
		h.writeStorageError(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, plan)
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
		session, err := h.accounts.ResolveClientSession(r.Context(), cookie.Value, origin)
		if err == nil {
			return session, cookie.Value, true
		}
		if errors.Is(err, accounts.ErrSessionElsewhere) {
			h.logger.WarnContext(r.Context(), "session refused from another client", "via", origin.Via, "address", origin.Address)
		}
	}
	return accounts.Session{}, "", false
}

func (h *productHandler) handleChangePassword(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &request) {
		return
	}
	if err := h.accounts.ChangePassword(r.Context(), session.User, request.CurrentPassword, request.NewPassword); err != nil {
		accountsapi.WriteError(w, r, h.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *productHandler) handleNotifications(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	notifications, err := h.accounts.Notifications(r.Context(), session.User)
	if err != nil {
		accountsapi.WriteError(w, r, h.logger, err)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": notifications})
}

func (h *productHandler) handleAcknowledgeNotification(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if err := h.accounts.AcknowledgeNotification(r.Context(), session.User, r.PathValue("notificationID")); err != nil {
		accountsapi.WriteError(w, r, h.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *productHandler) handleStartViewing(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		Password string                `json:"password"`
		Reason   string                `json:"reason"`
		Scope    accounts.ViewingScope `json:"scope"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &request) {
		return
	}
	start := h.accounts.StartViewing
	switch request.Scope {
	case "", accounts.ViewingScopeSpace:
	case accounts.ViewingScopeLibrary:
		start = h.accounts.StartLibraryViewing
	default:
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "scope must be space or library")
		return
	}
	grant, err := start(r.Context(), session.User, r.PathValue("userID"), request.Password, request.Reason)
	if err != nil {
		accountsapi.WriteError(w, r, h.logger, err)
		return
	}
	httpjson.Write(w, http.StatusCreated, grant)
}

func (h *productHandler) handleEndViewing(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if err := h.accounts.EndViewing(r.Context(), session.User, r.PathValue("grantID")); err != nil {
		accountsapi.WriteError(w, r, h.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// maxCookieLifetime is the longest cookie lifetime Chromium honours.
const maxCookieLifetime = 400 * 24 * time.Hour

func (h *productHandler) setSessionCookie(w http.ResponseWriter, session accounts.Session) {
	expires := session.ExpiresAt
	if session.LocalConsole() {
		expires = time.Now().Add(maxCookieLifetime)
	}
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
	response.CredentialReset, _ = h.accounts.LastCredentialReset(ctx, session.User)
	return response
}

const timeFormat = "2006-01-02T15:04:05.999999999Z07:00"

func (h *productHandler) writeStorageError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, storage.ErrDiskNotFound), errors.Is(err, storage.ErrPlanNotFound):
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "storage resource was not found")
	case errors.Is(err, storage.ErrDiskNotEligible):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "disk_not_eligible", err.Error())
	case errors.Is(err, storage.ErrConfirmation):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "confirmation_mismatch", err.Error())
	case errors.Is(err, storage.ErrPlanExpired):
		httpjson.WriteError(w, http.StatusGone, "plan_expired", err.Error())
	case errors.Is(err, storage.ErrPlanState):
		httpjson.WriteError(w, http.StatusConflict, "plan_state_conflict", err.Error())
	case errors.Is(err, storage.ErrVolumeUnavailable):
		httpjson.WriteError(w, http.StatusConflict, "volume_unavailable", err.Error())
	case errors.Is(err, storage.ErrVolumeBusy):
		httpjson.WriteError(w, http.StatusConflict, "volume_busy", err.Error())
	case errors.Is(err, storage.ErrVolumeExists):
		httpjson.WriteError(w, http.StatusConflict, "volume_exists", err.Error())
	case errors.Is(err, storage.ErrNoConversion):
		httpjson.WriteError(w, http.StatusConflict, "no_conversion", err.Error())
	case errors.Is(err, storage.ErrInvalidLayout):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "invalid_layout", err.Error())
	case errors.Is(err, storage.ErrLayoutTooSmall):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "layout_too_small", err.Error())
	case errors.Is(err, storage.ErrOperationAttention), errors.Is(err, storage.ErrDiskChanged):
		httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_needs_attention", err.Error())
	default:
		h.internalError(w, r, err)
	}
}

func (h *productHandler) internalError(w http.ResponseWriter, r *http.Request, err error) {
	h.logger.ErrorContext(r.Context(), "product request failed", "path", r.URL.Path, "error", err)
	httpjson.WriteError(w, http.StatusInternalServerError, "internal_error", "request could not be completed")
}
