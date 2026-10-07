package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/hoststate"
	"github.com/zhongwater123/A-NAS/internal/storage"
)

const sessionCookieName = "anas_session"

type ProductDependencies struct {
	Reader         hoststate.Observer
	DataSource     DataSource
	ProductVersion string
	Accounts       *accounts.Service
	Files          *files.Service
	Storage        *storage.Service
	Terminal       http.Handler
	Logger         *slog.Logger
}

type productHandler struct {
	state    http.Handler
	accounts *accounts.Service
	files    *files.Service
	storage  *storage.Service
	terminal http.Handler
	logger   *slog.Logger
	mux      *http.ServeMux
}

func NewProduct(dependencies ProductDependencies) http.Handler {
	logger := dependencies.Logger
	if logger == nil {
		logger = slog.Default()
	}
	handler := &productHandler{
		state:    New(dependencies.Reader, dependencies.DataSource, dependencies.ProductVersion, logger),
		accounts: dependencies.Accounts, files: dependencies.Files, storage: dependencies.Storage,
		terminal: dependencies.Terminal, logger: logger,
		mux: http.NewServeMux(),
	}
	handler.routes()
	return handler
}

func (h *productHandler) routes() {
	h.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { h.state.ServeHTTP(w, r) })
	h.mux.HandleFunc("GET /api/v1/setup/status", h.handleSetupStatus)
	h.mux.HandleFunc("POST /api/v1/setup/admin", h.handleSetupAdministrator)
	h.mux.HandleFunc("POST /api/v1/session", h.handleCreateSession)
	h.mux.HandleFunc("GET /api/v1/session", h.withSession(h.handleCurrentSession))
	h.mux.HandleFunc("DELETE /api/v1/session", h.withMutation(h.handleDeleteSession))
	h.mux.HandleFunc("GET /api/v1/terminal", h.withSession(h.handleTerminal))
	h.mux.HandleFunc("GET /api/v1/terminal/session", h.withSession(h.handleTerminal))
	for _, path := range []string{"/api/v1/system", "/api/v1/disks", "/api/v1/host-state", "/api/v1/metrics"} {
		h.mux.HandleFunc("GET "+path, h.withSession(func(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
			h.state.ServeHTTP(w, r)
		}))
	}
	h.mux.HandleFunc("GET /api/v1/spaces", h.withSession(h.handleSpaces))
	h.mux.HandleFunc("GET /api/v1/spaces/{spaceID}/entries", h.withSession(h.handleEntries))
	h.mux.HandleFunc("POST /api/v1/spaces/{spaceID}/directories", h.withMutation(h.handleCreateDirectory))
	h.mux.HandleFunc("POST /api/v1/spaces/{spaceID}/uploads", h.withMutation(h.handleUpload))
	h.mux.HandleFunc("GET /api/v1/files/{fileID}/content", h.withSession(h.handleDownload))
	h.mux.HandleFunc("PATCH /api/v1/files/{fileID}", h.withMutation(h.handleMoveFile))
	h.mux.HandleFunc("POST /api/v1/files/{fileID}/copies", h.withMutation(h.handleCopyFile))
	h.mux.HandleFunc("DELETE /api/v1/files/{fileID}", h.withMutation(h.handleDeleteFile))
	h.mux.HandleFunc("GET /api/v1/trash", h.withSession(h.handleTrash))
	h.mux.HandleFunc("POST /api/v1/trash/{trashID}/restore", h.withMutation(h.handleRestoreTrash))
	h.mux.HandleFunc("DELETE /api/v1/trash/{trashID}", h.withMutation(h.handlePurgeTrash))
	h.mux.HandleFunc("GET /api/v1/spaces/{spaceID}/snapshots", h.withSession(h.handleSnapshots))
	h.mux.HandleFunc("POST /api/v1/spaces/{spaceID}/snapshots", h.withMutation(h.handleCreateSnapshot))
	h.mux.HandleFunc("GET /api/v1/snapshots/{snapshotID}/entries", h.withSession(h.handleSnapshotEntries))
	h.mux.HandleFunc("POST /api/v1/snapshots/{snapshotID}/entries/{entryID}/restore", h.withMutation(h.handleRestoreSnapshotFile))
	h.mux.HandleFunc("DELETE /api/v1/snapshots/{snapshotID}", h.withMutation(h.handleDeleteSnapshot))
	h.mux.HandleFunc("GET /api/v1/users", h.withSession(h.handleUsers))
	h.mux.HandleFunc("POST /api/v1/users", h.withMutation(h.handleCreateUser))
	h.mux.HandleFunc("PATCH /api/v1/users/{userID}/credential", h.withMutation(h.handleResetUserCredential))
	h.mux.HandleFunc("DELETE /api/v1/users/{userID}", h.withMutation(h.handleDisableUser))
	h.mux.HandleFunc("GET /api/v1/volumes", h.withSession(h.handleVolumes))
	h.mux.HandleFunc("POST /api/v1/storage/plans", h.withMutation(h.handleCreateStoragePlan))
	h.mux.HandleFunc("POST /api/v1/storage/plans/{planID}/confirm", h.withMutation(h.handleConfirmStoragePlan))
	h.mux.HandleFunc("POST /api/v1/storage/plans/{planID}/execute", h.withMutation(h.handleExecuteStoragePlan))
	h.mux.HandleFunc("GET /api/v1/operations/{operationID}", h.withSession(h.handleOperation))
	h.mux.HandleFunc("GET /api/v1/audit", h.withSession(h.handleAudit))
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
	writeJSON(w, http.StatusOK, map[string]bool{"setupRequired": required})
}

func (h *productHandler) handleSetupAdministrator(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	if _, err := h.accounts.SetupAdministrator(r.Context(), request.Username, request.Password); err != nil {
		h.writeAccountError(w, r, err)
		return
	}
	session, err := h.accounts.Authenticate(r.Context(), request.Username, request.Password)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	h.setSessionCookie(w, session)
	writeJSON(w, http.StatusCreated, publicSession(session))
}

func (h *productHandler) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	session, err := h.accounts.Authenticate(r.Context(), request.Username, request.Password)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "username or password is invalid")
		return
	}
	h.setSessionCookie(w, session)
	writeJSON(w, http.StatusOK, publicSession(session))
}

func (h *productHandler) handleCurrentSession(w http.ResponseWriter, _ *http.Request, session accounts.Session) {
	writeJSON(w, http.StatusOK, publicSession(session))
}

func (h *productHandler) handleDeleteSession(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	cookie, _ := r.Cookie(sessionCookieName)
	if cookie != nil {
		_ = h.accounts.EndSession(r.Context(), cookie.Value)
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
	if spaces == nil {
		spaces = []accounts.Space{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": spaces})
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
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": content.Name}))
	http.ServeContent(w, r, content.Name, content.ModifiedAt, content.Reader)
}

func (h *productHandler) handleMoveFile(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		ParentID string `json:"parentId"`
		Name     string `json:"name"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	entry, err := h.files.Move(r.Context(), session.User, r.PathValue("fileID"), request.ParentID, request.Name)
	if err != nil {
		h.writeFileError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

func (h *productHandler) handleCopyFile(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		ParentID string `json:"parentId"`
		Name     string `json:"name"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	entry, err := h.files.Copy(r.Context(), session.User, r.PathValue("fileID"), request.ParentID, request.Name)
	if err != nil {
		h.writeFileError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, entry)
}

func (h *productHandler) handleDeleteFile(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	item, err := h.files.DeleteByID(r.Context(), session.User, r.PathValue("fileID"))
	if err != nil {
		h.writeFileError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
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
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	entry, err := h.files.Restore(r.Context(), session.User, r.PathValue("trashID"), request.ParentID, request.Name)
	if err != nil {
		h.writeFileError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, entry)
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

func (h *productHandler) handleRestoreSnapshotFile(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		ParentID string `json:"parentId"`
		Name     string `json:"name"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	entry, err := h.files.RestoreSnapshotFile(r.Context(), session.User, r.PathValue("snapshotID"), r.PathValue("entryID"), request.ParentID, request.Name)
	if err != nil {
		h.writeFileError(w, r, err)
		return
	}
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
		DiskID string `json:"diskId"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	plan, err := h.storage.PlanCreateVolume(r.Context(), request.DiskID)
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
		next(w, r, session)
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

func (h *productHandler) setSessionCookie(w http.ResponseWriter, session accounts.Session) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: session.Token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Expires: session.ExpiresAt,
	})
}

type sessionResponse struct {
	CSRFToken string        `json:"csrfToken"`
	ExpiresAt string        `json:"expiresAt"`
	User      accounts.User `json:"user"`
}

func publicSession(session accounts.Session) sessionResponse {
	return sessionResponse{CSRFToken: session.CSRFToken, ExpiresAt: session.ExpiresAt.UTC().Format(timeFormat), User: session.User}
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
	case errors.Is(err, accounts.ErrSetupComplete), errors.Is(err, accounts.ErrUsernameUnavailable):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, accounts.ErrInvalidUsername), errors.Is(err, accounts.ErrWeakPassword):
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", err.Error())
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
	case errors.Is(err, storage.ErrVolumeExists):
		writeError(w, http.StatusConflict, "data_volume_exists", err.Error())
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
	case errors.Is(err, files.ErrInvalidName), errors.Is(err, files.ErrUnsupportedType):
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
