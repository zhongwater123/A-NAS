// Package wire holds the documents, paths and error codes that the Host
// Agent and its client in internal/hoststate/agent exchange (ADR 0004).
package wire

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/datavolume"
	"github.com/zhongwater123/A-NAS/internal/filesystem"
	"github.com/zhongwater123/A-NAS/internal/hoststate"
)

const (
	IdentityConflictCode  = "identity_conflict"
	VolumeUnavailableCode = "volume_unavailable"
	SnapshotNotFoundCode  = "snapshot_not_found"
)

type CreateVolumeDocument struct {
	PlanID string                  `json:"planId"`
	Disks  []datavolume.DiskTarget `json:"disks"`
	Layout datavolume.Layout       `json:"layout"`
}

type AddDisksDocument struct {
	PlanID         string                  `json:"planId"`
	FilesystemUUID string                  `json:"filesystemUuid"`
	Disks          []datavolume.DiskTarget `json:"disks"`
	Layout         datavolume.Layout       `json:"layout"`
}

const (
	NoConversionCode      = "no_conversion"
	OperationNotFoundCode = "operation_not_found"
	OperationConflictCode = "operation_conflict"
	VolumeOperationsPath  = "/v1/storage/operations/"
)

type CredentialDocument struct {
	UserID         string         `json:"userId"`
	PrivateSpaceID string         `json:"privateSpaceId"`
	Username       string         `json:"username"`
	Password       string         `json:"password"`
	Role           accountid.Role `json:"role"`
	UID            int            `json:"uid"`
	Enabled        bool           `json:"enabled"`
}

type IdentityDocument struct {
	Username string         `json:"username"`
	UID      int            `json:"uid"`
	Role     accountid.Role `json:"role"`
	Enabled  bool           `json:"enabled"`
}

type ViewingDocument struct {
	ID            string `json:"id"`
	SpaceID       string `json:"spaceId"`
	AdminUsername string `json:"adminUsername"`
	AdminUID      int    `json:"adminUid"`
	ExpiresAt     string `json:"expiresAt"`
}

type IdentitiesDocument struct {
	Identities []IdentityDocument `json:"identities"`
}

type SnapshotDocument struct {
	SpaceID    string `json:"spaceId"`
	SnapshotID string `json:"snapshotId"`
}

// SnapshotSurveyPath reports which snapshots exist; it deletes nothing.
const SnapshotSurveyPath = "/v1/snapshot-survey"

type SnapshotSurveyDocument struct {
	Known []string `json:"known"`
}

// snapshotObjectLine is one line of a snapshot object listing: an object,
// the end of the listing, or the error that cut it short.
type SnapshotObjectLine struct {
	Object *filesystem.SnapshotObject `json:"object,omitempty"`
	End    *SnapshotObjectsEnd        `json:"end,omitempty"`
	Error  *ErrorDetail               `json:"error,omitempty"`
}

type SnapshotObjectsEnd struct {
	Count int `json:"count"`
}

type MetricsDocument struct {
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

func MetricsDocumentFromDomain(metrics hoststate.Metrics) MetricsDocument {
	return MetricsDocument{
		ObservedAt: metrics.ObservedAt.UTC().Format(time.RFC3339),
		CPU:        cpuMetricsDocument{UsagePercent: metrics.CPU.UsagePercent, LogicalCores: metrics.CPU.LogicalCores},
		Memory:     memoryMetricsDocument{TotalBytes: metrics.Memory.TotalBytes, UsedBytes: metrics.Memory.UsedBytes},
		Network: networkMetricsDocument{
			ReceiveBytesPerSecond:  metrics.Network.ReceiveBytesPerSecond,
			TransmitBytesPerSecond: metrics.Network.TransmitBytesPerSecond,
		},
	}
}

func (document MetricsDocument) ToDomain() (hoststate.Metrics, error) {
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

type StateDocument struct {
	ObservedAt string              `json:"observedAt"`
	System     systemDocument      `json:"system"`
	Disks      []DiskDocument      `json:"disks"`
	DataVolume *dataVolumeDocument `json:"dataVolume,omitempty"`
	// ReconcileFailed is left out unless set, so a Product Service that
	// predates it reads a healthy Host Agent's state as before.
	ReconcileFailed bool `json:"reconcileFailed,omitempty"`
}

type dataVolumeDocument struct {
	FilesystemUUID string                    `json:"filesystemUuid"`
	DiskIDs        []string                  `json:"diskIds"`
	Data           []profileUsageDocument    `json:"data"`
	Metadata       []profileUsageDocument    `json:"metadata"`
	Operation      hoststate.VolumeOperation `json:"operation,omitempty"`
}

type profileUsageDocument struct {
	Profile   hoststate.Profile `json:"profile"`
	UsedBytes uint64            `json:"usedBytes"`
}

type signatureDocument struct {
	Type  string `json:"type"`
	Label string `json:"label,omitempty"`
	UUID  string `json:"uuid,omitempty"`
}

type systemDocument struct {
	ID              string                  `json:"id"`
	Hostname        string                  `json:"hostname"`
	OperatingSystem operatingSystemDocument `json:"operatingSystem"`
	Architecture    string                  `json:"architecture"`
	UptimeSeconds   uint64                  `json:"uptimeSeconds"`
	Health          hoststate.Health        `json:"health"`
	Storage         *StorageUsageDocument   `json:"storage,omitempty"`
	Thermal         *thermalDocument        `json:"thermal,omitempty"`
}

type thermalDocument struct {
	CPU  *temperatureDocument `json:"cpu,omitempty"`
	GPUs []gpuDocument        `json:"gpus"`
}

type gpuDocument struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Integrated  bool                 `json:"integrated,omitempty"`
	Temperature *temperatureDocument `json:"temperature,omitempty"`
}

type temperatureDocument struct {
	Celsius    int `json:"celsius"`
	HotCelsius int `json:"hotCelsius"`
}

func thermalFromDomain(thermal *hoststate.Thermal) *thermalDocument {
	if thermal == nil {
		return nil
	}
	document := &thermalDocument{CPU: temperatureFromDomain(thermal.CPU), GPUs: make([]gpuDocument, len(thermal.GPUs))}
	for i, gpu := range thermal.GPUs {
		document.GPUs[i] = gpuDocument{ID: gpu.ID.String(), Name: gpu.Name, Integrated: gpu.Integrated, Temperature: temperatureFromDomain(gpu.Temperature)}
	}
	return document
}

func temperatureFromDomain(temperature *hoststate.Temperature) *temperatureDocument {
	if temperature == nil {
		return nil
	}
	return &temperatureDocument{Celsius: temperature.Celsius, HotCelsius: temperature.HotCelsius}
}

func (document *thermalDocument) toDomain() (*hoststate.Thermal, error) {
	if document == nil {
		return nil, nil
	}
	cpu, err := document.CPU.toDomain()
	if err != nil {
		return nil, err
	}
	thermal := &hoststate.Thermal{CPU: cpu, GPUs: make([]hoststate.GPU, len(document.GPUs))}
	seen := make(map[string]bool, len(document.GPUs))
	for i, gpu := range document.GPUs {
		id, err := hoststate.NewResourceID(gpu.ID)
		if err != nil || seen[id.String()] || strings.TrimSpace(gpu.Name) == "" {
			return nil, errors.New("invalid GPU")
		}
		seen[id.String()] = true
		temperature, err := gpu.Temperature.toDomain()
		if err != nil {
			return nil, err
		}
		thermal.GPUs[i] = hoststate.GPU{ID: id, Name: gpu.Name, Integrated: gpu.Integrated, Temperature: temperature}
	}
	return thermal, nil
}

func (document *temperatureDocument) toDomain() (*hoststate.Temperature, error) {
	if document == nil {
		return nil, nil
	}
	temperature := hoststate.Temperature{Celsius: document.Celsius, HotCelsius: document.HotCelsius}
	if !temperature.Valid() {
		return nil, errors.New("invalid temperature")
	}
	return &temperature, nil
}

type StorageUsageDocument struct {
	TotalBytes     uint64 `json:"totalBytes"`
	AvailableBytes uint64 `json:"availableBytes"`
}

func storageFromDomain(usage *hoststate.StorageUsage) *StorageUsageDocument {
	if usage == nil {
		return nil
	}
	return &StorageUsageDocument{TotalBytes: usage.TotalBytes, AvailableBytes: usage.AvailableBytes}
}

type operatingSystemDocument struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type DiskDocument struct {
	ObservationIssues []hoststate.DiskObservationIssue `json:"observationIssues,omitempty"`

	ID                       string              `json:"id"`
	Model                    string              `json:"model"`
	Transport                hoststate.Transport `json:"transport"`
	CapacityBytes            uint64              `json:"capacityBytes"`
	FilesystemCapacityBytes  *uint64             `json:"filesystemCapacityBytes,omitempty"`
	FilesystemAvailableBytes *uint64             `json:"filesystemAvailableBytes,omitempty"`
	Rotational               bool                `json:"rotational"`
	Removable                bool                `json:"removable"`
	InUse                    bool                `json:"inUse"`
	Filesystems              []string            `json:"filesystems"`
	Role                     hoststate.DiskRole  `json:"role"`
	Health                   hoststate.Health    `json:"health"`
	SMARTStatus              hoststate.Health    `json:"smartStatus"`
	TemperatureCelsius       *int                `json:"temperatureCelsius,omitempty"`
	FilesystemReadOnly       bool                `json:"filesystemReadOnly,omitempty"`
	FilesystemErrors         *uint64             `json:"filesystemErrors,omitempty"`
	Signatures               []signatureDocument `json:"signatures,omitempty"`
}

func DocumentFromDomain(state hoststate.State) StateDocument {
	disks := make([]DiskDocument, len(state.Disks))
	for i, disk := range state.Disks {
		disks[i] = DiskDocument{
			ObservationIssues:        append([]hoststate.DiskObservationIssue(nil), disk.ObservationIssues...),
			ID:                       disk.ID.String(),
			Model:                    disk.Model,
			Transport:                disk.Transport,
			CapacityBytes:            disk.CapacityBytes,
			FilesystemCapacityBytes:  disk.FilesystemCapacityBytes,
			FilesystemAvailableBytes: disk.FilesystemAvailableBytes,
			Rotational:               disk.Rotational,
			Removable:                disk.Removable,
			InUse:                    disk.InUse,
			Filesystems:              append([]string(nil), disk.Filesystems...),
			Role:                     disk.Role,
			Health:                   disk.Health,
			SMARTStatus:              disk.SMARTStatus,
			TemperatureCelsius:       disk.TemperatureCelsius,
			FilesystemReadOnly:       disk.FilesystemReadOnly,
			FilesystemErrors:         disk.FilesystemErrors,
		}
		for _, signature := range disk.Signatures {
			disks[i].Signatures = append(disks[i].Signatures, signatureDocument{Type: signature.Type, Label: signature.Label, UUID: signature.UUID})
		}
	}
	var dataVolume *dataVolumeDocument
	if volume := state.DataVolume; volume != nil {
		dataVolume = &dataVolumeDocument{
			FilesystemUUID: volume.FilesystemUUID, DiskIDs: make([]string, len(volume.DiskIDs)),
			Data: usageDocuments(volume.Data), Metadata: usageDocuments(volume.Metadata), Operation: volume.Operation,
		}
		for i, id := range volume.DiskIDs {
			dataVolume.DiskIDs[i] = id.String()
		}
	}
	return StateDocument{
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
			Storage:       storageFromDomain(state.System.Storage),
			Thermal:       thermalFromDomain(state.System.Thermal),
		},
		Disks:           disks,
		DataVolume:      dataVolume,
		ReconcileFailed: state.ReconcileFailed,
	}
}

func usageDocuments(usage []hoststate.ProfileUsage) []profileUsageDocument {
	documents := make([]profileUsageDocument, len(usage))
	for i, item := range usage {
		documents[i] = profileUsageDocument{Profile: item.Profile, UsedBytes: item.UsedBytes}
	}
	return documents
}

func (document StateDocument) ToDomain() (hoststate.State, error) {
	var storage *hoststate.StorageUsage
	if usage := document.System.Storage; usage != nil {
		if usage.TotalBytes == 0 || usage.AvailableBytes > usage.TotalBytes {
			return hoststate.State{}, errors.New("invalid system storage usage")
		}
		storage = &hoststate.StorageUsage{TotalBytes: usage.TotalBytes, AvailableBytes: usage.AvailableBytes}
	}
	thermal, err := document.System.Thermal.toDomain()
	if err != nil {
		return hoststate.State{}, err
	}
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
		if (strings.TrimSpace(disk.Model) == "" && !slices.Contains(disk.ObservationIssues, hoststate.DiskMissingModel)) ||
			(disk.CapacityBytes == 0 && !slices.Contains(disk.ObservationIssues, hoststate.DiskMissingCapacity)) ||
			!validTransport(disk.Transport) || !validRole(disk.Role) || !validHealth(disk.Health) || !validHealth(disk.SMARTStatus) {
			return hoststate.State{}, errors.New("invalid disk state")
		}
		for _, issue := range disk.ObservationIssues {
			if !issue.Valid() {
				return hoststate.State{}, errors.New("invalid disk observation issue")
			}
		}
		if strings.HasPrefix(disk.ID, "observation:") && len(disk.ObservationIssues) == 0 {
			return hoststate.State{}, errors.New("unidentified disk without an observation issue")
		}
		if (disk.FilesystemCapacityBytes == nil) != (disk.FilesystemAvailableBytes == nil) {
			return hoststate.State{}, errors.New("incomplete filesystem usage")
		}
		if disk.FilesystemCapacityBytes != nil && (disk.Role != hoststate.DiskRoleData || *disk.FilesystemCapacityBytes == 0 || *disk.FilesystemAvailableBytes > *disk.FilesystemCapacityBytes) {
			return hoststate.State{}, errors.New("invalid filesystem usage")
		}
		if (disk.FilesystemReadOnly || disk.FilesystemErrors != nil) && disk.Role != hoststate.DiskRoleData {
			return hoststate.State{}, errors.New("data volume facts on a disk without the data volume")
		}
		for _, filesystem := range disk.Filesystems {
			if strings.TrimSpace(filesystem) == "" {
				return hoststate.State{}, errors.New("invalid disk filesystem evidence")
			}
		}
		var signatures []hoststate.Signature
		for _, signature := range disk.Signatures {
			if strings.TrimSpace(signature.Type) == "" {
				return hoststate.State{}, errors.New("invalid disk signature")
			}
			signatures = append(signatures, hoststate.Signature{Type: signature.Type, Label: signature.Label, UUID: signature.UUID})
		}
		disks[i] = hoststate.Disk{
			ObservationIssues:        append([]hoststate.DiskObservationIssue(nil), disk.ObservationIssues...),
			ID:                       id,
			Model:                    disk.Model,
			Transport:                disk.Transport,
			CapacityBytes:            disk.CapacityBytes,
			FilesystemCapacityBytes:  disk.FilesystemCapacityBytes,
			FilesystemAvailableBytes: disk.FilesystemAvailableBytes,
			Rotational:               disk.Rotational,
			Removable:                disk.Removable,
			InUse:                    disk.InUse,
			Filesystems:              append([]string(nil), disk.Filesystems...),
			Role:                     disk.Role,
			Health:                   disk.Health,
			SMARTStatus:              disk.SMARTStatus,
			TemperatureCelsius:       disk.TemperatureCelsius,
			FilesystemReadOnly:       disk.FilesystemReadOnly,
			FilesystemErrors:         disk.FilesystemErrors,
			Signatures:               signatures,
		}
	}
	dataVolume, err := document.DataVolume.ToDomain(disks)
	if err != nil {
		return hoststate.State{}, err
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
			Storage:       storage,
			Thermal:       thermal,
		},
		Disks:           disks,
		DataVolume:      dataVolume,
		ReconcileFailed: document.ReconcileFailed,
	}, nil
}

// toDomain accepts a data volume only on disks the same document reports as
// data disks.
func (document *dataVolumeDocument) ToDomain(disks []hoststate.Disk) (*hoststate.DataVolume, error) {
	if document == nil {
		return nil, nil
	}
	if strings.TrimSpace(document.FilesystemUUID) == "" || len(document.DiskIDs) == 0 || !document.Operation.Valid() {
		return nil, errors.New("invalid data volume")
	}
	roles := make(map[string]hoststate.DiskRole, len(disks))
	for _, disk := range disks {
		roles[disk.ID.String()] = disk.Role
	}
	volume := &hoststate.DataVolume{FilesystemUUID: document.FilesystemUUID, Operation: document.Operation}
	for _, value := range document.DiskIDs {
		id, err := hoststate.NewResourceID(value)
		if err != nil || roles[id.String()] != hoststate.DiskRoleData {
			return nil, errors.New("data volume on a disk that is not a data disk")
		}
		volume.DiskIDs = append(volume.DiskIDs, id)
	}
	var err error
	if volume.Data, err = usageFromDocuments(document.Data); err != nil {
		return nil, err
	}
	if volume.Metadata, err = usageFromDocuments(document.Metadata); err != nil {
		return nil, err
	}
	return volume, nil
}

func usageFromDocuments(documents []profileUsageDocument) ([]hoststate.ProfileUsage, error) {
	var usage []hoststate.ProfileUsage
	for _, document := range documents {
		if !document.Profile.Valid() {
			return nil, errors.New("invalid data volume profile")
		}
		usage = append(usage, hoststate.ProfileUsage{Profile: document.Profile, UsedBytes: document.UsedBytes})
	}
	return usage, nil
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

func EnsureEndOfJSON(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("decode host agent response: multiple JSON values")
		}
		return fmt.Errorf("decode host agent response: %w", err)
	}
	return nil
}

type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ErrorDocument struct {
	Error ErrorDetail `json:"error"`
}

// SocketPath is the Host Agent's socket: configured (ANAS_HOST_AGENT_SOCKET)
// when set, otherwise a-nas/host-agent.sock in the runtime directory
// (XDG_RUNTIME_DIR), as for a user service. Both binaries read the two
// variables from their own configuration.
func SocketPath(configured, runtimeDirectory string) (string, error) {
	if configured != "" {
		if !filepath.IsAbs(configured) {
			return "", errors.New("ANAS_HOST_AGENT_SOCKET must be an absolute path")
		}
		return filepath.Clean(configured), nil
	}
	if runtimeDirectory == "" || !filepath.IsAbs(runtimeDirectory) {
		return "", errors.New("XDG_RUNTIME_DIR must be an absolute path when ANAS_HOST_AGENT_SOCKET is not set")
	}
	return filepath.Join(runtimeDirectory, "a-nas", "host-agent.sock"), nil
}
