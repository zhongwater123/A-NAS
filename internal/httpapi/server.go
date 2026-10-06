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
	reader         hoststate.Reader
	productVersion string
	logger         *slog.Logger
}

func New(reader hoststate.Reader, productVersion string, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &handler{
		reader:         reader,
		productVersion: productVersion,
		logger:         logger,
	}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/healthz", "/api/v1/system", "/api/v1/disks":
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
