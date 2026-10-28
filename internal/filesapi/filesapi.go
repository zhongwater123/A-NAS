// Package filesapi serves the files of the spaces a signed-in user sees:
// listing, uploads, downloads, moves and copies, the recycle bin and the
// space's snapshots. Every operation goes through files.Service, which acts
// as the user through the File Broker (ADR 0008).
package filesapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"syscall"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
)

// API serves the file routes.
type API struct {
	files *files.Service
	// changed, when set, is told after a Web upload, move, copy, delete or
	// restore changed a space, so indexes over ordinary files (the media
	// center) catch up without waiting for their periodic scan. The context
	// carries the session token.
	changed func(ctx context.Context, user accounts.User, spaceID string)
	logger  *slog.Logger
}

func New(service *files.Service, changed func(context.Context, accounts.User, string), logger *slog.Logger) *API {
	return &API{files: service, changed: changed, logger: logger}
}

func (h *API) Routes() []apiroute.Route {
	session, mutation := apiroute.Session, apiroute.Mutation
	return []apiroute.Route{
		{Method: http.MethodGet, Pattern: "/api/v1/spaces/{spaceID}/entries", Posture: session, Handler: h.handleEntries},
		{Method: http.MethodPost, Pattern: "/api/v1/spaces/{spaceID}/directories", Posture: mutation, Handler: h.handleCreateDirectory},
		{Method: http.MethodPost, Pattern: "/api/v1/spaces/{spaceID}/uploads", Posture: mutation, Handler: h.handleUpload},
		{Method: http.MethodGet, Pattern: "/api/v1/files/{fileID}/content", Posture: session, Handler: h.handleDownload},
		{Method: http.MethodGet, Pattern: "/api/v1/files/{fileID}/versions", Posture: session, Handler: h.handleFileVersions},
		{Method: http.MethodPatch, Pattern: "/api/v1/files/{fileID}", Posture: mutation, Handler: h.handleMoveFile},
		{Method: http.MethodPost, Pattern: "/api/v1/files/{fileID}/copies", Posture: mutation, Handler: h.handleCopyFile},
		{Method: http.MethodDelete, Pattern: "/api/v1/files/{fileID}", Posture: mutation, Handler: h.handleDeleteFile},
		{Method: http.MethodGet, Pattern: "/api/v1/trash", Posture: session, Handler: h.handleTrash},
		{Method: http.MethodPost, Pattern: "/api/v1/trash/{trashID}/restore", Posture: mutation, Handler: h.handleRestoreTrash},
		{Method: http.MethodDelete, Pattern: "/api/v1/trash/{trashID}", Posture: mutation, Handler: h.handlePurgeTrash},
		{Method: http.MethodGet, Pattern: "/api/v1/spaces/{spaceID}/snapshots", Posture: session, Handler: h.handleSnapshots},
		{Method: http.MethodPost, Pattern: "/api/v1/spaces/{spaceID}/snapshots", Posture: mutation, Handler: h.handleCreateSnapshot},
		{Method: http.MethodGet, Pattern: "/api/v1/spaces/{spaceID}/snapshot-schedule", Posture: session, Handler: h.handleSnapshotSchedule},
		{Method: http.MethodPut, Pattern: "/api/v1/spaces/{spaceID}/snapshot-schedule", Posture: mutation, Handler: h.handleUpdateSnapshotSchedule},
		{Method: http.MethodGet, Pattern: "/api/v1/spaces/{spaceID}/snapshot-search", Posture: session, Handler: h.handleSearchSnapshots},
		{Method: http.MethodGet, Pattern: "/api/v1/spaces/{spaceID}/removed-entries", Posture: session, Handler: h.handleRemovedEntries},
		{Method: http.MethodGet, Pattern: "/api/v1/snapshots/{snapshotID}/entries", Posture: session, Handler: h.handleSnapshotEntries},
		{Method: http.MethodGet, Pattern: "/api/v1/snapshots/{snapshotID}/entries/{entryID}/content", Posture: session, Handler: h.handleSnapshotContent},
		{Method: http.MethodPost, Pattern: "/api/v1/snapshots/{snapshotID}/entries/{entryID}/restore", Posture: mutation, Handler: h.handleRestoreSnapshotEntry},
		{Method: http.MethodPost, Pattern: "/api/v1/snapshots/{snapshotID}/restore", Posture: mutation, Handler: h.handleRestoreSnapshot},
		{Method: http.MethodPost, Pattern: "/api/v1/snapshots/{snapshotID}/keep", Posture: mutation, Handler: h.handleKeepSnapshot},
		{Method: http.MethodDelete, Pattern: "/api/v1/snapshots/{snapshotID}", Posture: mutation, Handler: h.handleDeleteSnapshot},
	}
}

func (h *API) handleEntries(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if h.files == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "files_unavailable", "file service is unavailable")
		return
	}
	entries, err := h.files.List(r.Context(), session.User, r.PathValue("spaceID"), r.URL.Query().Get("parentId"))
	if err != nil {
		WriteError(w, r, h.logger, err)
		return
	}
	if entries == nil {
		entries = []files.Entry{}
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": entries})
}

func (h *API) handleCreateDirectory(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if h.files == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "files_unavailable", "file service is unavailable")
		return
	}
	var request struct {
		ParentID string `json:"parentId"`
		Name     string `json:"name"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &request) {
		return
	}
	entry, err := h.files.CreateDirectory(r.Context(), session.User, r.PathValue("spaceID"), request.ParentID, request.Name)
	if err != nil {
		WriteError(w, r, h.logger, err)
		return
	}
	httpjson.Write(w, http.StatusCreated, entry)
}

func (h *API) handleUpload(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if h.files == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "files_unavailable", "file service is unavailable")
		return
	}
	reader, err := r.MultipartReader()
	if err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_multipart", "multipart upload is invalid")
		return
	}
	var parentID string
	for {
		part, nextErr := reader.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			httpjson.WriteError(w, http.StatusBadRequest, "invalid_multipart", "multipart upload is invalid")
			return
		}
		switch part.FormName() {
		case "parentId":
			value, readErr := io.ReadAll(io.LimitReader(part, 4097))
			_ = part.Close()
			if readErr != nil || len(value) > 4096 {
				httpjson.WriteError(w, http.StatusBadRequest, "invalid_parent", "parent ID is invalid")
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
				WriteError(w, r, h.logger, uploadErr)
				return
			}
			h.filesChanged(r, session, entry.SpaceID)
			httpjson.Write(w, http.StatusCreated, entry)
			return
		default:
			_ = part.Close()
		}
	}
	httpjson.WriteError(w, http.StatusBadRequest, "file_required", "one file part is required")
}

func (h *API) handleDownload(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if h.files == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "files_unavailable", "file service is unavailable")
		return
	}
	content, err := h.files.OpenContent(r.Context(), session.User, r.PathValue("fileID"))
	if err != nil {
		WriteError(w, r, h.logger, err)
		return
	}
	defer func() { _ = content.Reader.Close() }()
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

func (h *API) handleMoveFile(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		SpaceID  string `json:"spaceId"`
		ParentID string `json:"parentId"`
		Name     string `json:"name"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &request) {
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
		WriteError(w, r, h.logger, err)
		return
	}
	if from != entry.SpaceID {
		h.filesChanged(r, session, from)
	}
	h.filesChanged(r, session, entry.SpaceID)
	httpjson.Write(w, http.StatusOK, entry)
}

func (h *API) handleCopyFile(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		SpaceID  string `json:"spaceId"`
		ParentID string `json:"parentId"`
		Name     string `json:"name"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &request) {
		return
	}
	entry, err := h.files.Copy(r.Context(), session.User, r.PathValue("fileID"), request.SpaceID, request.ParentID, request.Name)
	if err != nil {
		WriteError(w, r, h.logger, err)
		return
	}
	h.filesChanged(r, session, entry.SpaceID)
	httpjson.Write(w, http.StatusCreated, entry)
}

func (h *API) handleDeleteFile(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	item, err := h.files.DeleteByID(r.Context(), session.User, r.PathValue("fileID"))
	if err != nil {
		WriteError(w, r, h.logger, err)
		return
	}
	h.filesChanged(r, session, item.SpaceID)
	httpjson.Write(w, http.StatusOK, item)
}

// filesChanged tells FilesChanged, if set, that a Web operation changed a space.
func (h *API) filesChanged(r *http.Request, session accounts.Session, spaceID string) {
	if h.changed != nil && spaceID != "" {
		h.changed(r.Context(), session.User, spaceID)
	}
}

func (h *API) handleTrash(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	items, err := h.files.ListTrash(r.Context(), session.User)
	if err != nil {
		WriteError(w, r, h.logger, err)
		return
	}
	if items == nil {
		items = []files.TrashItem{}
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": items})
}

func (h *API) handleRestoreTrash(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		ParentID string `json:"parentId"`
		Name     string `json:"name"`
		// Conflict is ask (the default), rename or replace, as for uploads.
		Conflict files.ConflictPolicy `json:"conflict"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &request) {
		return
	}
	switch request.Conflict {
	case "", files.ConflictAsk, files.ConflictRename, files.ConflictReplace:
	default:
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "conflict must be ask, rename or replace")
		return
	}
	restored, err := h.files.Restore(r.Context(), session.User, r.PathValue("trashID"), request.ParentID, request.Name, request.Conflict)
	if err != nil {
		WriteError(w, r, h.logger, err)
		return
	}
	h.filesChanged(r, session, restored.SpaceID)
	httpjson.Write(w, http.StatusOK, restored)
}

func (h *API) handlePurgeTrash(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if err := h.files.Purge(r.Context(), session.User, r.PathValue("trashID")); err != nil {
		WriteError(w, r, h.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *API) handleSnapshots(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	items, err := h.files.ListSnapshots(r.Context(), session.User, r.PathValue("spaceID"))
	if err != nil {
		WriteError(w, r, h.logger, err)
		return
	}
	if items == nil {
		items = []files.Snapshot{}
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": items})
}

func (h *API) handleCreateSnapshot(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		Name string `json:"name"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &request) {
		return
	}
	snapshot, err := h.files.CreateSnapshot(r.Context(), session.User, r.PathValue("spaceID"), request.Name)
	if err != nil {
		WriteError(w, r, h.logger, err)
		return
	}
	httpjson.Write(w, http.StatusCreated, snapshot)
}

func (h *API) handleSnapshotEntries(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	items, err := h.files.ListSnapshotEntries(r.Context(), session.User, r.PathValue("snapshotID"), r.URL.Query().Get("parentId"))
	if err != nil {
		WriteError(w, r, h.logger, err)
		return
	}
	if items == nil {
		items = []files.SnapshotEntry{}
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": items})
}

func (h *API) handleSnapshotContent(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	content, err := h.files.OpenSnapshotContent(r.Context(), session.User, r.PathValue("snapshotID"), r.PathValue("entryID"))
	if err != nil {
		WriteError(w, r, h.logger, err)
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

func (h *API) handleSearchSnapshots(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	items, truncated, err := h.files.SearchSnapshots(r.Context(), session.User, r.PathValue("spaceID"), r.URL.Query().Get("q"))
	if err != nil {
		WriteError(w, r, h.logger, err)
		return
	}
	if items == nil {
		items = []files.SnapshotMatch{}
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": items, "truncated": truncated})
}

func (h *API) handleRemovedEntries(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	items, truncated, err := h.files.RemovedEntries(r.Context(), session.User, r.PathValue("spaceID"))
	if err != nil {
		WriteError(w, r, h.logger, err)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": items, "truncated": truncated})
}

func (h *API) handleFileVersions(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	match, err := h.files.EntryVersions(r.Context(), session.User, r.PathValue("fileID"))
	if err != nil {
		WriteError(w, r, h.logger, err)
		return
	}
	httpjson.Write(w, http.StatusOK, match)
}

func (h *API) handleSnapshotSchedule(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	schedule, err := h.files.SnapshotSchedule(r.Context(), session.User, r.PathValue("spaceID"))
	if err != nil {
		WriteError(w, r, h.logger, err)
		return
	}
	httpjson.Write(w, http.StatusOK, schedule)
}

func (h *API) handleUpdateSnapshotSchedule(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request files.SnapshotScheduleSettings
	if !httpjson.Read(w, r, httpjson.Limit, &request) {
		return
	}
	schedule, err := h.files.UpdateSnapshotSchedule(r.Context(), session.User, r.PathValue("spaceID"), request)
	if err != nil {
		WriteError(w, r, h.logger, err)
		return
	}
	httpjson.Write(w, http.StatusOK, schedule)
}

func (h *API) handleKeepSnapshot(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	snapshot, err := h.files.KeepSnapshot(r.Context(), session.User, r.PathValue("snapshotID"))
	if err != nil {
		WriteError(w, r, h.logger, err)
		return
	}
	httpjson.Write(w, http.StatusOK, snapshot)
}

func (h *API) handleRestoreSnapshotEntry(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		ParentID string `json:"parentId"`
		Name     string `json:"name"`
		Replace  bool   `json:"replace"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &request) {
		return
	}
	entry, err := h.files.RestoreSnapshotEntry(r.Context(), session.User, r.PathValue("snapshotID"), r.PathValue("entryID"),
		request.ParentID, request.Name, request.Replace)
	if err != nil {
		WriteError(w, r, h.logger, err)
		return
	}
	h.filesChanged(r, session, entry.SpaceID)
	httpjson.Write(w, http.StatusCreated, entry)
}

func (h *API) handleRestoreSnapshot(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		ParentID string `json:"parentId"`
		Name     string `json:"name"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &request) {
		return
	}
	entry, err := h.files.RestoreSnapshot(r.Context(), session.User, r.PathValue("snapshotID"), request.ParentID, request.Name)
	if err != nil {
		WriteError(w, r, h.logger, err)
		return
	}
	h.filesChanged(r, session, entry.SpaceID)
	httpjson.Write(w, http.StatusCreated, entry)
}

func (h *API) handleDeleteSnapshot(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if err := h.files.DeleteSnapshot(r.Context(), session.User, r.PathValue("snapshotID")); err != nil {
		WriteError(w, r, h.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// WriteError answers an error of the file service.
func WriteError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	switch {
	case errors.Is(err, files.ErrForbidden):
		httpjson.WriteError(w, http.StatusForbidden, "forbidden", "file space access is forbidden")
	case errors.Is(err, files.ErrNotFound):
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "file resource was not found")
	case errors.Is(err, files.ErrSourceChanged):
		httpjson.WriteError(w, http.StatusConflict, "source_changed", "source changed during copy; retry after writes finish")
	case errors.Is(err, files.ErrConflict):
		httpjson.WriteError(w, http.StatusConflict, "name_conflict", "a file with this name already exists")
	case errors.Is(err, files.ErrNameTooLong), errors.Is(err, syscall.ENAMETOOLONG):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "name_too_long", "names may hold at most 255 bytes")
	case errors.Is(err, files.ErrNameNotPortable):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "name_not_portable", "the name cannot be used on Windows or macOS")
	case errors.Is(err, files.ErrInvalidName), errors.Is(err, files.ErrUnsupportedType),
		errors.Is(err, files.ErrInvalidQuery), errors.Is(err, files.ErrInvalidSchedule):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "validation_failed", err.Error())
	case errors.Is(err, files.ErrVolumeUnavailable):
		httpjson.WriteError(w, http.StatusLocked, "volume_unavailable", err.Error())
	case errors.Is(err, files.ErrInsufficientSpace):
		httpjson.WriteError(w, http.StatusInsufficientStorage, "insufficient_storage", err.Error())
	default:
		httpjson.WriteInternal(w, r, logger, err)
	}
}
