// Package agent carries container management between the Product Service and
// the container agent over HTTP/JSON on a Unix socket. The server accepts only
// the typed operations of containers.Manager; there is no generic Docker API
// passthrough.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/containers"
)

const (
	defaultSocketPath = "/run/a-nas-container/agent.sock"
	socketEnvironment = "ANAS_CONTAINER_AGENT_SOCKET"
	// Stats sampling alone takes about a second; stop and restart wait up to ten.
	requestTimeout = 20 * time.Second
)

func SocketPath() (string, error) {
	configured := strings.TrimSpace(os.Getenv(socketEnvironment))
	if configured == "" {
		return defaultSocketPath, nil
	}
	if !filepath.IsAbs(configured) {
		return "", errors.New("ANAS_CONTAINER_AGENT_SOCKET must be an absolute path")
	}
	return filepath.Clean(configured), nil
}

// Client implements containers.Manager by calling the container agent.
type Client struct {
	httpClient *http.Client
}

func NewClient(socketPath string) *Client {
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socketPath)
		},
		DisableKeepAlives: true,
	}
	return &Client{httpClient: &http.Client{Transport: transport, Timeout: requestTimeout}}
}

func (c *Client) Snapshot(ctx context.Context) (containers.Snapshot, error) {
	var document SnapshotDocument
	if err := c.do(ctx, http.MethodGet, "/v1/snapshot", &document); err != nil {
		return containers.Snapshot{}, err
	}
	return document.ToDomain()
}

func (c *Client) Act(ctx context.Context, id string, action containers.Action) error {
	if !containers.ValidID(id) {
		return containers.ErrInvalidID
	}
	if !action.Valid() {
		return containers.ErrInvalidAction
	}
	return c.do(ctx, http.MethodPost, "/v1/containers/"+id+"/"+string(action), nil)
}

func (c *Client) Logs(ctx context.Context, id string, tail int) ([]containers.LogLine, error) {
	if !containers.ValidID(id) {
		return nil, containers.ErrInvalidID
	}
	var document LogsDocument
	path := "/v1/containers/" + id + "/logs?tail=" + strconv.Itoa(containers.ClampTail(tail))
	if err := c.do(ctx, http.MethodGet, path, &document); err != nil {
		return nil, err
	}
	return document.ToDomain()
}

func (c *Client) do(ctx context.Context, method, path string, into any) error {
	request, err := http.NewRequestWithContext(ctx, method, "http://container-agent"+path, nil)
	if err != nil {
		return err
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("%w: %v", containers.ErrUnavailable, err)
	}
	defer response.Body.Close()
	body := io.LimitReader(response.Body, 4<<20)
	if response.StatusCode >= 300 {
		var failure errorDocument
		_ = json.NewDecoder(body).Decode(&failure)
		return errorFromCode(failure.Error.Code, response.StatusCode)
	}
	if into == nil {
		_, _ = io.Copy(io.Discard, body)
		return nil
	}
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return fmt.Errorf("decode container agent response: %w", err)
	}
	return nil
}

type handler struct {
	manager containers.Manager
	logger  *slog.Logger
}

func NewHandler(manager containers.Manager, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &handler{manager: manager, logger: logger}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/snapshot" {
		if !allowMethod(w, r, http.MethodGet) {
			return
		}
		snapshot, err := h.manager.Snapshot(r.Context())
		if err != nil {
			h.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, SnapshotFromDomain(snapshot))
		return
	}

	id, operation, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v1/containers/"), "/")
	if !strings.HasPrefix(r.URL.Path, "/v1/containers/") || !ok || strings.Contains(operation, "/") {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	if operation == "logs" {
		if !allowMethod(w, r, http.MethodGet) {
			return
		}
		tail, _ := strconv.Atoi(r.URL.Query().Get("tail"))
		lines, err := h.manager.Logs(r.Context(), id, tail)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, LogsFromDomain(lines))
		return
	}
	action := containers.Action(operation)
	if !action.Valid() {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	if !allowMethod(w, r, http.MethodPost) {
		return
	}
	if err := h.manager.Act(r.Context(), id, action); err != nil {
		h.fail(w, r, err)
		return
	}
	h.logger.InfoContext(r.Context(), "container action applied", "container", id, "action", action)
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	status, code := statusForError(err)
	if status >= 500 {
		h.logger.ErrorContext(r.Context(), "container operation failed", "path", r.URL.Path, "error", err)
	}
	writeError(w, status, code, err.Error())
}

// statusForError maps domain errors to the codes shared by the agent and the
// product API, so the desktop can tell a missing container from a down engine.
func statusForError(err error) (int, string) {
	switch {
	case errors.Is(err, containers.ErrInvalidID), errors.Is(err, containers.ErrInvalidAction):
		return http.StatusBadRequest, "invalid_request"
	case errors.Is(err, containers.ErrNotFound):
		return http.StatusNotFound, "container_not_found"
	case errors.Is(err, containers.ErrUnavailable):
		return http.StatusServiceUnavailable, "containers_unavailable"
	default:
		return http.StatusBadGateway, "container_engine_error"
	}
}

// StatusForError exposes the shared error mapping to the product API.
func StatusForError(err error) (int, string) {
	return statusForError(err)
}

func errorFromCode(code string, status int) error {
	switch code {
	case "invalid_request":
		return containers.ErrInvalidID
	case "container_not_found":
		return containers.ErrNotFound
	case "containers_unavailable":
		return containers.ErrUnavailable
	default:
		return fmt.Errorf("container agent returned status %d (%s)", status, code)
	}
}

func allowMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	return false
}

type SnapshotDocument struct {
	ObservedAt string              `json:"observedAt"`
	Engine     EngineDocument      `json:"engine"`
	Containers []ContainerDocument `json:"containers"`
	Images     []ImageDocument     `json:"images"`
}

type EngineDocument struct {
	Version    string `json:"version"`
	APIVersion string `json:"apiVersion"`
}

type ContainerDocument struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Image     string           `json:"image"`
	State     containers.State `json:"state"`
	Status    string           `json:"status"`
	CreatedAt string           `json:"createdAt"`
	Ports     []PortDocument   `json:"ports"`
	Project   string           `json:"project,omitempty"`
	Usage     *UsageDocument   `json:"usage,omitempty"`
}

type PortDocument struct {
	HostIP        string `json:"hostIp,omitempty"`
	HostPort      uint16 `json:"hostPort,omitempty"`
	ContainerPort uint16 `json:"containerPort"`
	Protocol      string `json:"protocol"`
}

type UsageDocument struct {
	CPUPercent       float64 `json:"cpuPercent"`
	MemoryBytes      uint64  `json:"memoryBytes"`
	MemoryLimitBytes uint64  `json:"memoryLimitBytes"`
}

type ImageDocument struct {
	ID        string   `json:"id"`
	Tags      []string `json:"tags"`
	SizeBytes int64    `json:"sizeBytes"`
	CreatedAt string   `json:"createdAt"`
}

type LogsDocument struct {
	Lines []LogLineDocument `json:"lines"`
}

type LogLineDocument struct {
	Stream string `json:"stream"`
	Time   string `json:"time,omitempty"`
	Text   string `json:"text"`
}

func SnapshotFromDomain(snapshot containers.Snapshot) SnapshotDocument {
	items := make([]ContainerDocument, len(snapshot.Containers))
	for i, item := range snapshot.Containers {
		ports := make([]PortDocument, len(item.Ports))
		for j, port := range item.Ports {
			ports[j] = PortDocument{HostIP: port.HostIP, HostPort: port.HostPort, ContainerPort: port.ContainerPort, Protocol: port.Protocol}
		}
		var usage *UsageDocument
		if item.Usage != nil {
			usage = &UsageDocument{CPUPercent: item.Usage.CPUPercent, MemoryBytes: item.Usage.MemoryBytes, MemoryLimitBytes: item.Usage.MemoryLimitBytes}
		}
		items[i] = ContainerDocument{
			ID: item.ID, Name: item.Name, Image: item.Image, State: item.State, Status: item.Status,
			CreatedAt: item.CreatedAt.UTC().Format(time.RFC3339), Ports: ports, Project: item.Project, Usage: usage,
		}
	}
	images := make([]ImageDocument, len(snapshot.Images))
	for i, image := range snapshot.Images {
		tags := image.Tags
		if tags == nil {
			tags = []string{}
		}
		images[i] = ImageDocument{ID: image.ID, Tags: tags, SizeBytes: image.SizeBytes, CreatedAt: image.CreatedAt.UTC().Format(time.RFC3339)}
	}
	return SnapshotDocument{
		ObservedAt: snapshot.ObservedAt.UTC().Format(time.RFC3339),
		Engine:     EngineDocument{Version: snapshot.Engine.Version, APIVersion: snapshot.Engine.APIVersion},
		Containers: items,
		Images:     images,
	}
}

func (document SnapshotDocument) ToDomain() (containers.Snapshot, error) {
	observedAt, err := time.Parse(time.RFC3339, document.ObservedAt)
	if err != nil {
		return containers.Snapshot{}, errors.New("invalid observed time")
	}
	snapshot := containers.Snapshot{
		ObservedAt: observedAt.UTC(),
		Engine:     containers.Engine{Version: document.Engine.Version, APIVersion: document.Engine.APIVersion},
		Containers: make([]containers.Container, len(document.Containers)),
		Images:     make([]containers.Image, len(document.Images)),
	}
	for i, item := range document.Containers {
		createdAt, err := time.Parse(time.RFC3339, item.CreatedAt)
		if err != nil || !containers.ValidID(item.ID) || !item.State.Valid() || strings.TrimSpace(item.Name) == "" {
			return containers.Snapshot{}, errors.New("invalid container")
		}
		ports := make([]containers.Port, len(item.Ports))
		for j, port := range item.Ports {
			ports[j] = containers.Port{HostIP: port.HostIP, HostPort: port.HostPort, ContainerPort: port.ContainerPort, Protocol: port.Protocol}
		}
		var usage *containers.Usage
		if item.Usage != nil {
			usage = &containers.Usage{CPUPercent: item.Usage.CPUPercent, MemoryBytes: item.Usage.MemoryBytes, MemoryLimitBytes: item.Usage.MemoryLimitBytes}
		}
		snapshot.Containers[i] = containers.Container{
			ID: item.ID, Name: item.Name, Image: item.Image, State: item.State, Status: item.Status,
			CreatedAt: createdAt.UTC(), Ports: ports, Project: item.Project, Usage: usage,
		}
	}
	for i, image := range document.Images {
		createdAt, err := time.Parse(time.RFC3339, image.CreatedAt)
		if err != nil || image.ID == "" {
			return containers.Snapshot{}, errors.New("invalid image")
		}
		snapshot.Images[i] = containers.Image{ID: image.ID, Tags: image.Tags, SizeBytes: image.SizeBytes, CreatedAt: createdAt.UTC()}
	}
	return snapshot, nil
}

func LogsFromDomain(lines []containers.LogLine) LogsDocument {
	document := LogsDocument{Lines: make([]LogLineDocument, len(lines))}
	for i, line := range lines {
		stamp := ""
		if !line.Time.IsZero() {
			stamp = line.Time.UTC().Format(time.RFC3339Nano)
		}
		document.Lines[i] = LogLineDocument{Stream: line.Stream, Time: stamp, Text: line.Text}
	}
	return document
}

func (document LogsDocument) ToDomain() ([]containers.LogLine, error) {
	lines := make([]containers.LogLine, len(document.Lines))
	for i, line := range document.Lines {
		if line.Stream != "stdout" && line.Stream != "stderr" {
			return nil, errors.New("invalid log stream")
		}
		lines[i] = containers.LogLine{Stream: line.Stream, Text: line.Text}
		if line.Time != "" {
			parsed, err := time.Parse(time.RFC3339Nano, line.Time)
			if err != nil {
				return nil, errors.New("invalid log time")
			}
			lines[i].Time = parsed.UTC()
		}
	}
	return lines, nil
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

var _ containers.Manager = (*Client)(nil)
