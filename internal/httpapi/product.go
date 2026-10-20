package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/appstoreapi"
	"github.com/zhongwater123/A-NAS/internal/assistantapi"
	"github.com/zhongwater123/A-NAS/internal/feedbackapi"
	"github.com/zhongwater123/A-NAS/internal/filepreview"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/hoststate"
	"github.com/zhongwater123/A-NAS/internal/localorigin"
	"github.com/zhongwater123/A-NAS/internal/mediaapi"
	"github.com/zhongwater123/A-NAS/internal/music"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
	"github.com/zhongwater123/A-NAS/internal/sharesapi"
	"github.com/zhongwater123/A-NAS/internal/storage"
	"github.com/zhongwater123/A-NAS/internal/systemlog"
	"github.com/zhongwater123/A-NAS/internal/transfersapi"
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
	// FilesChanged, when set, is told after a Web upload, move, copy, delete
	// or restore changed a space, so indexes over ordinary files (the media
	// center) catch up without waiting for their periodic scan. The context
	// carries the session token.
	FilesChanged func(ctx context.Context, user accounts.User, spaceID string)
	Logger       *slog.Logger
}

type productHandler struct {
	state      http.Handler
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
		state:    state,
		accounts: dependencies.Accounts, files: dependencies.Files, storage: dependencies.Storage,
		previews: dependencies.FilePreviews,
		terminal: dependencies.Terminal, containers: dependencies.Containers, apps: dependencies.Apps, shortcuts: dependencies.AppShortcuts,
		shares: dependencies.Shares, photos: dependencies.Photos, media: dependencies.Media, music: dependencies.Music, feedback: dependencies.Feedback,
		transfers: dependencies.Transfers, assistant: dependencies.Assistant, systemLogs: dependencies.SystemLogs, dataSource: dependencies.DataSource,
		changed: dependencies.FilesChanged, logger: logger, mux: http.NewServeMux(),
	}
	handler.routes()
	return handler
}

func (h *productHandler) routes() {
	h.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { h.state.ServeHTTP(w, r) })
	for prefix, handler := range map[string]http.Handler{"/api/v1/containers": h.containers, "/api/v1/apps": h.apps} {
		if handler != nil {
			h.mux.Handle(prefix, h.withAdministrator(handler))
			h.mux.Handle(prefix+"/", h.withAdministrator(handler))
		}
	}
	if h.shortcuts != nil {
		serve := h.withSession(func(w http.ResponseWriter, r *http.Request, _ accounts.Session) { h.shortcuts.ServeHTTP(w, r) })
		h.mux.Handle(appstoreapi.ShortcutsPrefix, serve)
		h.mux.Handle(appstoreapi.ShortcutsPrefix+"/", serve)
	}
	if h.shares != nil {
		h.mux.Handle(sharesapi.PathPrefix, h.withUser(h.shares, sharesapi.WithUser))
		h.mux.Handle(sharesapi.PathPrefix+"/", h.withUser(h.shares, sharesapi.WithUser))
	}
	if h.photos != nil {
		h.mux.Handle(photosapi.PathPrefix+"/", h.withPhotoPrincipal(h.photos))
	}
	if h.media != nil {
		h.mux.Handle(mediaapi.PathPrefix+"/", h.withUser(h.media, mediaapi.WithUser))
	}
	if h.feedback != nil {
		h.mux.Handle(feedbackapi.PathPrefix, h.withUser(h.feedback, feedbackapi.WithUser))
		h.mux.Handle(feedbackapi.PathPrefix+"/", h.withUser(h.feedback, feedbackapi.WithUser))
	}
	if h.assistant != nil {
		h.mux.Handle(assistantapi.PathPrefix+"/", h.withUser(h.assistant, assistantapi.WithUser))
	}
	if h.transfers != nil {
		for _, prefix := range []string{transfersapi.TransfersPath, transfersapi.UploadsPath} {
			h.mux.Handle(prefix, h.withUser(h.transfers, transfersapi.WithUser))
			h.mux.Handle(prefix+"/", h.withUser(h.transfers, transfersapi.WithUser))
		}
	}
	h.mux.HandleFunc("GET /api/v1/setup/status", h.handleSetupStatus)
	h.mux.HandleFunc("POST /api/v1/setup/admin", h.handleSetupAdministrator)
	h.mux.HandleFunc("POST /api/v1/session", h.handleCreateSession)
	h.mux.HandleFunc("GET /api/v1/session", h.withSession(h.handleCurrentSession))
	h.mux.HandleFunc("DELETE /api/v1/session", h.withMutation(h.handleDeleteSession))
	h.mux.HandleFunc("POST /api/v1/session/password", h.withMutation(h.handleChangePassword))
	h.mux.HandleFunc("GET /api/v1/notifications", h.withSession(h.handleNotifications))
	h.mux.HandleFunc("POST /api/v1/notifications/{notificationID}/acknowledge", h.withMutation(h.handleAcknowledgeNotification))
	h.mux.HandleFunc("POST /api/v1/users/{userID}/viewing", h.withMutation(h.handleStartViewing))
	h.mux.HandleFunc("DELETE /api/v1/viewing/{grantID}", h.withMutation(h.handleEndViewing))
	h.mux.HandleFunc("GET /api/v1/terminal", h.withSession(h.handleTerminal))
	h.mux.HandleFunc("GET /api/v1/terminal/session", h.withSession(h.handleTerminal))
	for _, path := range []string{"/api/v1/system", "/api/v1/disks", "/api/v1/host-state", "/api/v1/metrics"} {
		h.mux.HandleFunc("GET "+path, h.withSession(func(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
			h.state.ServeHTTP(w, r)
		}))
	}
	// The task manager is read-only and available to members as well as admins.
	// Register the path without a method so the state handler returns JSON 405
	// after authentication instead of falling through the catch-all 404.
	for _, path := range []string{"/api/v1/task-manager/device", "/api/v1/task-manager/tasks"} {
		h.mux.HandleFunc(path, h.withSession(func(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
			h.state.ServeHTTP(w, r)
		}))
	}
	h.mux.HandleFunc("GET /api/v1/spaces", h.withSession(h.handleSpaces))
	h.mux.HandleFunc("GET /api/v1/spaces/{spaceID}/entries", h.withSession(h.handleEntries))
	h.mux.HandleFunc("POST /api/v1/spaces/{spaceID}/directories", h.withMutation(h.handleCreateDirectory))
	h.mux.HandleFunc("POST /api/v1/spaces/{spaceID}/uploads", h.withMutation(h.handleUpload))
	h.mux.HandleFunc("GET /api/v1/files/{fileID}/content", h.withSession(h.handleDownload))
	h.mux.HandleFunc("GET /api/v1/files/{fileID}/preview", h.withSession(h.handleFilePreview))
	h.mux.HandleFunc("GET /api/v1/files/{fileID}/versions", h.withSession(h.handleFileVersions))
	h.mux.HandleFunc("PATCH /api/v1/files/{fileID}", h.withMutation(h.handleMoveFile))
	h.mux.HandleFunc("POST /api/v1/files/{fileID}/copies", h.withMutation(h.handleCopyFile))
	h.mux.HandleFunc("DELETE /api/v1/files/{fileID}", h.withMutation(h.handleDeleteFile))
	h.mux.HandleFunc("GET /api/v1/trash", h.withSession(h.handleTrash))
	h.mux.HandleFunc("POST /api/v1/trash/{trashID}/restore", h.withMutation(h.handleRestoreTrash))
	h.mux.HandleFunc("DELETE /api/v1/trash/{trashID}", h.withMutation(h.handlePurgeTrash))
	h.mux.HandleFunc("GET /api/v1/spaces/{spaceID}/snapshots", h.withSession(h.handleSnapshots))
	h.mux.HandleFunc("POST /api/v1/spaces/{spaceID}/snapshots", h.withMutation(h.handleCreateSnapshot))
	h.mux.HandleFunc("GET /api/v1/spaces/{spaceID}/snapshot-schedule", h.withSession(h.handleSnapshotSchedule))
	h.mux.HandleFunc("PUT /api/v1/spaces/{spaceID}/snapshot-schedule", h.withMutation(h.handleUpdateSnapshotSchedule))
	h.mux.HandleFunc("GET /api/v1/spaces/{spaceID}/snapshot-search", h.withSession(h.handleSearchSnapshots))
	h.mux.HandleFunc("GET /api/v1/spaces/{spaceID}/removed-entries", h.withSession(h.handleRemovedEntries))
	h.mux.HandleFunc("GET /api/v1/snapshots/{snapshotID}/entries", h.withSession(h.handleSnapshotEntries))
	h.mux.HandleFunc("GET /api/v1/snapshots/{snapshotID}/entries/{entryID}/content", h.withSession(h.handleSnapshotContent))
	h.mux.HandleFunc("POST /api/v1/snapshots/{snapshotID}/entries/{entryID}/restore", h.withMutation(h.handleRestoreSnapshotEntry))
	h.mux.HandleFunc("POST /api/v1/snapshots/{snapshotID}/restore", h.withMutation(h.handleRestoreSnapshot))
	h.mux.HandleFunc("POST /api/v1/snapshots/{snapshotID}/keep", h.withMutation(h.handleKeepSnapshot))
	h.mux.HandleFunc("DELETE /api/v1/snapshots/{snapshotID}", h.withMutation(h.handleDeleteSnapshot))
	h.mux.HandleFunc("GET /api/v1/users", h.withSession(h.handleUsers))
	h.mux.HandleFunc("POST /api/v1/users", h.withMutation(h.handleCreateUser))
	h.mux.HandleFunc("PATCH /api/v1/users/{userID}/credential", h.withMutation(h.handleResetUserCredential))
	h.mux.HandleFunc("DELETE /api/v1/users/{userID}", h.withMutation(h.handleDisableUser))
	h.mux.HandleFunc("GET /api/v1/volumes", h.withSession(h.handleVolumes))
	h.mux.HandleFunc("POST /api/v1/storage/plans", h.withMutation(h.handleCreateStoragePlan))
	h.mux.HandleFunc("POST /api/v1/volumes/{volumeID}/conversion", h.withMutation(h.handleContinueConversion))
	h.mux.HandleFunc("POST /api/v1/storage/plans/{planID}/confirm", h.withMutation(h.handleConfirmStoragePlan))
	h.mux.HandleFunc("POST /api/v1/storage/plans/{planID}/execute", h.withMutation(h.handleExecuteStoragePlan))
	h.mux.HandleFunc("GET /api/v1/operations/{operationID}", h.withSession(h.handleOperation))
	h.mux.HandleFunc("GET /api/v1/audit", h.withSession(h.handleAudit))
	h.mux.HandleFunc("GET /api/v1/activity", h.withSession(h.handleActivity))
	h.mux.HandleFunc("GET /api/v1/system-logs", h.withSession(h.handleSystemLogs))
	h.musicRoutes()
	h.profileRoutes()
	h.mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	})
}

func (h *productHandler) handleTerminal(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if !h.requireAdministrator(w, session) {
		return
	}
	if h.terminal == nil {
		writeError(w, http.StatusServiceUnavailable, "terminal_unavailable", "terminal service is unavailable")
		return
	}
	h.terminal.ServeHTTP(w, r)
}

func (h *productHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	h.mux.ServeHTTP(w, r)
}

func (h *productHandler) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	if h.accounts == nil {
		writeError(w, http.StatusServiceUnavailable, "accounts_unavailable", "account service is unavailable")
		return
	}
	required, err := h.accounts.SetupRequired(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"setupRequired": required, "setupAllowed": directLoopback(r)})
}

func (h *productHandler) handleSetupAdministrator(w http.ResponseWriter, r *http.Request) {
	// Only the device's own screen or an SSH tunnel may claim a device that
	// is not set up yet, so nobody else on the LAN creates its administrator
	// first. The LAN entry always adds forwarding headers (ADR 0012).
	if !directLoopback(r) {
		writeError(w, http.StatusForbidden, "setup_local_only", "the device can only be set up on its own screen or through an SSH tunnel")
		return
	}
	var request struct {
		Username     string `json:"username"`
		Password     string `json:"password"`
		LocalConsole bool   `json:"localConsole"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	if _, err := h.accounts.SetupAdministrator(r.Context(), request.Username, request.Password); err != nil {
		h.writeAccountError(w, r, err)
		return
	}
	session, err := h.authenticate(r, request.Username, request.Password, request.LocalConsole)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	h.setSessionCookie(w, session)
	h.keepAssistant(session.User, session.Token)
	writeJSON(w, http.StatusCreated, h.publicSession(r.Context(), session))
}

func (h *productHandler) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Username     string `json:"username"`
		Password     string `json:"password"`
		LocalConsole bool   `json:"localConsole"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	session, err := h.authenticate(r, request.Username, request.Password, request.LocalConsole)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "username or password is invalid")
		return
	}
	h.setSessionCookie(w, session)
	h.keepAssistant(session.User, session.Token)
	writeJSON(w, http.StatusOK, h.publicSession(r.Context(), session))
}

func (h *productHandler) handleCurrentSession(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	// The page reads its session on every load; renewing the cookie here keeps
	// a local console signed in past the browser's cookie lifetime cap.
	cookie, err := r.Cookie(sessionCookieName)
	if err == nil && session.LocalConsole() {
		session.Token = cookie.Value
		h.setSessionCookie(w, session)
	}
	if err == nil {
		// Also after the Product Service restarted, which forgets kept instances.
		h.keepAssistant(session.User, cookie.Value)
	}
	writeJSON(w, http.StatusOK, h.publicSession(r.Context(), session))
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
	cookie, _ := r.Cookie(sessionCookieName)
	if cookie != nil {
		_ = h.accounts.EndSession(withClientOrigin(r), cookie.Value)
		if keeper, ok := h.assistant.(assistantapi.Keeper); ok {
			keeper.Forget(cookie.Value)
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func (h *productHandler) handleSpaces(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	spaces, err := h.accounts.ListSpaces(r.Context(), session.User)
	if err != nil {
		h.writeAccountError(w, r, err)
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
			h.writeAccountError(w, r, err)
			return
		}
		items = append(items, visibleSpace{Space: space, Writable: writable && space.Viewing == nil})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
func (h *productHandler) handleEntries(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if h.files == nil {
		writeError(w, http.StatusServiceUnavailable, "files_unavailable", "file service is unavailable")
		return
	}
	entries, err := h.files.List(r.Context(), session.User, r.PathValue("spaceID"), r.URL.Query().Get("parentId"))
	if err != nil {
		h.writeFileError(w, r, err)
		return
	}
	if entries == nil {
		entries = []files.Entry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": entries})
}

func (h *productHandler) handleCreateDirectory(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if h.files == nil {
		writeError(w, http.StatusServiceUnavailable, "files_unavailable", "file service is unavailable")
		return
	}
	var request struct {
		ParentID string `json:"parentId"`
		Name     string `json:"name"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	entry, err := h.files.CreateDirectory(r.Context(), session.User, r.PathValue("spaceID"), request.ParentID, request.Name)
	if err != nil {
		h.writeFileError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, entry)
}

func (h *productHandler) handleUpload(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if h.files == nil {
		writeError(w, http.StatusServiceUnavailable, "files_unavailable", "file service is unavailable")
		return
	}
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_multipart", "multipart upload is invalid")
		return
	}
	var parentID string
	for {
		part, nextErr := reader.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			writeError(w, http.StatusBadRequest, "invalid_multipart", "multipart upload is invalid")
			return
		}
		switch part.FormName() {
		case "parentId":
			value, readErr := io.ReadAll(io.LimitReader(part, 4097))
			_ = part.Close()
			if readErr != nil || len(value) > 4096 {
				writeError(w, http.StatusBadRequest, "invalid_parent", "parent ID is invalid")
				return
			}
			parentID = string(value)
		case "file":
			if part.FileName() == "" {
				_ = part.Close()
				continue
			}
			entry, uploadErr := h.files.Upload(r.Context(), session.User, r.PathValue("spaceID"), parentID, part.FileName(), part)
			_ = part.Close()
			if uploadErr != nil {
				h.writeFileError(w, r, uploadErr)
				return
			}
			h.filesChanged(r, session, entry.SpaceID)
			writeJSON(w, http.StatusCreated, entry)
			return
		default:
			_ = part.Close()
		}
	}
	writeError(w, http.StatusBadRequest, "file_required", "one file part is required")
}

func (h *productHandler) handleDownload(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if h.files == nil {
		writeError(w, http.StatusServiceUnavailable, "files_unavailable", "file service is unavailable")
		return
	}
	content, err := h.files.OpenContent(r.Context(), session.User, r.PathValue("fileID"))
	if err != nil {
		h.writeFileError(w, r, err)
		return
	}
	defer content.Reader.Close()
	disposition := "attachment"
	if r.URL.Query().Get("disposition") == "inline" && browserPreviewableContent(content.Name) {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": content.Name}))
	http.ServeContent(w, r, content.Name, content.ModifiedAt, content.Reader)
}

func browserPreviewableContent(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, suffix := range []string{".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".avif", ".pdf"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

func (h *productHandler) handleMoveFile(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		SpaceID  string `json:"spaceId"`
		ParentID string `json:"parentId"`
		Name     string `json:"name"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	// A move to another space changes both spaces.
	var from string
	if request.SpaceID != "" {
		if source, _, err := h.files.Lookup(r.Context(), session.User, r.PathValue("fileID")); err == nil {
			from = source.SpaceID
		}
	}
	entry, err := h.files.Move(r.Context(), session.User, r.PathValue("fileID"), request.SpaceID, request.ParentID, request.Name)
	if err != nil {
		h.writeFileError(w, r, err)
		return
	}
	if from != entry.SpaceID {
		h.filesChanged(r, session, from)
	}
	h.filesChanged(r, session, entry.SpaceID)
	writeJSON(w, http.StatusOK, entry)
}

func (h *productHandler) handleCopyFile(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		SpaceID  string `json:"spaceId"`
		ParentID string `json:"parentId"`
		Name     string `json:"name"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	entry, err := h.files.Copy(r.Context(), session.User, r.PathValue("fileID"), request.SpaceID, request.ParentID, request.Name)
	if err != nil {
		h.writeFileError(w, r, err)
		return
	}
	h.filesChanged(r, session, entry.SpaceID)
	writeJSON(w, http.StatusCreated, entry)
}

func (h *productHandler) handleDeleteFile(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	item, err := h.files.DeleteByID(r.Context(), session.User, r.PathValue("fileID"))
	if err != nil {
		h.writeFileError(w, r, err)
		return
	}
	h.filesChanged(r, session, item.SpaceID)
	writeJSON(w, http.StatusOK, item)
}

// filesChanged tells FilesChanged, if set, that a Web operation changed a space.
func (h *productHandler) filesChanged(r *http.Request, session accounts.Session, spaceID string) {
	if h.changed != nil && spaceID != "" {
		h.changed(r.Context(), session.User, spaceID)
	}
}

func (h *productHandler) handleTrash(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	items, err := h.files.ListTrash(r.Context(), session.User)
	if err != nil {
		h.writeFileError(w, r, err)
		return
	}
	if items == nil {
		items = []files.TrashItem{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *productHandler) handleRestoreTrash(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		ParentID string `json:"parentId"`
		Name     string `json:"name"`
		// Conflict is ask (the default), rename or replace, as for uploads.
		Conflict files.ConflictPolicy `json:"conflict"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	switch request.Conflict {
	case "", files.ConflictAsk, files.ConflictRename, files.ConflictReplace:
	default:
		writeError(w, http.StatusBadRequest, "invalid_request", "conflict must be ask, rename or replace")
		return
	}
	restored, err := h.files.Restore(r.Context(), session.User, r.PathValue("trashID"), request.ParentID, request.Name, request.Conflict)
	if err != nil {
		h.writeFileError(w, r, err)
		return
	}
	h.filesChanged(r, session, restored.SpaceID)
	writeJSON(w, http.StatusOK, restored)
}

func (h *productHandler) handlePurgeTrash(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if err := h.files.Purge(r.Context(), session.User, r.PathValue("trashID")); err != nil {
		h.writeFileError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *productHandler) handleSnapshots(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	items, err := h.files.ListSnapshots(r.Context(), session.User, r.PathValue("spaceID"))
	if err != nil {
		h.writeFileError(w, r, err)
		return
	}
	if items == nil {
		items = []files.Snapshot{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *productHandler) handleCreateSnapshot(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	snapshot, err := h.files.CreateSnapshot(r.Context(), session.User, r.PathValue("spaceID"), request.Name)
	if err != nil {
		h.writeFileError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, snapshot)
}

func (h *productHandler) handleSnapshotEntries(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	items, err := h.files.ListSnapshotEntries(r.Context(), session.User, r.PathValue("snapshotID"), r.URL.Query().Get("parentId"))
	if err != nil {
		h.writeFileError(w, r, err)
		return
	}
	if items == nil {
		items = []files.SnapshotEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *productHandler) handleSnapshotContent(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	content, err := h.files.OpenSnapshotContent(r.Context(), session.User, r.PathValue("snapshotID"), r.PathValue("entryID"))
	if err != nil {
		h.writeFileError(w, r, err)
		return
	}
	defer content.Reader.Close()
	// The Host Agent streams snapshot objects, so downloads cannot resume.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(content.SizeBytes, 10))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": content.Name}))
	if !content.ModifiedAt.IsZero() {
		w.Header().Set("Last-Modified", content.ModifiedAt.UTC().Format(http.TimeFormat))
	}
	if _, err := io.Copy(w, content.Reader); err != nil {
		h.logger.WarnContext(r.Context(), "snapshot download interrupted", "snapshot", r.PathValue("snapshotID"), "error", err)
	}
}

func (h *productHandler) handleSearchSnapshots(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	items, truncated, err := h.files.SearchSnapshots(r.Context(), session.User, r.PathValue("spaceID"), r.URL.Query().Get("q"))
	if err != nil {
		h.writeFileError(w, r, err)
		return
	}
	if items == nil {
		items = []files.SnapshotMatch{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "truncated": truncated})
}

func (h *productHandler) handleRemovedEntries(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	items, truncated, err := h.files.RemovedEntries(r.Context(), session.User, r.PathValue("spaceID"))
	if err != nil {
		h.writeFileError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "truncated": truncated})
}

func (h *productHandler) handleFileVersions(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	match, err := h.files.EntryVersions(r.Context(), session.User, r.PathValue("fileID"))
	if err != nil {
		h.writeFileError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, match)
}

func (h *productHandler) handleSnapshotSchedule(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	schedule, err := h.files.SnapshotSchedule(r.Context(), session.User, r.PathValue("spaceID"))
	if err != nil {
		h.writeFileError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, schedule)
}

func (h *productHandler) handleUpdateSnapshotSchedule(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request files.SnapshotScheduleSettings
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	schedule, err := h.files.UpdateSnapshotSchedule(r.Context(), session.User, r.PathValue("spaceID"), request)
	if err != nil {
		h.writeFileError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, schedule)
}

func (h *productHandler) handleKeepSnapshot(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	snapshot, err := h.files.KeepSnapshot(r.Context(), session.User, r.PathValue("snapshotID"))
	if err != nil {
		h.writeFileError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (h *productHandler) handleRestoreSnapshotEntry(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		ParentID string `json:"parentId"`
		Name     string `json:"name"`
		Replace  bool   `json:"replace"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	entry, err := h.files.RestoreSnapshotEntry(r.Context(), session.User, r.PathValue("snapshotID"), r.PathValue("entryID"),
		request.ParentID, request.Name, request.Replace)
	if err != nil {
		h.writeFileError(w, r, err)
		return
	}
	h.filesChanged(r, session, entry.SpaceID)
	writeJSON(w, http.StatusCreated, entry)
}

func (h *productHandler) handleRestoreSnapshot(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		ParentID string `json:"parentId"`
		Name     string `json:"name"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	entry, err := h.files.RestoreSnapshot(r.Context(), session.User, r.PathValue("snapshotID"), request.ParentID, request.Name)
	if err != nil {
		h.writeFileError(w, r, err)
		return
	}
	h.filesChanged(r, session, entry.SpaceID)
	writeJSON(w, http.StatusCreated, entry)
}

func (h *productHandler) handleDeleteSnapshot(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if err := h.files.DeleteSnapshot(r.Context(), session.User, r.PathValue("snapshotID")); err != nil {
		h.writeFileError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *productHandler) handleUsers(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	users, err := h.accounts.ListUsers(r.Context(), session.User)
	if err != nil {
		h.writeAccountError(w, r, err)
		return
	}
	if users == nil {
		users = []accounts.User{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": users})
}

func (h *productHandler) handleCreateUser(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	user, err := h.accounts.CreateMember(r.Context(), session.User, request.Username, request.Password)
	if err != nil {
		h.writeAccountError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, user)
}

func (h *productHandler) handleResetUserCredential(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	if err := h.accounts.ResetCredential(r.Context(), session.User, r.PathValue("userID"), request.Password); err != nil {
		h.writeAccountError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *productHandler) handleDisableUser(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if err := h.accounts.DisableUser(r.Context(), session.User, r.PathValue("userID")); err != nil {
		h.writeAccountError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *productHandler) handleCreateStoragePlan(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if !h.requireAdministrator(w, session) {
		return
	}
	if h.storage == nil {
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "storage service is unavailable")
		return
	}
	var request struct {
		DiskIDs []string       `json:"diskIds"`
		Layout  storage.Layout `json:"layout"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	plan, err := h.storage.PlanVolume(r.Context(), request.DiskIDs, request.Layout)
	if err != nil {
		h.writeStorageError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, plan)
}

func (h *productHandler) handleVolumes(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	if h.storage == nil {
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "storage service is unavailable")
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
	writeJSON(w, http.StatusOK, map[string]any{"items": volumes})
}

// handleContinueConversion starts converting the data volume to its layout
// now, after a shutdown or an error stopped the conversion.
func (h *productHandler) handleContinueConversion(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if !h.requireAdministrator(w, session) {
		return
	}
	if h.storage == nil {
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "storage service is unavailable")
		return
	}
	volumes, err := h.storage.ListVolumes(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	if len(volumes) == 0 || volumes[0].ID != r.PathValue("volumeID") {
		writeError(w, http.StatusNotFound, "not_found", "storage resource was not found")
		return
	}
	if err := h.storage.ContinueConversion(r.Context()); err != nil {
		h.writeStorageError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (h *productHandler) handleConfirmStoragePlan(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if !h.requireAdministrator(w, session) {
		return
	}
	if h.storage == nil {
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "storage service is unavailable")
		return
	}
	var request struct {
		ConfirmationPhrase string `json:"confirmationPhrase"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	plan, err := h.storage.ConfirmPlan(r.Context(), r.PathValue("planID"), request.ConfirmationPhrase)
	if err != nil {
		h.writeStorageError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (h *productHandler) handleExecuteStoragePlan(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if !h.requireAdministrator(w, session) {
		return
	}
	if h.storage == nil {
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "storage service is unavailable")
		return
	}
	plan, err := h.storage.ExecutePlan(r.Context(), r.PathValue("planID"))
	if err != nil {
		h.writeStorageError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (h *productHandler) handleOperation(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if !h.requireAdministrator(w, session) {
		return
	}
	if h.storage == nil {
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "storage service is unavailable")
		return
	}
	plan, err := h.storage.GetPlan(r.PathValue("operationID"))
	if err != nil {
		h.writeStorageError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

type auditRecord struct {
	ID           string    `json:"id"`
	ActorUserID  string    `json:"actorUserId,omitempty"`
	Action       string    `json:"action"`
	ResourceType string    `json:"resourceType"`
	ResourceID   string    `json:"resourceId"`
	OccurredAt   time.Time `json:"occurredAt"`
	Detail       string    `json:"detail,omitempty"`
}

func (h *productHandler) handleAudit(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if !h.requireAdministrator(w, session) {
		return
	}
	accountEvents, err := h.accounts.ListAudit(r.Context(), session.User)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	var records []auditRecord
	for _, event := range accountEvents {
		records = append(records, auditRecord(event))
	}
	if h.files != nil {
		fileEvents, err := h.files.ListAudit(r.Context(), session.User)
		if err != nil {
			h.internalError(w, r, err)
			return
		}
		for _, event := range fileEvents {
			records = append(records, auditRecord(event))
		}
	}
	if h.storage != nil {
		storageEvents, err := h.storage.ListAudit(r.Context())
		if err != nil {
			h.internalError(w, r, err)
			return
		}
		for _, event := range storageEvents {
			records = append(records, auditRecord(event))
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].OccurredAt.After(records[j].OccurredAt) })
	if records == nil {
		records = []auditRecord{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": records})
}

func (h *productHandler) requireAdministrator(w http.ResponseWriter, session accounts.Session) bool {
	if session.User.Role != accounts.RoleAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "administrator permission is required")
		return false
	}
	return true
}

func (h *productHandler) withSession(next func(http.ResponseWriter, *http.Request, accounts.Session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.accounts == nil {
			writeError(w, http.StatusServiceUnavailable, "accounts_unavailable", "account service is unavailable")
			return
		}
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || strings.TrimSpace(cookie.Value) == "" {
			writeError(w, http.StatusUnauthorized, "authentication_required", "authentication is required")
			return
		}
		session, err := h.accounts.ResolveSession(r.Context(), cookie.Value)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "authentication_required", "authentication is required")
			return
		}
		if session.User.MustChangePassword && !allowedDuringPasswordChange(r) {
			writeError(w, http.StatusForbidden, "password_change_required", "choose a new password before continuing")
			return
		}
		// The File Broker verifies the token itself before acting as the user.
		next(w, r.WithContext(accounts.WithSessionToken(r.Context(), cookie.Value)), session)
	}
}

// withAdministrator admits only administrators, and requires the CSRF token
// for anything but reads.
func (h *productHandler) withAdministrator(next http.Handler) http.HandlerFunc {
	serve := func(w http.ResponseWriter, r *http.Request, session accounts.Session) {
		if session.User.Role != accounts.RoleAdmin {
			writeError(w, http.StatusForbidden, "forbidden", "only administrators manage applications")
			return
		}
		next.ServeHTTP(w, r)
	}
	read, write := h.withSession(serve), h.withMutation(serve)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			read(w, r)
			return
		}
		write(w, r)
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

func (h *productHandler) handleChangePassword(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	if err := h.accounts.ChangePassword(r.Context(), session.User, request.CurrentPassword, request.NewPassword); err != nil {
		h.writeAccountError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *productHandler) handleNotifications(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	notifications, err := h.accounts.Notifications(r.Context(), session.User)
	if err != nil {
		h.writeAccountError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": notifications})
}

func (h *productHandler) handleAcknowledgeNotification(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if err := h.accounts.AcknowledgeNotification(r.Context(), session.User, r.PathValue("notificationID")); err != nil {
		h.writeAccountError(w, r, err)
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
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	start := h.accounts.StartViewing
	switch request.Scope {
	case "", accounts.ViewingScopeSpace:
	case accounts.ViewingScopeLibrary:
		start = h.accounts.StartLibraryViewing
	default:
		writeError(w, http.StatusBadRequest, "invalid_request", "scope must be space or library")
		return
	}
	grant, err := start(r.Context(), session.User, r.PathValue("userID"), request.Password, request.Reason)
	if err != nil {
		h.writeAccountError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, grant)
}

func (h *productHandler) handleEndViewing(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if err := h.accounts.EndViewing(r.Context(), session.User, r.PathValue("grantID")); err != nil {
		h.writeAccountError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// withPhotoPrincipal admits signed-in users to the photo API, which applies
// its own Policy; writes still need the session's CSRF token. An in-process
// photo API receives the caller as a Principal, while a Proxy forwards only
// the session token for the photo service to confirm.
func (h *productHandler) withPhotoPrincipal(next http.Handler) http.HandlerFunc {
	serve := func(w http.ResponseWriter, r *http.Request, _ accounts.Session) { next.ServeHTTP(w, r) }
	if _, proxied := next.(*photosapi.Proxy); !proxied {
		serve = func(w http.ResponseWriter, r *http.Request, session accounts.Session) {
			viewings, err := h.accounts.LibraryViewings(r.Context(), session.User)
			if err != nil {
				h.internalError(w, r, err)
				return
			}
			principal := photosapi.NewPrincipal(session.User.ID, session.User.Username, session.User.Role == accounts.RoleAdmin, viewings)
			next.ServeHTTP(w, r.WithContext(photosapi.WithPrincipal(r.Context(), principal)))
		}
	}
	read, write := h.withSession(serve), h.withMutation(serve)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			read(w, r)
			return
		}
		write(w, r)
	}
}

// withUser hands an API the signed-in user through attach; writes need the
// CSRF token.
func (h *productHandler) withUser(next http.Handler, attach func(context.Context, accounts.User) context.Context) http.HandlerFunc {
	serve := func(w http.ResponseWriter, r *http.Request, session accounts.Session) {
		next.ServeHTTP(w, r.WithContext(attach(r.Context(), session.User)))
	}
	read, write := h.withSession(serve), h.withMutation(serve)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			read(w, r)
			return
		}
		write(w, r)
	}
}

func (h *productHandler) withMutation(next func(http.ResponseWriter, *http.Request, accounts.Session)) http.HandlerFunc {
	return h.withSession(func(w http.ResponseWriter, r *http.Request, session accounts.Session) {
		provided := r.Header.Get("X-CSRF-Token")
		if subtle.ConstantTimeCompare([]byte(provided), []byte(session.CSRFToken)) != 1 {
			writeError(w, http.StatusForbidden, "csrf_failed", "CSRF token is invalid")
			return
		}
		next(w, r, session)
	})
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

func decodeJSON(r *http.Request, target any) error {
	if mediaType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])); mediaType != "application/json" {
		return errors.New("Content-Type must be application/json")
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON value")
	}
	return nil
}

func (h *productHandler) writeAccountError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, accounts.ErrSetupComplete):
		writeError(w, http.StatusConflict, "setup_complete", err.Error())
	case errors.Is(err, accounts.ErrUsernameUnavailable), errors.Is(err, accounts.ErrIdentityConflict):
		writeError(w, http.StatusConflict, "username_unavailable", err.Error())
	case errors.Is(err, accounts.ErrViewingActive):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, accounts.ErrNicknameUnavailable):
		writeError(w, http.StatusConflict, "nickname_unavailable", err.Error())
	case errors.Is(err, accounts.ErrInvalidNickname):
		writeError(w, http.StatusUnprocessableEntity, "invalid_nickname", err.Error())
	case errors.Is(err, accounts.ErrInvalidUsername):
		writeError(w, http.StatusUnprocessableEntity, "invalid_username", err.Error())
	case errors.Is(err, accounts.ErrInvalidPassword):
		writeError(w, http.StatusUnprocessableEntity, "invalid_password", err.Error())
	case errors.Is(err, accounts.ErrPasswordUnchanged):
		writeError(w, http.StatusUnprocessableEntity, "password_unchanged", err.Error())
	case errors.Is(err, accounts.ErrReasonRequired):
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", err.Error())
	case errors.Is(err, accounts.ErrInvalidCredentials):
		writeError(w, http.StatusForbidden, "reauthentication_failed", "the password is incorrect")
	case errors.Is(err, accounts.ErrVolumeUnavailable):
		writeError(w, http.StatusLocked, "volume_unavailable", err.Error())
	case errors.Is(err, accounts.ErrSMBDisablePending):
		writeError(w, http.StatusServiceUnavailable, "smb_disable_pending", err.Error())
	case errors.Is(err, accounts.ErrCredentialProvision):
		writeError(w, http.StatusServiceUnavailable, "credential_provision_failed", "account could not be enabled for SMB; repair the host service and retry")
	case errors.Is(err, accounts.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "operation is forbidden")
	case errors.Is(err, accounts.ErrUserNotFound):
		writeError(w, http.StatusNotFound, "not_found", "user was not found")
	default:
		h.internalError(w, r, err)
	}
}

func (h *productHandler) writeStorageError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, storage.ErrDiskNotFound), errors.Is(err, storage.ErrPlanNotFound):
		writeError(w, http.StatusNotFound, "not_found", "storage resource was not found")
	case errors.Is(err, storage.ErrDiskNotEligible):
		writeError(w, http.StatusUnprocessableEntity, "disk_not_eligible", err.Error())
	case errors.Is(err, storage.ErrConfirmation):
		writeError(w, http.StatusUnprocessableEntity, "confirmation_mismatch", err.Error())
	case errors.Is(err, storage.ErrPlanExpired):
		writeError(w, http.StatusGone, "plan_expired", err.Error())
	case errors.Is(err, storage.ErrPlanState):
		writeError(w, http.StatusConflict, "plan_state_conflict", err.Error())
	case errors.Is(err, storage.ErrVolumeUnavailable):
		writeError(w, http.StatusConflict, "volume_unavailable", err.Error())
	case errors.Is(err, storage.ErrVolumeBusy):
		writeError(w, http.StatusConflict, "volume_busy", err.Error())
	case errors.Is(err, storage.ErrNoConversion):
		writeError(w, http.StatusConflict, "no_conversion", err.Error())
	case errors.Is(err, storage.ErrInvalidLayout):
		writeError(w, http.StatusUnprocessableEntity, "invalid_layout", err.Error())
	case errors.Is(err, storage.ErrLayoutTooSmall):
		writeError(w, http.StatusUnprocessableEntity, "layout_too_small", err.Error())
	case errors.Is(err, storage.ErrOperationAttention), errors.Is(err, storage.ErrDiskChanged):
		writeError(w, http.StatusServiceUnavailable, "operation_needs_attention", err.Error())
	default:
		h.internalError(w, r, err)
	}
}

func (h *productHandler) writeFileError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, files.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "file space access is forbidden")
	case errors.Is(err, files.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "file resource was not found")
	case errors.Is(err, files.ErrConflict):
		writeError(w, http.StatusConflict, "name_conflict", "a file with this name already exists")
	case errors.Is(err, files.ErrNameTooLong), errors.Is(err, syscall.ENAMETOOLONG):
		writeError(w, http.StatusUnprocessableEntity, "name_too_long", "names may hold at most 255 bytes")
	case errors.Is(err, files.ErrNameNotPortable):
		writeError(w, http.StatusUnprocessableEntity, "name_not_portable", "the name cannot be used on Windows or macOS")
	case errors.Is(err, files.ErrInvalidName), errors.Is(err, files.ErrUnsupportedType),
		errors.Is(err, files.ErrInvalidQuery), errors.Is(err, files.ErrInvalidSchedule):
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", err.Error())
	case errors.Is(err, files.ErrVolumeUnavailable):
		writeError(w, http.StatusLocked, "volume_unavailable", err.Error())
	case errors.Is(err, files.ErrInsufficientSpace):
		writeError(w, http.StatusInsufficientStorage, "insufficient_storage", err.Error())
	default:
		h.internalError(w, r, err)
	}
}

func (h *productHandler) internalError(w http.ResponseWriter, r *http.Request, err error) {
	h.logger.ErrorContext(r.Context(), "product request failed", "path", r.URL.Path, "error", err)
	writeError(w, http.StatusInternalServerError, "internal_error", "request could not be completed")
}
