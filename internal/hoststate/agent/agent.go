// Package agent transports host state between the Product Service and Host Agent.
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
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
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
	httpClient *http.Client
}

func NewClient(socketPath string) *Client {
	dialer := &net.Dialer{Timeout: requestTimeout}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socketPath)
		},
		DisableKeepAlives: true,
	}
	return &Client{httpClient: &http.Client{Transport: transport, Timeout: requestTimeout}}
}

func (c *Client) Read(ctx context.Context) (hoststate.State, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://host-agent/v1/state", nil)
	if err != nil {
		return hoststate.State{}, fmt.Errorf("create host agent request: %w", err)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return hoststate.State{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return hoststate.State{}, fmt.Errorf("%w: status %d", ErrUnavailable, response.StatusCode)
	}

	var document stateDocument
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return hoststate.State{}, fmt.Errorf("decode host agent response: %w", err)
	}
	if err := ensureEndOfJSON(decoder); err != nil {
		return hoststate.State{}, err
	}
	state, err := document.toDomain()
	if err != nil {
		return hoststate.State{}, fmt.Errorf("validate host agent response: %w", err)
	}
	return state, nil
}

type handler struct {
	reader hoststate.Reader
	logger *slog.Logger
}

func NewHandler(reader hoststate.Reader, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &handler{reader: reader, logger: logger}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/state" {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	state, err := h.reader.Read(r.Context())
	if err != nil {
		h.logger.ErrorContext(r.Context(), "host state read failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "state_unavailable", "host state is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, documentFromDomain(state))
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
	Role               hoststate.DiskRole  `json:"role"`
	Health             hoststate.Health    `json:"health"`
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
			Role:               disk.Role,
			Health:             disk.Health,
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
		if strings.TrimSpace(disk.Model) == "" || disk.CapacityBytes == 0 || !validTransport(disk.Transport) || !validRole(disk.Role) || !validHealth(disk.Health) {
			return hoststate.State{}, errors.New("invalid disk state")
		}
		disks[i] = hoststate.Disk{
			ID:                 id,
			Model:              disk.Model,
			Transport:          disk.Transport,
			CapacityBytes:      disk.CapacityBytes,
			Rotational:         disk.Rotational,
			Role:               disk.Role,
			Health:             disk.Health,
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

var _ hoststate.Reader = (*Client)(nil)
