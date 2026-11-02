// Package rootagent serves the Host Agent's HTTP interface on its Unix
// sockets: it checks each request's document and turns it into a call of
// the typed host operations (ADR 0004). The Product Service reaches it
// through internal/hoststate/agent and links none of it (#221).
package rootagent

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hoststate/agent/wire"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/appid"
	"github.com/zhongwater123/A-NAS/internal/datavolume"
	"github.com/zhongwater123/A-NAS/internal/filesystem"
	"github.com/zhongwater123/A-NAS/internal/hoststate"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/systemlog"
	"github.com/zhongwater123/A-NAS/internal/websearchswitch"
)

type handler struct {
	observer    hoststate.Observer
	volume      datavolume.VolumeExecutor
	credentials accountid.CredentialProvisioner
	identities  accountid.IdentitySynchronizer
	viewing     accountid.ViewingProvisioner
	apps        appid.Host
	snapshots   filesystem.SnapshotBackend
	systemLogs  systemlog.Reader
	webSearch   websearchswitch.Switch
	// reconcileFailed reports the last reconciliation of the data volume.
	reconcileFailed func() bool
	logger          *slog.Logger
}

type Services struct {
	Reader      hoststate.Observer
	Volume      datavolume.VolumeExecutor
	Credentials accountid.CredentialProvisioner
	Identities  accountid.IdentitySynchronizer
	Viewing     accountid.ViewingProvisioner
	Apps        appid.Host
	Snapshots   filesystem.SnapshotBackend
	// SystemLogs reads the allowlisted service logs (ADR 0020).
	SystemLogs systemlog.Reader
	// WebSearch is the administrator's switch of web search (ADR 0030).
	WebSearch websearchswitch.Switch
	// ReconcileFailed reports whether the Host Agent's last reconciliation of
	// the data volume failed; the host state carries it (#166).
	ReconcileFailed func() bool
}

func NewHandler(observer hoststate.Observer, logger *slog.Logger) http.Handler {
	return NewOperationsHandler(Services{Reader: observer}, logger)
}

func NewOperationsHandler(services Services, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &handler{
		observer: services.Reader, volume: services.Volume, credentials: services.Credentials,
		identities: services.Identities, viewing: services.Viewing, apps: services.Apps, snapshots: services.Snapshots,
		systemLogs: services.SystemLogs, webSearch: services.WebSearch, reconcileFailed: services.ReconcileFailed, logger: logger,
	}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == wire.DeviceStatusPath || r.URL.Path == wire.TasksPath {
		h.handleTaskManager(w, r)
		return
	}
	if r.URL.Path == "/v1/storage/volumes" {
		h.handleCreateVolume(w, r)
		return
	}
	if r.URL.Path == "/v1/storage/volumes/disks" {
		h.handleAddDisks(w, r)
		return
	}
	if r.URL.Path == "/v1/storage/volumes/conversion" {
		h.handleContinueConversion(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, wire.VolumeOperationsPath) {
		h.handleVolumeOperation(w, r)
		return
	}
	if r.URL.Path == "/v1/accounts/credential" {
		h.handleSetCredential(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, wire.AppsPathPrefix) {
		h.handleApps(w, r)
		return
	}
	if r.URL.Path == "/v1/viewing-grants" || strings.HasPrefix(r.URL.Path, "/v1/viewing-grants/") {
		h.handleViewing(w, r)
		return
	}
	if r.URL.Path == "/v1/accounts/identities" {
		h.handleSyncIdentities(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/accounts/credential/") {
		h.handleDisableCredential(w, r)
		return
	}
	if r.URL.Path == wire.WebSearchPath {
		h.handleWebSearch(w, r)
		return
	}
	if r.URL.Path == wire.SystemLogsPath {
		h.handleSystemLogs(w, r)
		return
	}
	if r.URL.Path == "/v1/snapshots" {
		h.handleSnapshots(w, r)
		return
	}
	if r.URL.Path == wire.SnapshotSurveyPath {
		h.handleSnapshotSurvey(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/snapshots/") {
		h.handleSnapshotResource(w, r)
		return
	}
	if r.URL.Path != "/v1/state" && r.URL.Path != "/v1/metrics" {
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		httpjson.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if h.observer == nil {
		code, message := "state_unavailable", "host state is unavailable"
		if r.URL.Path == "/v1/metrics" {
			code, message = "metrics_unavailable", "host metrics are unavailable"
		}
		httpjson.WriteError(w, http.StatusServiceUnavailable, code, message)
		return
	}
	if r.URL.Path == "/v1/metrics" {
		metrics, err := h.observer.ReadMetrics(r.Context())
		if err != nil {
			h.logger.ErrorContext(r.Context(), "host metrics read failed", "error", err)
			httpjson.WriteError(w, http.StatusServiceUnavailable, "metrics_unavailable", "host metrics are unavailable")
			return
		}
		httpjson.Write(w, http.StatusOK, wire.MetricsDocumentFromDomain(metrics))
		return
	}
	state, err := h.observer.Read(r.Context())
	if err != nil {
		h.logger.ErrorContext(r.Context(), "host state read failed", "error", err)
		httpjson.WriteError(w, http.StatusServiceUnavailable, "state_unavailable", "host state is unavailable")
		return
	}
	state.ReconcileFailed = h.reconcileFailed != nil && h.reconcileFailed()
	httpjson.Write(w, http.StatusOK, wire.DocumentFromDomain(state))
}

// handleSnapshotSurvey reports which of the named snapshots are on the data
// volume and how many others it holds.
func (h *handler) handleSnapshotSurvey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		httpjson.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if h.snapshots == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_unavailable", "snapshot operation is unavailable")
		return
	}
	var request wire.SnapshotSurveyDocument
	if err := decodeRequest(r, &request); err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "request is invalid")
		return
	}
	survey, err := h.snapshots.Survey(r.Context(), request.Known)
	if errors.Is(err, filesystem.ErrVolumeUnavailable) {
		httpjson.WriteError(w, http.StatusLocked, wire.VolumeUnavailableCode, "the data volume is offline")
		return
	}
	if err != nil {
		h.logger.ErrorContext(r.Context(), "survey snapshots failed", "error", err)
		httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_failed", "snapshot operation failed")
		return
	}
	httpjson.Write(w, http.StatusOK, survey)
}

// handleSnapshots takes a snapshot. It no longer deletes the snapshots that a
// list leaves out (PUT): a Product Service from before #269 that asks gets
// 405 and deletes nothing.
func (h *handler) handleSnapshots(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		httpjson.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if h.snapshots == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_unavailable", "snapshot operation is unavailable")
		return
	}
	var request wire.SnapshotDocument
	if err := decodeRequest(r, &request); err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "request is invalid")
		return
	}
	if err := h.snapshots.Create(r.Context(), request.SpaceID, request.SnapshotID); err != nil {
		h.logger.ErrorContext(r.Context(), "create snapshot failed", "snapshot", request.SnapshotID, "error", err)
		httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_failed", "snapshot operation failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) handleSnapshotResource(w http.ResponseWriter, r *http.Request) {
	if h.snapshots == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_unavailable", "snapshot operation is unavailable")
		return
	}
	remainder := strings.TrimPrefix(r.URL.Path, "/v1/snapshots/")
	snapshotID, suffix, _ := strings.Cut(remainder, "/")
	if snapshotID == "" || strings.Contains(snapshotID, "/") {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "snapshot ID is invalid")
		return
	}
	if r.Method == http.MethodDelete && suffix == "" {
		spaceID := r.URL.Query().Get("spaceId")
		if err := h.snapshots.Delete(r.Context(), spaceID, snapshotID); err != nil {
			if errors.Is(err, filesystem.ErrNotFound) {
				httpjson.WriteError(w, http.StatusNotFound, wire.SnapshotNotFoundCode, "snapshot was not found")
				return
			}
			h.logger.ErrorContext(r.Context(), "delete snapshot failed", "snapshot", snapshotID, "error", err)
			httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_failed", "snapshot operation failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method == http.MethodGet && suffix == "objects" {
		h.streamSnapshotObjects(w, r, snapshotID)
		return
	}
	if r.Method == http.MethodGet && suffix == "object" {
		key := r.URL.Query().Get("key")
		reader, object, err := h.snapshots.Open(r.Context(), snapshotID, key)
		if err != nil {
			httpjson.WriteError(w, http.StatusNotFound, "not_found", "snapshot object was not found")
			return
		}
		defer reader.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-A-NAS-Object-Key", object.Key)
		w.Header().Set("X-A-NAS-Object-Name", url.QueryEscape(object.Name))
		w.Header().Set("X-A-NAS-Object-Kind", string(object.Kind))
		w.Header().Set("X-A-NAS-Object-Size", strconv.FormatInt(object.SizeBytes, 10))
		if _, err := io.Copy(w, reader); err != nil {
			h.logger.ErrorContext(r.Context(), "stream snapshot object failed", "snapshot", snapshotID, "error", err)
		}
		return
	}
	w.Header().Set("Allow", http.MethodGet+", "+http.MethodDelete)
	httpjson.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

// streamSnapshotObjects writes a snapshot's objects as they are walked, one
// JSON line each, and ends with a line that counts them. A walk that fails
// after the first line ends with an error line instead, so a reader never
// takes a cut-off listing for a whole one.
func (h *handler) streamSnapshotObjects(w http.ResponseWriter, r *http.Request, snapshotID string) {
	encoder := json.NewEncoder(w)
	started, count := false, 0
	start := func() {
		if !started {
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.WriteHeader(http.StatusOK)
			started = true
		}
	}
	err := h.snapshots.Objects(r.Context(), snapshotID, func(object filesystem.SnapshotObject) error {
		start()
		count++
		return encoder.Encode(wire.SnapshotObjectLine{Object: &object})
	})
	if err != nil && !started {
		if errors.Is(err, filesystem.ErrNotFound) {
			httpjson.WriteError(w, http.StatusNotFound, wire.SnapshotNotFoundCode, "snapshot was not found")
			return
		}
		h.logger.ErrorContext(r.Context(), "list snapshot objects failed", "snapshot", snapshotID, "error", err)
		httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_failed", "snapshot operation failed")
		return
	}
	start()
	if err != nil {
		if r.Context().Err() == nil {
			h.logger.ErrorContext(r.Context(), "list snapshot objects failed", "snapshot", snapshotID, "error", err)
		}
		_ = encoder.Encode(wire.SnapshotObjectLine{Error: &wire.ErrorDetail{Code: "operation_failed", Message: "snapshot operation failed"}})
		return
	}
	_ = encoder.Encode(wire.SnapshotObjectLine{End: &wire.SnapshotObjectsEnd{Count: count}})
}

func (h *handler) handleCreateVolume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		httpjson.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if h.volume == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_unavailable", "volume operation is unavailable")
		return
	}
	var document wire.CreateVolumeDocument
	if err := decodeRequest(r, &document); err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "request is invalid")
		return
	}
	operation, err := h.volume.StartCreateVolume(r.Context(), datavolume.CreateVolumeRequest{
		PlanID: document.PlanID, Disks: document.Disks, Layout: document.Layout,
	})
	if err != nil {
		h.writeVolumeOperationError(w, r, "start creating the data volume failed", err)
		return
	}
	httpjson.Write(w, http.StatusOK, operation)
}

func (h *handler) handleAddDisks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		httpjson.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if h.volume == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_unavailable", "volume operation is unavailable")
		return
	}
	var document wire.AddDisksDocument
	if err := decodeRequest(r, &document); err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "request is invalid")
		return
	}
	operation, err := h.volume.StartAddDisks(r.Context(), datavolume.AddDisksRequest{
		PlanID: document.PlanID, FilesystemUUID: document.FilesystemUUID, Disks: document.Disks, Layout: document.Layout,
	})
	if err != nil {
		h.writeVolumeOperationError(w, r, "start adding disks to the data volume failed", err)
		return
	}
	httpjson.Write(w, http.StatusOK, operation)
}

// handleVolumeOperation reports a plan's data-volume operation, which runs
// on after the request that started it.
func (h *handler) handleVolumeOperation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		httpjson.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if h.volume == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_unavailable", "volume operation is unavailable")
		return
	}
	planID := strings.TrimPrefix(r.URL.Path, wire.VolumeOperationsPath)
	if planID == "" || strings.Contains(planID, "/") {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "plan ID is invalid")
		return
	}
	operation, err := h.volume.VolumeOperation(r.Context(), planID)
	if err != nil {
		h.writeVolumeOperationError(w, r, "read the data-volume operation failed", err)
		return
	}
	httpjson.Write(w, http.StatusOK, operation)
}

func (h *handler) writeVolumeOperationError(w http.ResponseWriter, r *http.Request, message string, err error) {
	switch {
	case errors.Is(err, datavolume.ErrOperationNotFound):
		httpjson.WriteError(w, http.StatusNotFound, wire.OperationNotFoundCode, "the plan has no data-volume operation")
	case errors.Is(err, datavolume.ErrPlanState):
		httpjson.WriteError(w, http.StatusConflict, wire.OperationConflictCode, "another data-volume operation is running, or the plan asked for another one")
	default:
		h.logger.ErrorContext(r.Context(), message, "error", err)
		httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_failed", "volume operation failed")
	}
}

func (h *handler) handleContinueConversion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		httpjson.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if h.volume == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_unavailable", "volume operation is unavailable")
		return
	}
	if err := h.volume.ContinueConversion(r.Context()); err != nil {
		if errors.Is(err, datavolume.ErrNoConversion) {
			httpjson.WriteError(w, http.StatusConflict, wire.NoConversionCode, "the data volume has nothing to convert")
			return
		}
		h.logger.ErrorContext(r.Context(), "continue data-volume conversion failed", "error", err)
		httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_failed", "volume operation failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) handleSetCredential(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", http.MethodPut)
		httpjson.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if h.credentials == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_unavailable", "credential operation is unavailable")
		return
	}
	var request wire.CredentialDocument
	if err := decodeRequest(r, &request); err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "request is invalid")
		return
	}
	if err := h.credentials.SetCredential(r.Context(), accountid.CredentialRequest{
		UserID: request.UserID, PrivateSpaceID: request.PrivateSpaceID, Username: request.Username,
		Password: request.Password, Role: request.Role, UID: request.UID, Enabled: request.Enabled,
	}); err != nil {
		h.logger.ErrorContext(r.Context(), "set credential failed", "username", request.Username, "error", err)
		h.writeAccountError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) handleSyncIdentities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", http.MethodPut)
		httpjson.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if h.identities == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_unavailable", "identity operation is unavailable")
		return
	}
	var request wire.IdentitiesDocument
	if err := decodeRequest(r, &request); err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "request is invalid")
		return
	}
	identities := make([]accountid.Identity, len(request.Identities))
	for i, identity := range request.Identities {
		identities[i] = accountid.Identity{Username: identity.Username, UID: identity.UID, Role: identity.Role, Enabled: identity.Enabled}
	}
	if err := h.identities.SyncIdentities(r.Context(), identities); err != nil {
		h.logger.ErrorContext(r.Context(), "sync identities failed", "error", err)
		h.writeAccountError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) writeAccountError(w http.ResponseWriter, err error) {
	if errors.Is(err, accountid.ErrIdentityConflict) {
		httpjson.WriteError(w, http.StatusConflict, wire.IdentityConflictCode, "identity conflicts with an existing host account")
		return
	}
	httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_failed", "credential operation failed")
}

func (h *handler) handleDisableCredential(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", http.MethodDelete)
		httpjson.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if h.credentials == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_unavailable", "credential operation is unavailable")
		return
	}
	username := strings.TrimPrefix(r.URL.Path, "/v1/accounts/credential/")
	if username == "" || strings.Contains(username, "/") {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "username is invalid")
		return
	}
	if err := h.credentials.DisableCredential(r.Context(), username); err != nil {
		h.logger.ErrorContext(r.Context(), "disable credential failed", "username", username, "error", err)
		httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_failed", "credential operation failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) handleViewing(w http.ResponseWriter, r *http.Request) {
	if h.viewing == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_unavailable", "viewing operation is unavailable")
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/viewing-grants":
		var document wire.ViewingDocument
		if err := decodeRequest(r, &document); err != nil {
			httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "request is invalid")
			return
		}
		expiresAt, err := time.Parse(time.RFC3339Nano, document.ExpiresAt)
		if err != nil {
			httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "expiry is invalid")
			return
		}
		if err := h.viewing.GrantViewing(r.Context(), accountid.ViewingRequest{
			ID: document.ID, SpaceID: document.SpaceID, AdminUsername: document.AdminUsername,
			AdminUID: document.AdminUID, ExpiresAt: expiresAt,
		}); err != nil {
			if errors.Is(err, accountid.ErrVolumeUnavailable) {
				httpjson.WriteError(w, http.StatusLocked, wire.VolumeUnavailableCode, "the data volume is offline")
				return
			}
			h.logger.ErrorContext(r.Context(), "grant viewing failed", "grant", document.ID, "error", err)
			httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_failed", "viewing operation failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/viewing-grants/"):
		id := strings.TrimPrefix(r.URL.Path, "/v1/viewing-grants/")
		if id == "" || strings.Contains(id, "/") {
			httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "grant is invalid")
			return
		}
		if err := h.viewing.RevokeViewing(r.Context(), id); err != nil {
			h.logger.ErrorContext(r.Context(), "revoke viewing failed", "grant", id, "error", err)
			httpjson.WriteError(w, http.StatusServiceUnavailable, "operation_failed", "viewing operation failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		httpjson.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func decodeRequest(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return wire.EnsureEndOfJSON(decoder)
}
