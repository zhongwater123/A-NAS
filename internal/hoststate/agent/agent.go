// Package agent transports host state between the Product Service and Host Agent.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/hoststate"
	"github.com/zhongwater123/A-NAS/internal/storage"
)

const requestTimeout = 3 * time.Second

const socketEnvironmentVariable = "ANAS_HOST_AGENT_SOCKET"

var ErrUnavailable = errors.New("host agent state is unavailable")

func SocketPath() (string, error) {
	if configured := strings.TrimSpace(os.Getenv(socketEnvironmentVariable)); configured != "" {
		if !filepath.IsAbs(configured) {
			return "", errors.New("ANAS_HOST_AGENT_SOCKET must be an absolute path")
		}
		return filepath.Clean(configured), nil
	}
	runtimeDirectory := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR"))
	if runtimeDirectory == "" || !filepath.IsAbs(runtimeDirectory) {
		return "", errors.New("XDG_RUNTIME_DIR must be an absolute path")
	}
	return filepath.Join(runtimeDirectory, "a-nas", "host-agent.sock"), nil
}

type Client struct {
	httpClient      *http.Client
	operationClient *http.Client
}

func NewClient(socketPath string) *Client {
	dialer := &net.Dialer{Timeout: requestTimeout}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socketPath)
		},
		DisableKeepAlives: true,
	}
	return &Client{
		httpClient:      &http.Client{Transport: transport, Timeout: requestTimeout},
		operationClient: &http.Client{Transport: transport, Timeout: 15 * time.Minute},
	}
}

func (c *Client) Read(ctx context.Context) (hoststate.State, error) {
	var document stateDocument
	if err := c.get(ctx, "/v1/state", &document); err != nil {
		return hoststate.State{}, err
	}
	state, err := document.toDomain()
	if err != nil {
		return hoststate.State{}, fmt.Errorf("validate host agent response: %w", err)
	}
	return state, nil
}

// ReadMetrics returns the Host Agent's latest utilisation observation.
func (c *Client) ReadMetrics(ctx context.Context) (hoststate.Metrics, error) {
	var document metricsDocument
	if err := c.get(ctx, "/v1/metrics", &document); err != nil {
		return hoststate.Metrics{}, err
	}
	metrics, err := document.toDomain()
	if err != nil {
		return hoststate.Metrics{}, fmt.Errorf("validate host agent response: %w", err)
	}
	return metrics, nil
}

func (c *Client) get(ctx context.Context, path string, into any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://host-agent"+path, nil)
	if err != nil {
		return fmt.Errorf("create host agent request: %w", err)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("%w: status %d", ErrUnavailable, response.StatusCode)
	}

	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return fmt.Errorf("decode host agent response: %w", err)
	}
	return ensureEndOfJSON(decoder)
}

type handler struct {
	observer    hoststate.Observer
	volume      storage.VolumeExecutor
	credentials accounts.CredentialProvisioner
	identities  accounts.IdentitySynchronizer
	viewing     accounts.ViewingProvisioner
	snapshots   files.SnapshotBackend
	logger      *slog.Logger
}

type Services struct {
	Reader      hoststate.Observer
	Volume      storage.VolumeExecutor
	Credentials accounts.CredentialProvisioner
	Identities  accounts.IdentitySynchronizer
	Viewing     accounts.ViewingProvisioner
	Snapshots   files.SnapshotBackend
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
		identities: services.Identities, viewing: services.Viewing, snapshots: services.Snapshots, logger: logger,
	}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/storage/volumes" {
		h.handleCreateVolume(w, r)
		return
	}
	if r.URL.Path == "/v1/accounts/credential" {
		h.handleSetCredential(w, r)
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
	if r.URL.Path == "/v1/snapshots" {
		h.handleCreateSnapshot(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/snapshots/") {
		h.handleSnapshotResource(w, r)
		return
	}
	if r.URL.Path != "/v1/state" && r.URL.Path != "/v1/metrics" {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if h.observer == nil {
		code, message := "state_unavailable", "host state is unavailable"
		if r.URL.Path == "/v1/metrics" {
			code, message = "metrics_unavailable", "host metrics are unavailable"
		}
		writeError(w, http.StatusServiceUnavailable, code, message)
		return
	}
	if r.URL.Path == "/v1/metrics" {
		metrics, err := h.observer.ReadMetrics(r.Context())
		if err != nil {
			h.logger.ErrorContext(r.Context(), "host metrics read failed", "error", err)
			writeError(w, http.StatusServiceUnavailable, "metrics_unavailable", "host metrics are unavailable")
			return
		}
		writeJSON(w, http.StatusOK, metricsDocumentFromDomain(metrics))
		return
	}
	state, err := h.observer.Read(r.Context())
	if err != nil {
		h.logger.ErrorContext(r.Context(), "host state read failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "state_unavailable", "host state is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, documentFromDomain(state))
}

func (h *handler) handleCreateSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if h.snapshots == nil {
		writeError(w, http.StatusServiceUnavailable, "operation_unavailable", "snapshot operation is unavailable")
		return
	}
	var request snapshotDocument
	if err := decodeRequest(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request is invalid")
		return
	}
	objects, err := h.snapshots.Create(r.Context(), request.SpaceID, request.SnapshotID)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "create snapshot failed", "snapshot", request.SnapshotID, "error", err)
		writeError(w, http.StatusServiceUnavailable, "operation_failed", "snapshot operation failed")
		return
	}
	writeJSON(w, http.StatusOK, snapshotObjectsDocument{Items: objects})
}

func (h *handler) handleSnapshotResource(w http.ResponseWriter, r *http.Request) {
	if h.snapshots == nil {
		writeError(w, http.StatusServiceUnavailable, "operation_unavailable", "snapshot operation is unavailable")
		return
	}
	remainder := strings.TrimPrefix(r.URL.Path, "/v1/snapshots/")
	snapshotID, suffix, _ := strings.Cut(remainder, "/")
	if snapshotID == "" || strings.Contains(snapshotID, "/") {
		writeError(w, http.StatusBadRequest, "invalid_request", "snapshot ID is invalid")
		return
	}
	if r.Method == http.MethodDelete && suffix == "" {
		spaceID := r.URL.Query().Get("spaceId")
		if err := h.snapshots.Delete(r.Context(), spaceID, snapshotID); err != nil {
			h.logger.ErrorContext(r.Context(), "delete snapshot failed", "snapshot", snapshotID, "error", err)
			writeError(w, http.StatusServiceUnavailable, "operation_failed", "snapshot operation failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method == http.MethodGet && suffix == "object" {
		key := r.URL.Query().Get("key")
		reader, object, err := h.snapshots.Open(r.Context(), snapshotID, key)
		if err != nil {
			writeError(w, http.StatusNotFound, "not_found", "snapshot object was not found")
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
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

func (h *handler) handleCreateVolume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if h.volume == nil {
		writeError(w, http.StatusServiceUnavailable, "operation_unavailable", "volume operation is unavailable")
		return
	}
	var document createVolumeDocument
	if err := decodeRequest(r, &document); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request is invalid")
		return
	}
	volume, err := h.volume.CreateVolume(r.Context(), storage.CreateVolumeRequest{
		PlanID: document.PlanID, DiskID: document.DiskID, Fingerprint: document.Fingerprint,
	})
	if err != nil {
		h.logger.ErrorContext(r.Context(), "create volume failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "operation_failed", "volume operation failed")
		return
	}
	writeJSON(w, http.StatusOK, volume)
}

func (h *handler) handleSetCredential(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", http.MethodPut)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if h.credentials == nil {
		writeError(w, http.StatusServiceUnavailable, "operation_unavailable", "credential operation is unavailable")
		return
	}
	var request credentialDocument
	if err := decodeRequest(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request is invalid")
		return
	}
	if err := h.credentials.SetCredential(r.Context(), accounts.CredentialRequest{
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
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if h.identities == nil {
		writeError(w, http.StatusServiceUnavailable, "operation_unavailable", "identity operation is unavailable")
		return
	}
	var request identitiesDocument
	if err := decodeRequest(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request is invalid")
		return
	}
	identities := make([]accounts.Identity, len(request.Identities))
	for i, identity := range request.Identities {
		identities[i] = accounts.Identity{Username: identity.Username, UID: identity.UID, Role: identity.Role, Enabled: identity.Enabled}
	}
	if err := h.identities.SyncIdentities(r.Context(), identities); err != nil {
		h.logger.ErrorContext(r.Context(), "sync identities failed", "error", err)
		h.writeAccountError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) writeAccountError(w http.ResponseWriter, err error) {
	if errors.Is(err, accounts.ErrIdentityConflict) {
		writeError(w, http.StatusConflict, identityConflictCode, "identity conflicts with an existing host account")
		return
	}
	writeError(w, http.StatusServiceUnavailable, "operation_failed", "credential operation failed")
}

const identityConflictCode = "identity_conflict"

func (h *handler) handleDisableCredential(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", http.MethodDelete)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if h.credentials == nil {
		writeError(w, http.StatusServiceUnavailable, "operation_unavailable", "credential operation is unavailable")
		return
	}
	username := strings.TrimPrefix(r.URL.Path, "/v1/accounts/credential/")
	if username == "" || strings.Contains(username, "/") {
		writeError(w, http.StatusBadRequest, "invalid_request", "username is invalid")
		return
	}
	if err := h.credentials.DisableCredential(r.Context(), username); err != nil {
		h.logger.ErrorContext(r.Context(), "disable credential failed", "username", username, "error", err)
		writeError(w, http.StatusServiceUnavailable, "operation_failed", "credential operation failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type createVolumeDocument struct {
	PlanID      string `json:"planId"`
	DiskID      string `json:"diskId"`
	Fingerprint string `json:"fingerprint"`
}

type credentialDocument struct {
	UserID         string        `json:"userId"`
	PrivateSpaceID string        `json:"privateSpaceId"`
	Username       string        `json:"username"`
	Password       string        `json:"password"`
	Role           accounts.Role `json:"role"`
	UID            int           `json:"uid"`
	Enabled        bool          `json:"enabled"`
}

type identityDocument struct {
	Username string        `json:"username"`
	UID      int           `json:"uid"`
	Role     accounts.Role `json:"role"`
	Enabled  bool          `json:"enabled"`
}

type viewingDocument struct {
	ID            string `json:"id"`
	SpaceID       string `json:"spaceId"`
	AdminUsername string `json:"adminUsername"`
	AdminUID      int    `json:"adminUid"`
	ExpiresAt     string `json:"expiresAt"`
}

func (h *handler) handleViewing(w http.ResponseWriter, r *http.Request) {
	if h.viewing == nil {
		writeError(w, http.StatusServiceUnavailable, "operation_unavailable", "viewing operation is unavailable")
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/viewing-grants":
		var document viewingDocument
		if err := decodeRequest(r, &document); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "request is invalid")
			return
		}
		expiresAt, err := time.Parse(time.RFC3339Nano, document.ExpiresAt)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "expiry is invalid")
			return
		}
		if err := h.viewing.GrantViewing(r.Context(), accounts.ViewingRequest{
			ID: document.ID, SpaceID: document.SpaceID, AdminUsername: document.AdminUsername,
			AdminUID: document.AdminUID, ExpiresAt: expiresAt,
		}); err != nil {
			h.logger.ErrorContext(r.Context(), "grant viewing failed", "grant", document.ID, "error", err)
			writeError(w, http.StatusServiceUnavailable, "operation_failed", "viewing operation failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/viewing-grants/"):
		id := strings.TrimPrefix(r.URL.Path, "/v1/viewing-grants/")
		if id == "" || strings.Contains(id, "/") {
			writeError(w, http.StatusBadRequest, "invalid_request", "grant is invalid")
			return
		}
		if err := h.viewing.RevokeViewing(r.Context(), id); err != nil {
			h.logger.ErrorContext(r.Context(), "revoke viewing failed", "grant", id, "error", err)
			writeError(w, http.StatusServiceUnavailable, "operation_failed", "viewing operation failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

type identitiesDocument struct {
	Identities []identityDocument `json:"identities"`
}

type snapshotDocument struct {
	SpaceID    string `json:"spaceId"`
	SnapshotID string `json:"snapshotId"`
}

type snapshotObjectsDocument struct {
	Items []files.SnapshotObject `json:"items"`
}

func decodeRequest(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return ensureEndOfJSON(decoder)
}

func (c *Client) CreateVolume(ctx context.Context, request storage.CreateVolumeRequest) (storage.Volume, error) {
	var response storage.Volume
	err := c.doJSON(ctx, c.operationClient, http.MethodPost, "/v1/storage/volumes", createVolumeDocument{
		PlanID: request.PlanID, DiskID: request.DiskID, Fingerprint: request.Fingerprint,
	}, &response)
	return response, err
}

func (c *Client) SetCredential(ctx context.Context, request accounts.CredentialRequest) error {
	return c.doJSON(ctx, c.operationClient, http.MethodPut, "/v1/accounts/credential", credentialDocument{
		UserID: request.UserID, PrivateSpaceID: request.PrivateSpaceID, Username: request.Username,
		Password: request.Password, Role: request.Role, UID: request.UID, Enabled: request.Enabled,
	}, nil)
}

func (c *Client) GrantViewing(ctx context.Context, request accounts.ViewingRequest) error {
	return c.doJSON(ctx, c.operationClient, http.MethodPost, "/v1/viewing-grants", viewingDocument{
		ID: request.ID, SpaceID: request.SpaceID, AdminUsername: request.AdminUsername,
		AdminUID: request.AdminUID, ExpiresAt: request.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}, nil)
}

func (c *Client) RevokeViewing(ctx context.Context, id string) error {
	return c.doJSON(ctx, c.operationClient, http.MethodDelete, "/v1/viewing-grants/"+url.PathEscape(id), nil, nil)
}

func (c *Client) SyncIdentities(ctx context.Context, identities []accounts.Identity) error {
	document := identitiesDocument{Identities: make([]identityDocument, len(identities))}
	for i, identity := range identities {
		document.Identities[i] = identityDocument{Username: identity.Username, UID: identity.UID, Role: identity.Role, Enabled: identity.Enabled}
	}
	return c.doJSON(ctx, c.operationClient, http.MethodPut, "/v1/accounts/identities", document, nil)
}

func (c *Client) DisableCredential(ctx context.Context, username string) error {
	return c.doJSON(ctx, c.operationClient, http.MethodDelete, "/v1/accounts/credential/"+url.PathEscape(username), nil, nil)
}

func (c *Client) Create(ctx context.Context, spaceID, snapshotID string) ([]files.SnapshotObject, error) {
	var response snapshotObjectsDocument
	if err := c.doJSON(ctx, c.operationClient, http.MethodPost, "/v1/snapshots", snapshotDocument{SpaceID: spaceID, SnapshotID: snapshotID}, &response); err != nil {
		return nil, err
	}
	return response.Items, nil
}

func (c *Client) Open(ctx context.Context, snapshotID, key string) (io.ReadCloser, files.SnapshotObject, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://host-agent/v1/snapshots/"+url.PathEscape(snapshotID)+"/object?key="+url.QueryEscape(key), nil)
	if err != nil {
		return nil, files.SnapshotObject{}, err
	}
	response, err := c.operationClient.Do(request)
	if err != nil {
		return nil, files.SnapshotObject{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil, files.SnapshotObject{}, fmt.Errorf("%w: status %d", ErrUnavailable, response.StatusCode)
	}
	size, err := strconv.ParseInt(response.Header.Get("X-A-NAS-Object-Size"), 10, 64)
	if err != nil {
		_ = response.Body.Close()
		return nil, files.SnapshotObject{}, errors.New("invalid snapshot object size")
	}
	name, err := url.QueryUnescape(response.Header.Get("X-A-NAS-Object-Name"))
	if err != nil {
		_ = response.Body.Close()
		return nil, files.SnapshotObject{}, errors.New("invalid snapshot object name")
	}
	object := files.SnapshotObject{
		Key: response.Header.Get("X-A-NAS-Object-Key"), Name: name,
		Kind: files.EntryKind(response.Header.Get("X-A-NAS-Object-Kind")), SizeBytes: size,
	}
	return response.Body, object, nil
}

func (c *Client) Delete(ctx context.Context, spaceID, snapshotID string) error {
	return c.doJSON(ctx, c.operationClient, http.MethodDelete,
		"/v1/snapshots/"+url.PathEscape(snapshotID)+"?spaceId="+url.QueryEscape(spaceID), nil, nil)
}

func (c *Client) doJSON(ctx context.Context, client *http.Client, method, path string, requestBody, responseBody any) error {
	var body io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://host-agent"+path, body)
	if err != nil {
		return err
	}
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure errorDocument
		_ = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&failure)
		if response.StatusCode == http.StatusConflict && failure.Error.Code == identityConflictCode {
			return accounts.ErrIdentityConflict
		}
		return fmt.Errorf("%w: status %d", ErrUnavailable, response.StatusCode)
	}
	if responseBody == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(responseBody); err != nil {
		return err
	}
	return ensureEndOfJSON(decoder)
}

type metricsDocument struct {
	ObservedAt string                 `json:"observedAt"`
	CPU        cpuMetricsDocument     `json:"cpu"`
	Memory     memoryMetricsDocument  `json:"memory"`
	Network    networkMetricsDocument `json:"network"`
}

type cpuMetricsDocument struct {
	UsagePercent float64 `json:"usagePercent"`
	LogicalCores int     `json:"logicalCores"`
}

type memoryMetricsDocument struct {
	TotalBytes uint64 `json:"totalBytes"`
	UsedBytes  uint64 `json:"usedBytes"`
}

type networkMetricsDocument struct {
	ReceiveBytesPerSecond  uint64 `json:"receiveBytesPerSecond"`
	TransmitBytesPerSecond uint64 `json:"transmitBytesPerSecond"`
}

func metricsDocumentFromDomain(metrics hoststate.Metrics) metricsDocument {
	return metricsDocument{
		ObservedAt: metrics.ObservedAt.UTC().Format(time.RFC3339),
		CPU:        cpuMetricsDocument{UsagePercent: metrics.CPU.UsagePercent, LogicalCores: metrics.CPU.LogicalCores},
		Memory:     memoryMetricsDocument{TotalBytes: metrics.Memory.TotalBytes, UsedBytes: metrics.Memory.UsedBytes},
		Network: networkMetricsDocument{
			ReceiveBytesPerSecond:  metrics.Network.ReceiveBytesPerSecond,
			TransmitBytesPerSecond: metrics.Network.TransmitBytesPerSecond,
		},
	}
}

func (document metricsDocument) toDomain() (hoststate.Metrics, error) {
	observedAt, err := time.Parse(time.RFC3339, document.ObservedAt)
	if err != nil {
		return hoststate.Metrics{}, errors.New("invalid observed time")
	}
	metrics := hoststate.Metrics{
		ObservedAt: observedAt.UTC(),
		CPU:        hoststate.CPUMetrics{UsagePercent: document.CPU.UsagePercent, LogicalCores: document.CPU.LogicalCores},
		Memory:     hoststate.MemoryMetrics{TotalBytes: document.Memory.TotalBytes, UsedBytes: document.Memory.UsedBytes},
		Network: hoststate.NetworkMetrics{
			ReceiveBytesPerSecond:  document.Network.ReceiveBytesPerSecond,
			TransmitBytesPerSecond: document.Network.TransmitBytesPerSecond,
		},
	}
	if !metrics.Valid() {
		return hoststate.Metrics{}, errors.New("inconsistent metrics")
	}
	return metrics, nil
}

type stateDocument struct {
	ObservedAt string         `json:"observedAt"`
	System     systemDocument `json:"system"`
	Disks      []diskDocument `json:"disks"`
}

type systemDocument struct {
	ID              string                  `json:"id"`
	Hostname        string                  `json:"hostname"`
	OperatingSystem operatingSystemDocument `json:"operatingSystem"`
	Architecture    string                  `json:"architecture"`
	UptimeSeconds   uint64                  `json:"uptimeSeconds"`
	Health          hoststate.Health        `json:"health"`
}

type operatingSystemDocument struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type diskDocument struct {
	ID                 string              `json:"id"`
	Model              string              `json:"model"`
	Transport          hoststate.Transport `json:"transport"`
	CapacityBytes      uint64              `json:"capacityBytes"`
	Rotational         bool                `json:"rotational"`
	Removable          bool                `json:"removable"`
	InUse              bool                `json:"inUse"`
	Filesystems        []string            `json:"filesystems"`
	Role               hoststate.DiskRole  `json:"role"`
	Health             hoststate.Health    `json:"health"`
	SMARTStatus        hoststate.Health    `json:"smartStatus"`
	TemperatureCelsius *int                `json:"temperatureCelsius,omitempty"`
}

func documentFromDomain(state hoststate.State) stateDocument {
	disks := make([]diskDocument, len(state.Disks))
	for i, disk := range state.Disks {
		disks[i] = diskDocument{
			ID:                 disk.ID.String(),
			Model:              disk.Model,
			Transport:          disk.Transport,
			CapacityBytes:      disk.CapacityBytes,
			Rotational:         disk.Rotational,
			Removable:          disk.Removable,
			InUse:              disk.InUse,
			Filesystems:        append([]string(nil), disk.Filesystems...),
			Role:               disk.Role,
			Health:             disk.Health,
			SMARTStatus:        disk.SMARTStatus,
			TemperatureCelsius: disk.TemperatureCelsius,
		}
	}
	return stateDocument{
		ObservedAt: state.ObservedAt.UTC().Format(time.RFC3339),
		System: systemDocument{
			ID:       state.System.ID.String(),
			Hostname: state.System.Hostname,
			OperatingSystem: operatingSystemDocument{
				Name:    state.System.OperatingSystem.Name,
				Version: state.System.OperatingSystem.Version,
			},
			Architecture:  state.System.Architecture,
			UptimeSeconds: state.System.UptimeSeconds,
			Health:        state.System.Health,
		},
		Disks: disks,
	}
}

func (document stateDocument) toDomain() (hoststate.State, error) {
	observedAt, err := time.Parse(time.RFC3339, document.ObservedAt)
	if err != nil {
		return hoststate.State{}, errors.New("invalid observed time")
	}
	hostID, err := hoststate.NewResourceID(document.System.ID)
	if err != nil {
		return hoststate.State{}, errors.New("invalid host ID")
	}
	if strings.TrimSpace(document.System.Hostname) == "" || strings.TrimSpace(document.System.Architecture) == "" {
		return hoststate.State{}, errors.New("invalid system identity")
	}
	if strings.TrimSpace(document.System.OperatingSystem.Name) == "" || strings.TrimSpace(document.System.OperatingSystem.Version) == "" {
		return hoststate.State{}, errors.New("invalid operating system")
	}
	if !validHealth(document.System.Health) {
		return hoststate.State{}, errors.New("invalid system health")
	}

	disks := make([]hoststate.Disk, len(document.Disks))
	seen := make(map[string]struct{}, len(document.Disks))
	for i, disk := range document.Disks {
		id, err := hoststate.NewResourceID(disk.ID)
		if err != nil {
			return hoststate.State{}, errors.New("invalid disk ID")
		}
		if _, exists := seen[id.String()]; exists {
			return hoststate.State{}, errors.New("duplicate disk ID")
		}
		seen[id.String()] = struct{}{}
		if strings.TrimSpace(disk.Model) == "" || disk.CapacityBytes == 0 || !validTransport(disk.Transport) || !validRole(disk.Role) || !validHealth(disk.Health) || !validHealth(disk.SMARTStatus) {
			return hoststate.State{}, errors.New("invalid disk state")
		}
		for _, filesystem := range disk.Filesystems {
			if strings.TrimSpace(filesystem) == "" {
				return hoststate.State{}, errors.New("invalid disk filesystem evidence")
			}
		}
		disks[i] = hoststate.Disk{
			ID:                 id,
			Model:              disk.Model,
			Transport:          disk.Transport,
			CapacityBytes:      disk.CapacityBytes,
			Rotational:         disk.Rotational,
			Removable:          disk.Removable,
			InUse:              disk.InUse,
			Filesystems:        append([]string(nil), disk.Filesystems...),
			Role:               disk.Role,
			Health:             disk.Health,
			SMARTStatus:        disk.SMARTStatus,
			TemperatureCelsius: disk.TemperatureCelsius,
		}
	}
	return hoststate.State{
		ObservedAt: observedAt.UTC(),
		System: hoststate.System{
			ID:       hostID,
			Hostname: document.System.Hostname,
			OperatingSystem: hoststate.OperatingSystem{
				Name:    document.System.OperatingSystem.Name,
				Version: document.System.OperatingSystem.Version,
			},
			Architecture:  document.System.Architecture,
			UptimeSeconds: document.System.UptimeSeconds,
			Health:        document.System.Health,
		},
		Disks: disks,
	}, nil
}

func validHealth(value hoststate.Health) bool {
	switch value {
	case hoststate.HealthHealthy, hoststate.HealthWarning, hoststate.HealthCritical, hoststate.HealthUnknown:
		return true
	default:
		return false
	}
}

func validRole(value hoststate.DiskRole) bool {
	switch value {
	case hoststate.DiskRoleSystem, hoststate.DiskRoleData, hoststate.DiskRoleUnassigned:
		return true
	default:
		return false
	}
}

func validTransport(value hoststate.Transport) bool {
	switch value {
	case hoststate.TransportNVMe, hoststate.TransportSATA, hoststate.TransportUSB, hoststate.TransportUnknown:
		return true
	default:
		return false
	}
}

func ensureEndOfJSON(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("decode host agent response: multiple JSON values")
		}
		return fmt.Errorf("decode host agent response: %w", err)
	}
	return nil
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorDocument struct {
	Error errorDetail `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorDocument{Error: errorDetail{Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		panic(err)
	}
}

var _ hoststate.Observer = (*Client)(nil)
var _ storage.VolumeExecutor = (*Client)(nil)
var _ accounts.CredentialProvisioner = (*Client)(nil)
var _ accounts.IdentitySynchronizer = (*Client)(nil)
var _ accounts.ViewingProvisioner = (*Client)(nil)
var _ files.SnapshotBackend = (*Client)(nil)
