// Package httpapi exposes read-only A-NAS product endpoints over HTTP.
package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
)

type handler struct {
	reader         hoststate.Observer
	dataSource     DataSource
	productVersion string
	logger         *slog.Logger
}

type DataSource string

const (
	DataSourceSimulated DataSource = "simulated"
	DataSourceLive      DataSource = "live"
)

func New(reader hoststate.Observer, dataSource DataSource, productVersion string, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &handler{
		reader:         reader,
		dataSource:     dataSource,
		productVersion: productVersion,
		logger:         logger,
	}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/healthz", "/api/v1/system", "/api/v1/disks", "/api/v1/host-state", "/api/v1/metrics":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
	default:
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}

	if r.URL.Path == "/healthz" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if r.URL.Path == "/api/v1/system" {
		state, err := h.reader.Read(r.Context())
		if err != nil {
			h.writeStateUnavailable(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, systemResponse{
			ID:       state.System.ID.String(),
			Hostname: state.System.Hostname,
			OperatingSystem: operatingSystemResponse{
				Name:    state.System.OperatingSystem.Name,
				Version: state.System.OperatingSystem.Version,
			},
			Architecture:   state.System.Architecture,
			UptimeSeconds:  state.System.UptimeSeconds,
			Health:         state.System.Health,
			ProductVersion: h.productVersion,
			ObservedAt:     state.ObservedAt.UTC().Format(time.RFC3339),
		})
		return
	}
	if r.URL.Path == "/api/v1/disks" {
		state, err := h.reader.Read(r.Context())
		if err != nil {
			h.writeStateUnavailable(w, r, err)
			return
		}
		items := make([]diskResponse, len(state.Disks))
		for i, disk := range state.Disks {
			items[i] = diskResponse{
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
		sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
		writeJSON(w, http.StatusOK, disksResponse{
			ObservedAt: state.ObservedAt.UTC().Format(time.RFC3339),
			Items:      items,
		})
		return
	}
	if r.URL.Path == "/api/v1/metrics" {
		metrics, err := h.reader.ReadMetrics(r.Context())
		if err != nil {
			h.logger.ErrorContext(r.Context(), "host metrics read failed", "path", r.URL.Path, "error", err)
			writeError(w, http.StatusServiceUnavailable, "metrics_unavailable", "host metrics are unavailable")
			return
		}
		// Utilisation changes every poll; intermediaries must not reuse it.
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, metricsResponse{
			DataSource: h.dataSource,
			ObservedAt: metrics.ObservedAt.UTC().Format(time.RFC3339),
			CPU:        cpuMetricsResponse{UsagePercent: metrics.CPU.UsagePercent, LogicalCores: metrics.CPU.LogicalCores},
			Memory:     memoryMetricsResponse{TotalBytes: metrics.Memory.TotalBytes, UsedBytes: metrics.Memory.UsedBytes},
			Network: networkMetricsResponse{
				ReceiveBytesPerSecond:  metrics.Network.ReceiveBytesPerSecond,
				TransmitBytesPerSecond: metrics.Network.TransmitBytesPerSecond,
			},
		})
		return
	}
	if r.URL.Path == "/api/v1/host-state" {
		state, err := h.reader.Read(r.Context())
		if err != nil {
			h.writeStateUnavailable(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, hostStateResponse{
			DataSource:     h.dataSource,
			ProductVersion: h.productVersion,
			ObservedAt:     state.ObservedAt.UTC().Format(time.RFC3339),
			System:         mapSystem(state.System),
			Disks:          mapDisks(state.Disks),
		})
		return
	}
}

func (h *handler) writeStateUnavailable(w http.ResponseWriter, r *http.Request, err error) {
	h.logger.ErrorContext(r.Context(), "host state read failed", "path", r.URL.Path, "error", err)
	writeError(w, http.StatusServiceUnavailable, "state_unavailable", "host state is unavailable")
}

type operatingSystemResponse struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type systemResponse struct {
	ID              string                  `json:"id"`
	Hostname        string                  `json:"hostname"`
	OperatingSystem operatingSystemResponse `json:"operatingSystem"`
	Architecture    string                  `json:"architecture"`
	UptimeSeconds   uint64                  `json:"uptimeSeconds"`
	Health          hoststate.Health        `json:"health"`
	ProductVersion  string                  `json:"productVersion"`
	ObservedAt      string                  `json:"observedAt"`
}

type systemStateResponse struct {
	ID              string                  `json:"id"`
	Hostname        string                  `json:"hostname"`
	OperatingSystem operatingSystemResponse `json:"operatingSystem"`
	Architecture    string                  `json:"architecture"`
	UptimeSeconds   uint64                  `json:"uptimeSeconds"`
	Health          hoststate.Health        `json:"health"`
}

type diskResponse struct {
	ID                 string              `json:"id"`
	Model              string              `json:"model"`
	Transport          hoststate.Transport `json:"transport"`
	CapacityBytes      uint64              `json:"capacityBytes"`
	Rotational         bool                `json:"rotational"`
	Role               hoststate.DiskRole  `json:"role"`
	Health             hoststate.Health    `json:"health"`
	TemperatureCelsius *int                `json:"temperatureCelsius,omitempty"`
}

type disksResponse struct {
	ObservedAt string         `json:"observedAt"`
	Items      []diskResponse `json:"items"`
}

type hostStateResponse struct {
	DataSource     DataSource          `json:"dataSource"`
	ProductVersion string              `json:"productVersion"`
	ObservedAt     string              `json:"observedAt"`
	System         systemStateResponse `json:"system"`
	Disks          []diskResponse      `json:"disks"`
}

type metricsResponse struct {
	DataSource DataSource             `json:"dataSource"`
	ObservedAt string                 `json:"observedAt"`
	CPU        cpuMetricsResponse     `json:"cpu"`
	Memory     memoryMetricsResponse  `json:"memory"`
	Network    networkMetricsResponse `json:"network"`
}

type cpuMetricsResponse struct {
	UsagePercent float64 `json:"usagePercent"`
	LogicalCores int     `json:"logicalCores"`
}

type memoryMetricsResponse struct {
	TotalBytes uint64 `json:"totalBytes"`
	UsedBytes  uint64 `json:"usedBytes"`
}

type networkMetricsResponse struct {
	ReceiveBytesPerSecond  uint64 `json:"receiveBytesPerSecond"`
	TransmitBytesPerSecond uint64 `json:"transmitBytesPerSecond"`
}

func mapSystem(system hoststate.System) systemStateResponse {
	return systemStateResponse{
		ID:       system.ID.String(),
		Hostname: system.Hostname,
		OperatingSystem: operatingSystemResponse{
			Name:    system.OperatingSystem.Name,
			Version: system.OperatingSystem.Version,
		},
		Architecture:  system.Architecture,
		UptimeSeconds: system.UptimeSeconds,
		Health:        system.Health,
	}
}

func mapDisks(disks []hoststate.Disk) []diskResponse {
	items := make([]diskResponse, len(disks))
	for i, disk := range disks {
		items[i] = diskResponse{
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
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorResponse struct {
	Error errorDetail `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Error: errorDetail{Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		panic(err)
	}
}
