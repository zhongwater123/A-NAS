// Package httpapi composes the product API: the route table and the guard
// that enforces each route's posture (routes.go), setting up the device,
// signing in and sessions (product.go), the terminal and the host state
// (this file). Each domain's own routes live in its internal/<domain>api
// package.
package httpapi

import (
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
)

type handler struct {
	reader         hoststate.Observer
	dataSource     DataSource
	productVersion string
	logger         *slog.Logger
	// dataVolumeExpected says a data volume was created, so the health
	// assessment can tell an offline volume from none; nil means none.
	dataVolumeExpected func() bool
}

type DataSource string

const (
	DataSourceSimulated DataSource = "simulated"
	DataSourceLive      DataSource = "live"
)

func newStateHandler(reader hoststate.Observer, dataSource DataSource, productVersion string, logger *slog.Logger) *handler {
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

// assess rates the observed system (docs/specs/system-health.md).
func (h *handler) assess(state hoststate.State) systemHealth {
	expected := hoststate.Expectations{DataVolume: h.dataVolumeExpected != nil && h.dataVolumeExpected()}
	health, findings := hoststate.Assess(state, expected)
	items := make([]healthFindingResponse, len(findings))
	for i, finding := range findings {
		items[i] = healthFindingResponse{Code: finding.Code, Severity: finding.Severity, DiskID: finding.DiskID.String(), Value: finding.Value}
	}
	return systemHealth{health: health, findings: items}
}

type systemHealth struct {
	health   hoststate.Health
	findings []healthFindingResponse
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/healthz", "/api/v1/system", "/api/v1/disks", "/api/v1/host-state", "/api/v1/metrics":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			httpjson.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
	default:
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}

	if r.URL.Path == "/healthz" {
		httpjson.Write(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if r.URL.Path == "/api/v1/system" {
		state, err := h.reader.Read(r.Context())
		if err != nil {
			h.writeStateUnavailable(w, r, err)
			return
		}
		assessed := h.assess(state)
		httpjson.Write(w, http.StatusOK, systemResponse{
			ID:       state.System.ID.String(),
			Hostname: state.System.Hostname,
			OperatingSystem: operatingSystemResponse{
				Name:    state.System.OperatingSystem.Name,
				Version: state.System.OperatingSystem.Version,
			},
			Architecture:   state.System.Architecture,
			UptimeSeconds:  state.System.UptimeSeconds,
			Health:         assessed.health,
			HealthFindings: assessed.findings,
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
			items[i] = mapDisk(disk)
		}
		sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
		httpjson.Write(w, http.StatusOK, disksResponse{
			ObservedAt: state.ObservedAt.UTC().Format(time.RFC3339),
			Items:      items,
		})
		return
	}
	if r.URL.Path == "/api/v1/metrics" {
		metrics, err := h.reader.ReadMetrics(r.Context())
		if err != nil {
			h.logger.ErrorContext(r.Context(), "host metrics read failed", "path", r.URL.Path, "error", err)
			httpjson.WriteError(w, http.StatusServiceUnavailable, "metrics_unavailable", "host metrics are unavailable")
			return
		}
		// Utilisation changes every poll; intermediaries must not reuse it.
		w.Header().Set("Cache-Control", "no-store")
		httpjson.Write(w, http.StatusOK, metricsResponse{
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
		httpjson.Write(w, http.StatusOK, hostStateResponse{
			DataSource:     h.dataSource,
			ProductVersion: h.productVersion,
			ObservedAt:     state.ObservedAt.UTC().Format(time.RFC3339),
			System:         mapSystem(state.System, h.assess(state)),
			Disks:          mapDisks(state.Disks),
		})
		return
	}
}

func (h *handler) writeStateUnavailable(w http.ResponseWriter, r *http.Request, err error) {
	h.logger.ErrorContext(r.Context(), "host state read failed", "path", r.URL.Path, "error", err)
	httpjson.WriteError(w, http.StatusServiceUnavailable, "state_unavailable", "host state is unavailable")
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
	HealthFindings  []healthFindingResponse `json:"healthFindings"`
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
	HealthFindings  []healthFindingResponse `json:"healthFindings"`
}

// healthFindingResponse is one reason the system is not plainly healthy.
type healthFindingResponse struct {
	Code     hoststate.FindingCode `json:"code"`
	Severity hoststate.Health      `json:"severity"`
	DiskID   string                `json:"diskId,omitempty"`
	Value    *int64                `json:"value,omitempty"`
}

type diskResponse struct {
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
	Eligible           bool                `json:"eligibleForDataVolume"`
	IneligibleReasons  []string            `json:"ineligibleReasons"`
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

func mapSystem(system hoststate.System, assessed systemHealth) systemStateResponse {
	return systemStateResponse{
		ID:       system.ID.String(),
		Hostname: system.Hostname,
		OperatingSystem: operatingSystemResponse{
			Name:    system.OperatingSystem.Name,
			Version: system.OperatingSystem.Version,
		},
		Architecture:   system.Architecture,
		UptimeSeconds:  system.UptimeSeconds,
		Health:         assessed.health,
		HealthFindings: assessed.findings,
	}
}

func mapDisks(disks []hoststate.Disk) []diskResponse {
	items := make([]diskResponse, len(disks))
	for i, disk := range disks {
		items[i] = mapDisk(disk)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

func mapDisk(disk hoststate.Disk) diskResponse {
	filesystems := append([]string(nil), disk.Filesystems...)
	if filesystems == nil {
		filesystems = []string{}
	}
	reasons := make([]string, 0, 4)
	for _, issue := range disk.ObservationIssues {
		reasons = append(reasons, string(issue))
	}
	switch disk.Role {
	case hoststate.DiskRoleSystem:
		reasons = append(reasons, "system_disk")
	case hoststate.DiskRoleData:
		reasons = append(reasons, "data_volume")
	}
	if disk.Transport == hoststate.TransportUSB || disk.Transport == hoststate.TransportUnknown {
		reasons = append(reasons, "unsupported_transport")
	}
	if disk.Removable {
		reasons = append(reasons, "removable")
	}
	if disk.InUse {
		reasons = append(reasons, "in_use")
	}
	return diskResponse{
		ID: disk.ID.String(), Model: disk.Model, Transport: disk.Transport,
		CapacityBytes: disk.CapacityBytes, Rotational: disk.Rotational,
		Removable: disk.Removable, InUse: disk.InUse, Filesystems: filesystems,
		Role: disk.Role, Health: disk.Health, SMARTStatus: disk.SMARTStatus,
		Eligible:          len(reasons) == 0 && disk.Role == hoststate.DiskRoleUnassigned,
		IneligibleReasons: reasons, TemperatureCelsius: disk.TemperatureCelsius,
	}
}
