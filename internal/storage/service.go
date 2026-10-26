// Package storage coordinates safe, high-level data-volume operations.
package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/zhongwater123/A-NAS/internal/activity"
	"github.com/zhongwater123/A-NAS/internal/capacity"
	"github.com/zhongwater123/A-NAS/internal/hoststate"
)

var (
	ErrDiskNotFound       = errors.New("disk not found")
	ErrDiskNotEligible    = errors.New("disk is not eligible for the data volume")
	ErrPlanNotFound       = errors.New("execution plan not found")
	ErrPlanExpired        = errors.New("execution plan expired")
	ErrPlanState          = errors.New("execution plan is not in the required state")
	ErrConfirmation       = errors.New("confirmation phrase does not match")
	ErrDiskChanged        = errors.New("disk identity changed after planning")
	ErrVolumeChanged      = errors.New("data volume changed after planning")
	ErrVolumeUnavailable  = errors.New("the data volume is not mounted and writable")
	ErrVolumeBusy         = errors.New("the data volume is converting to another layout")
	ErrInvalidLayout      = errors.New("the layout does not fit the number of disks")
	ErrLayoutTooSmall     = errors.New("the files on the data volume do not fit the layout")
	ErrNoConversion       = errors.New("the data volume is not converting")
	ErrOperationAttention = errors.New("operation needs administrator attention")
	// ErrVolumeExists: the host shows a data volume, or a mount unit for
	// one, that the plan did not make.
	ErrVolumeExists = errors.New("a data volume this plan did not make is already on the host")
	// ErrOperationNotFound: the host has no operation for the plan; it
	// never received it.
	ErrOperationNotFound = errors.New("the host has no operation for the plan")
)

// maxPlanDisks bounds one plan; a home NAS has a handful of bays.
const maxPlanDisks = 8

type PlanState string

const (
	PlanStatePlanned        PlanState = "planned"
	PlanStateConfirmed      PlanState = "confirmed"
	PlanStateRunning        PlanState = "running"
	PlanStateSucceeded      PlanState = "succeeded"
	PlanStateFailed         PlanState = "failed"
	PlanStateNeedsAttention PlanState = "needs_attention"
	PlanStateExpired        PlanState = "expired"
	// PlanStateCancelled: the administrator gave the plan up before it ran.
	PlanStateCancelled PlanState = "cancelled"
)

// PlanKind is what a plan does to the data volume.
type PlanKind string

const (
	// PlanKindCreateVolume formats the disks as a new data volume.
	PlanKindCreateVolume PlanKind = "create_volume"
	// PlanKindAddDisks erases the disks and adds them to the data volume.
	PlanKindAddDisks PlanKind = "add_disks"
)

type VolumeState string

const (
	VolumeStateCreating    VolumeState = "creating"
	VolumeStateAvailable   VolumeState = "available"
	VolumeStateUnavailable VolumeState = "unavailable"
	VolumeStateReadOnly    VolumeState = "read_only"
)

type Action struct {
	Kind        string `json:"kind"`
	Description string `json:"description"`
}

// Signature is a format found on a disk that erasing it destroys.
type Signature struct {
	Type  string `json:"type"`
	Label string `json:"label,omitempty"`
}

// PlannedDisk is a disk a plan erases, as identified when planning.
type PlannedDisk struct {
	ID            string      `json:"id"`
	Model         string      `json:"model"`
	CapacityBytes uint64      `json:"capacityBytes"`
	Fingerprint   string      `json:"fingerprint"`
	Signatures    []Signature `json:"signatures"`
}

type ExecutionPlan struct {
	ID     string        `json:"id"`
	Kind   PlanKind      `json:"kind"`
	Layout Layout        `json:"layout"`
	Disks  []PlannedDisk `json:"disks"`
	// CapacityBeforeBytes and CapacityAfterBytes estimate the space for
	// files before and after the plan; there is no before for a new volume.
	CapacityBeforeBytes uint64    `json:"capacityBeforeBytes,omitempty"`
	CapacityAfterBytes  uint64    `json:"capacityAfterBytes"`
	ConfirmationPhrase  string    `json:"confirmationPhrase"`
	Actions             []Action  `json:"actions"`
	State               PlanState `json:"state"`
	// Step is the step the host is at while the plan runs, or the one it
	// stopped in when the plan needs attention.
	Step      OperationStep `json:"step,omitempty"`
	CreatedAt time.Time     `json:"createdAt"`
	ExpiresAt time.Time     `json:"expiresAt"`
	Failure   string        `json:"failure,omitempty"`
	Volume    *Volume       `json:"volume,omitempty"`
}

// UnmarshalJSON also reads plans stored before data volumes could span
// disks, which named one disk at the top level.
func (p *ExecutionPlan) UnmarshalJSON(data []byte) error {
	type document ExecutionPlan
	var decoded struct {
		document
		DiskID        string   `json:"diskId"`
		DiskModel     string   `json:"diskModel"`
		CapacityBytes uint64   `json:"capacityBytes"`
		Fingerprint   string   `json:"fingerprint"`
		Signatures    []string `json:"signatures"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*p = ExecutionPlan(decoded.document)
	if p.Kind == "" {
		p.Kind, p.Layout, p.CapacityAfterBytes = PlanKindCreateVolume, LayoutSingle, decoded.CapacityBytes
		disk := PlannedDisk{ID: decoded.DiskID, Model: decoded.DiskModel, CapacityBytes: decoded.CapacityBytes, Fingerprint: decoded.Fingerprint, Signatures: []Signature{}}
		for _, signature := range decoded.Signatures {
			disk.Signatures = append(disk.Signatures, Signature{Type: signature})
		}
		p.Disks = []PlannedDisk{disk}
	}
	return nil
}

type Volume struct {
	ID string `json:"id"`
	// DiskIDs are the disks holding the volume: those observed, or the
	// planned ones while it is not mounted.
	DiskIDs        []string    `json:"diskIds"`
	FilesystemUUID string      `json:"filesystemUuid,omitempty"`
	Layout         Layout      `json:"layout"`
	CapacityBytes  uint64      `json:"capacityBytes,omitempty"`
	AvailableBytes *uint64     `json:"availableBytes,omitempty"`
	State          VolumeState `json:"state"`
	// Conversion is set while data or metadata are outside the layout's
	// Btrfs profiles, after disks were added with another layout.
	Conversion *Conversion `json:"conversion,omitempty"`
}

// UnmarshalJSON also reads volumes stored when a volume had one disk.
func (v *Volume) UnmarshalJSON(data []byte) error {
	type document Volume
	var decoded struct {
		document
		DiskID string `json:"diskId"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*v = Volume(decoded.document)
	if len(v.DiskIDs) == 0 && decoded.DiskID != "" {
		v.DiskIDs = []string{decoded.DiskID}
	}
	if v.Layout == "" {
		v.Layout = LayoutSingle
	}
	return nil
}

type ConversionState string

const (
	// ConversionRunning: Btrfs is converting the volume now.
	ConversionRunning ConversionState = "running"
	// ConversionWaiting: the conversion stopped, for example at shutdown;
	// the Host Agent continues it at startup and every 15 minutes.
	ConversionWaiting ConversionState = "waiting"
)

type Conversion struct {
	State          ConversionState `json:"state"`
	ConvertedBytes uint64          `json:"convertedBytes"`
	TotalBytes     uint64          `json:"totalBytes"`
}

// DiskTarget is a disk the Host Agent erases, with the fingerprint it must
// still have.
type DiskTarget struct {
	DiskID      string `json:"diskId"`
	Fingerprint string `json:"fingerprint"`
}

type CreateVolumeRequest struct {
	PlanID string
	Disks  []DiskTarget
	Layout Layout
}

// AddDisksRequest adds disks to the mounted data volume with the
// filesystem UUID and converts it to the layout.
type AddDisksRequest struct {
	PlanID         string
	FilesystemUUID string
	Disks          []DiskTarget
	Layout         Layout
}

// VolumeExecutor runs a plan's operation on the host. Starting reports the
// operation without waiting for it to end; for a plan the host already has
// an operation for, it starts nothing new: it reports one that runs or
// succeeded, and continues one that failed or was interrupted after the
// steps that are done.
type VolumeExecutor interface {
	StartCreateVolume(context.Context, CreateVolumeRequest) (VolumeOperation, error)
	StartAddDisks(context.Context, AddDisksRequest) (VolumeOperation, error)
	// VolumeOperation reports the plan's operation, or ErrOperationNotFound.
	VolumeOperation(context.Context, string) (VolumeOperation, error)
	// ContinueConversion starts converting the data volume to its layout
	// again now, instead of at the Host Agent's next check.
	ContinueConversion(context.Context) error
}

type Options struct {
	// Now and NewID default to time.Now and random plan IDs. Planning
	// requests call both outside the service's lock, and the goroutine that
	// follows a running plan calls Now when it saves the plan, so both must
	// be safe for concurrent use, as the defaults are.
	Now   func() time.Time
	NewID func() string
	Store *Store
	// PollInterval is how often a running operation is asked about; one
	// second when zero.
	PollInterval time.Duration
}

type Store struct{ db *sql.DB }

func OpenSQLite(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("SQLite path is empty")
	}
	db, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_busy_timeout=5000&_journal_mode=WAL")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS storage_plans (
    id TEXT PRIMARY KEY,
    document_json TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS audit_events (
    id TEXT PRIMARY KEY,
    actor_user_id TEXT NOT NULL,
    action TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    occurred_at TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT ''
);
` + activity.IndexStatement); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

type Service struct {
	reader       hoststate.Reader
	executor     VolumeExecutor
	now          func() time.Time
	newID        func() string
	pollInterval time.Duration

	ctx       context.Context
	stop      context.CancelFunc
	followers sync.WaitGroup

	mu        sync.Mutex
	plans     map[string]ExecutionPlan
	following map[string]bool
	store     *Store
}

func NewService(reader hoststate.Reader, executor VolumeExecutor, options Options) *Service {
	options.Store = nil
	service, _ := OpenService(reader, executor, options)
	return service
}

func OpenService(reader hoststate.Reader, executor VolumeExecutor, options Options) (*Service, error) {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	newID := options.NewID
	if newID == nil {
		newID = randomID
	}
	pollInterval := options.PollInterval
	if pollInterval <= 0 {
		pollInterval = time.Second
	}
	service := &Service{
		reader: reader, executor: executor, now: now, newID: newID, pollInterval: pollInterval,
		plans: make(map[string]ExecutionPlan), following: make(map[string]bool), store: options.Store,
	}
	service.ctx, service.stop = context.WithCancel(context.Background())
	if err := service.loadPlans(context.Background()); err != nil {
		service.stop()
		return nil, err
	}
	// The Host Agent went on with a plan that ran when the service stopped.
	// The running plans are listed first: a follower writes s.plans as soon
	// as it starts, so the map is not ranged over once one runs.
	var running []string
	for _, plan := range service.plans {
		if plan.State == PlanStateRunning {
			running = append(running, plan.ID)
		}
	}
	for _, planID := range running {
		if executor == nil {
			_, _ = service.needsAttention(planID, errors.New("volume executor is unavailable"))
			continue
		}
		service.follow(planID)
	}
	return service, nil
}

// PlanVolume plans erasing the disks: it creates the data volume with the
// layout when there is none, and otherwise adds them to it and converts it
// to the layout. An empty layout creates a single disk or a mirror. The new
// plan takes the place of any that has not run, which is cancelled: a page
// reload forgets a pending plan, and it must not hold the next one back.
func (s *Service) PlanVolume(ctx context.Context, diskIDs []string, layout Layout) (ExecutionPlan, error) {
	ids, err := distinctDiskIDs(diskIDs)
	if err != nil {
		return ExecutionPlan{}, err
	}
	s.mu.Lock()
	err = s.runningPlanLocked()
	current, exists := s.currentVolumeLocked()
	s.mu.Unlock()
	if err != nil {
		return ExecutionPlan{}, err
	}
	state, err := s.reader.Read(ctx)
	if err != nil {
		return ExecutionPlan{}, err
	}
	if !exists && unrecordedVolume(state) {
		return ExecutionPlan{}, ErrVolumeExists
	}
	disks := make([]hoststate.Disk, 0, len(ids))
	for _, id := range ids {
		disk, err := eligibleDisk(state, id, current.FilesystemUUID)
		if err != nil {
			return ExecutionPlan{}, err
		}
		disks = append(disks, disk)
	}
	now := s.now().UTC()
	plan := ExecutionPlan{
		ID: s.newID(), Layout: layout, ConfirmationPhrase: confirmationPhrase(ids),
		State: PlanStatePlanned, CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute),
	}
	sizes := make([]uint64, 0, len(disks))
	for _, disk := range disks {
		plan.Disks = append(plan.Disks, plannedDisk(disk))
		sizes = append(sizes, disk.CapacityBytes)
	}
	if !exists {
		plan.Kind = PlanKindCreateVolume
		if plan.Layout == "" {
			plan.Layout = LayoutSingle
			if len(disks) > 1 {
				plan.Layout = LayoutMirror
			}
		}
		if !plan.Layout.Fits(len(disks)) {
			return ExecutionPlan{}, ErrInvalidLayout
		}
		plan.CapacityAfterBytes = UsableBytes(plan.Layout, sizes)
	} else {
		plan.Kind = PlanKindAddDisks
		volume := observeVolume(current, state)
		if volume.State != VolumeStateAvailable || state.DataVolume == nil {
			return ExecutionPlan{}, ErrVolumeUnavailable
		}
		if volume.Conversion != nil {
			return ExecutionPlan{}, ErrVolumeBusy
		}
		members := diskSizes(state, volume.DiskIDs)
		if !plan.Layout.Fits(len(members) + len(disks)) {
			return ExecutionPlan{}, ErrInvalidLayout
		}
		plan.CapacityBeforeBytes = UsableBytes(current.Layout, members)
		plan.CapacityAfterBytes = UsableBytes(plan.Layout, append(members, sizes...))
		if volume.AvailableBytes != nil && volume.CapacityBytes > 0 {
			used := volume.CapacityBytes - min(*volume.AvailableBytes, volume.CapacityBytes)
			if used+capacity.Reserve(plan.CapacityAfterBytes) > plan.CapacityAfterBytes {
				return ExecutionPlan{}, ErrLayoutTooSmall
			}
		}
	}
	plan.Actions = planActions(plan.Kind, plan.Layout, current.Layout)
	if strings.TrimSpace(plan.ID) == "" {
		return ExecutionPlan{}, errors.New("generate execution plan ID: empty ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.runningPlanLocked(); err != nil {
		return ExecutionPlan{}, err
	}
	if latest, stillExists := s.currentVolumeLocked(); stillExists != exists || latest.FilesystemUUID != current.FilesystemUUID {
		return ExecutionPlan{}, ErrPlanState
	}
	for id, pending := range s.plans {
		if pending.State == PlanStatePlanned || pending.State == PlanStateConfirmed {
			pending.State = PlanStateCancelled
			s.plans[id] = pending
			if err := s.savePlanLocked(context.Background(), pending); err != nil {
				return ExecutionPlan{}, err
			}
		}
	}
	s.plans[plan.ID] = plan
	if err := s.savePlanLocked(context.Background(), plan); err != nil {
		return ExecutionPlan{}, err
	}
	return clonePlan(plan), nil
}

// unrecordedVolume reports whether the host shows a data volume although
// no plan here made one: an interrupted plan made it, or its record is
// gone. A new volume on other disks would leave it behind with the files
// on it (issue #165), so the plan that made it is retried instead.
func unrecordedVolume(state hoststate.State) bool {
	if state.DataVolume != nil {
		return true
	}
	return slices.ContainsFunc(state.Disks, func(disk hoststate.Disk) bool { return disk.Role == hoststate.DiskRoleData })
}

// runningPlanLocked refuses a new plan while another one runs.
func (s *Service) runningPlanLocked() error {
	for _, existing := range s.plans {
		if existing.State == PlanStateRunning {
			return ErrPlanState
		}
	}
	return nil
}

func distinctDiskIDs(diskIDs []string) ([]string, error) {
	ids := make([]string, 0, len(diskIDs))
	for _, id := range diskIDs {
		id = strings.TrimSpace(id)
		if id == "" || slices.Contains(ids, id) {
			return nil, ErrDiskNotFound
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 || len(ids) > maxPlanDisks {
		return nil, ErrDiskNotFound
	}
	return ids, nil
}

func plannedDisk(disk hoststate.Disk) PlannedDisk {
	planned := PlannedDisk{
		ID: disk.ID.String(), Model: disk.Model, CapacityBytes: disk.CapacityBytes,
		Fingerprint: FingerprintDisk(disk), Signatures: []Signature{},
	}
	for _, signature := range disk.Signatures {
		planned.Signatures = append(planned.Signatures, Signature{Type: signature.Type, Label: signature.Label})
	}
	if len(disk.Signatures) == 0 {
		for _, filesystem := range disk.Filesystems {
			planned.Signatures = append(planned.Signatures, Signature{Type: filesystem})
		}
	}
	return planned
}

func diskIDsOf(disks []PlannedDisk) []string {
	ids := make([]string, len(disks))
	for i, disk := range disks {
		ids[i] = disk.ID
	}
	return ids
}

func diskSizes(state hoststate.State, diskIDs []string) []uint64 {
	var sizes []uint64
	for _, disk := range state.Disks {
		if slices.Contains(diskIDs, disk.ID.String()) {
			sizes = append(sizes, disk.CapacityBytes)
		}
	}
	return sizes
}

var layoutNames = map[Layout]string{LayoutSingle: "单盘", LayoutMirror: "镜像（RAID 1）", LayoutSpan: "扩容（不冗余）", LayoutStripe: "条带（RAID 0）"}

func planActions(kind PlanKind, layout, current Layout) []Action {
	actions := []Action{
		{Kind: "erase_signatures", Description: "清除目标硬盘上现有的分区、文件系统和阵列签名"},
		{Kind: "create_partition_table", Description: "在每块目标硬盘上创建 GPT 和单个 A-NAS 数据分区"},
	}
	if kind == PlanKindCreateVolume {
		return append(actions,
			Action{Kind: "create_btrfs", Description: "创建标签为 ANAS_DATA、冗余方式为" + layoutNames[layout] + "的 Btrfs 文件系统"},
			Action{Kind: "mount_volume", Description: "按文件系统 UUID 持久挂载并创建空间子卷"},
		)
	}
	actions = append(actions, Action{Kind: "add_devices", Description: "把目标硬盘加入正在使用的数据卷，已有文件保持原样"})
	description := "在后台把数据卷从" + layoutNames[current] + "转换为" + layoutNames[layout] + "，期间可以照常使用"
	if layout == current {
		description = "在后台让数据卷的元数据在两块硬盘上各存一份，期间可以照常使用"
	}
	return append(actions, Action{Kind: "convert_layout", Description: description})
}

func (s *Service) ConfirmPlan(ctx context.Context, planID, phrase string) (ExecutionPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	plan, ok := s.plans[planID]
	if !ok {
		return ExecutionPlan{}, ErrPlanNotFound
	}
	if !s.now().UTC().Before(plan.ExpiresAt) {
		plan.State = PlanStateExpired
		s.plans[planID] = plan
		_ = s.savePlanLocked(ctx, plan)
		return clonePlan(plan), ErrPlanExpired
	}
	if plan.State != PlanStatePlanned {
		return clonePlan(plan), ErrPlanState
	}
	if phrase != plan.ConfirmationPhrase {
		return clonePlan(plan), ErrConfirmation
	}
	plan.State = PlanStateConfirmed
	s.plans[planID] = plan
	if err := s.savePlanLocked(ctx, plan); err != nil {
		return ExecutionPlan{}, err
	}
	return clonePlan(plan), nil
}

// ExecutePlan starts the confirmed plan on the host and returns it running,
// or ended when the host finished at once. The Host Agent runs the
// operation apart from this request, and the service follows it until it
// ends. A plan that needs attention is retried the same way: the Host Agent
// checks the disks again and continues after the steps that are done.
func (s *Service) ExecutePlan(ctx context.Context, planID string) (ExecutionPlan, error) {
	s.mu.Lock()
	plan, ok := s.plans[planID]
	if !ok {
		s.mu.Unlock()
		return ExecutionPlan{}, ErrPlanNotFound
	}
	retry := plan.State == PlanStateNeedsAttention
	switch plan.State {
	case PlanStateSucceeded, PlanStateRunning:
		s.mu.Unlock()
		return clonePlan(plan), nil
	case PlanStateConfirmed:
		if !s.now().UTC().Before(plan.ExpiresAt) {
			plan.State = PlanStateExpired
			s.plans[planID] = plan
			_ = s.savePlanLocked(ctx, plan)
			s.mu.Unlock()
			return clonePlan(plan), ErrPlanExpired
		}
	case PlanStateNeedsAttention:
		// The ten minutes do not apply: the plan has run, and its disks
		// may no longer be unused.
		if s.runningPlanLocked() != nil || s.supersededLocked(plan) {
			s.mu.Unlock()
			return clonePlan(plan), ErrPlanState
		}
	default:
		s.mu.Unlock()
		return clonePlan(plan), ErrPlanState
	}
	current, exists := s.currentVolumeLocked()
	if retry && exists != (plan.Kind == PlanKindAddDisks) {
		s.mu.Unlock()
		return clonePlan(plan), ErrPlanState
	}
	plan.State, plan.Step, plan.Failure = PlanStateRunning, "", ""
	s.plans[planID] = plan
	if err := s.savePlanLocked(ctx, plan); err != nil {
		s.mu.Unlock()
		return ExecutionPlan{}, err
	}
	s.mu.Unlock()

	if s.executor == nil {
		return s.needsAttention(planID, errors.New("volume executor is unavailable"))
	}
	targets := make([]DiskTarget, 0, len(plan.Disks))
	for _, planned := range plan.Disks {
		targets = append(targets, DiskTarget{DiskID: planned.ID, Fingerprint: planned.Fingerprint})
	}
	// Closing the page must not stop the start halfway either.
	startCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), operationCallTimeout)
	defer cancel()
	// A first run checks the host here as well, to answer at once; a retry
	// leaves that to the Host Agent, which knows the steps that are done.
	if !retry {
		state, err := s.reader.Read(startCtx)
		if err != nil {
			return s.needsAttention(planID, err)
		}
		for _, planned := range plan.Disks {
			disk, err := eligibleDisk(state, planned.ID, current.FilesystemUUID)
			if err != nil || FingerprintDisk(disk) != planned.Fingerprint {
				return s.needsAttention(planID, ErrDiskChanged)
			}
		}
		switch {
		case plan.Kind == PlanKindCreateVolume && !exists:
			if unrecordedVolume(state) {
				return s.needsAttention(planID, ErrVolumeExists)
			}
		case plan.Kind == PlanKindAddDisks && exists:
			observed := observeVolume(current, state)
			if observed.State != VolumeStateAvailable || observed.Conversion != nil || state.DataVolume == nil {
				return s.needsAttention(planID, ErrVolumeChanged)
			}
		default:
			return s.needsAttention(planID, ErrVolumeChanged)
		}
	}
	var operation VolumeOperation
	var err error
	if plan.Kind == PlanKindCreateVolume {
		operation, err = s.executor.StartCreateVolume(startCtx, CreateVolumeRequest{PlanID: plan.ID, Disks: targets, Layout: plan.Layout})
	} else {
		operation, err = s.executor.StartAddDisks(startCtx, AddDisksRequest{
			PlanID: plan.ID, FilesystemUUID: current.FilesystemUUID, Disks: targets, Layout: plan.Layout,
		})
	}
	if err != nil {
		// The Host Agent may have started the operation before the answer
		// was lost; only one it never received is known to change nothing.
		found, lookupErr := s.executor.VolumeOperation(startCtx, planID)
		switch {
		case errors.Is(lookupErr, ErrOperationNotFound):
			return s.needsAttention(planID, err)
		case lookupErr != nil:
			s.follow(planID)
			return s.GetPlan(planID)
		}
		operation = found
	}
	return s.track(planID, operation)
}

// CancelPlan gives up a plan that has not run, so that the administrator
// can leave it or plan again at once instead of waiting for it to expire.
// Nothing has touched a disk before a plan runs.
func (s *Service) CancelPlan(ctx context.Context, planID string) (ExecutionPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	plan, ok := s.plans[planID]
	if !ok {
		return ExecutionPlan{}, ErrPlanNotFound
	}
	switch plan.State {
	case PlanStateCancelled:
		return clonePlan(plan), nil
	case PlanStatePlanned, PlanStateConfirmed:
	default:
		return clonePlan(plan), ErrPlanState
	}
	plan.State = PlanStateCancelled
	s.plans[planID] = plan
	if err := s.savePlanLocked(ctx, plan); err != nil {
		return ExecutionPlan{}, err
	}
	return clonePlan(plan), nil
}

func (s *Service) GetPlan(planID string) (ExecutionPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	plan, ok := s.plans[planID]
	if !ok {
		return ExecutionPlan{}, ErrPlanNotFound
	}
	return clonePlan(plan), nil
}

func (s *Service) needsAttention(planID string, cause error) (ExecutionPlan, error) {
	return s.stopped(planID, "", cause)
}

// stopped records that the plan's operation stopped in the step, before it
// could finish. Only a running plan stops: another caller may have ended it.
func (s *Service) stopped(planID string, step OperationStep, cause error) (ExecutionPlan, error) {
	s.mu.Lock()
	plan := s.plans[planID]
	if plan.State != PlanStateRunning {
		s.mu.Unlock()
		return clonePlan(plan), nil
	}
	plan.State, plan.Step, plan.Failure = PlanStateNeedsAttention, step, cause.Error()
	s.plans[planID] = plan
	_ = s.savePlanLocked(context.Background(), plan)
	s.mu.Unlock()
	return clonePlan(plan), fmt.Errorf("%w: %v", ErrOperationAttention, cause)
}

func (s *Service) ListVolumes(ctx context.Context) ([]Volume, error) {
	s.mu.Lock()
	current, exists := s.currentVolumeLocked()
	s.mu.Unlock()
	if !exists {
		return []Volume{}, nil
	}
	state, err := s.reader.Read(ctx)
	if err != nil {
		current.State, current.AvailableBytes = VolumeStateUnavailable, nil
		return []Volume{current}, nil
	}
	return []Volume{observeVolume(current, state)}, nil
}

// ContinueConversion asks the Host Agent to convert the data volume to its
// layout now, after a conversion stopped.
func (s *Service) ContinueConversion(ctx context.Context) error {
	volumes, err := s.ListVolumes(ctx)
	if err != nil {
		return err
	}
	if len(volumes) == 0 || volumes[0].Conversion == nil || volumes[0].Conversion.State != ConversionWaiting {
		return ErrNoConversion
	}
	if s.executor == nil {
		return errors.New("volume executor is unavailable")
	}
	return s.executor.ContinueConversion(ctx)
}

// currentVolumeLocked is the data volume this device created, with the
// layout and disks the most recent plan left it with.
func (s *Service) currentVolumeLocked() (Volume, bool) {
	var created, latest *ExecutionPlan
	for _, plan := range s.plans {
		if plan.State != PlanStateSucceeded || plan.Volume == nil {
			continue
		}
		if plan.Kind == PlanKindCreateVolume && (created == nil || plan.CreatedAt.Before(created.CreatedAt)) {
			created = &plan
		}
		if latest == nil || plan.CreatedAt.After(latest.CreatedAt) {
			latest = &plan
		}
	}
	if created == nil {
		return Volume{}, false
	}
	volume := *created.Volume
	volume.Layout = latest.Volume.Layout
	volume.DiskIDs = append([]string(nil), latest.Volume.DiskIDs...)
	volume.Conversion = nil
	return volume, true
}

// observeVolume adds what the host shows of the volume: whether it is
// mounted and writable, its size and free space, its disks, and how far a
// conversion to its layout has come.
func observeVolume(volume Volume, state hoststate.State) Volume {
	volume.State, volume.AvailableBytes, volume.Conversion = VolumeStateUnavailable, nil, nil
	var raw []uint64
	for _, disk := range state.Disks {
		if slices.Contains(volume.DiskIDs, disk.ID.String()) {
			raw = append(raw, disk.CapacityBytes)
		}
		if disk.Role != hoststate.DiskRoleData {
			continue
		}
		if volume.State == VolumeStateUnavailable {
			volume.State = VolumeStateAvailable
		}
		if disk.FilesystemReadOnly {
			volume.State = VolumeStateReadOnly
		}
		if disk.FilesystemCapacityBytes != nil && disk.FilesystemAvailableBytes != nil {
			available := *disk.FilesystemAvailableBytes
			volume.CapacityBytes, volume.AvailableBytes = *disk.FilesystemCapacityBytes, &available
		}
	}
	if volume.AvailableBytes == nil && len(raw) > 0 {
		volume.CapacityBytes = UsableBytes(volume.Layout, raw)
	}
	if volume.DiskIDs == nil {
		volume.DiskIDs = []string{}
	}
	mounted := state.DataVolume
	if mounted == nil || volume.State == VolumeStateUnavailable || (volume.FilesystemUUID != "" && !strings.EqualFold(mounted.FilesystemUUID, volume.FilesystemUUID)) {
		return volume
	}
	volume.DiskIDs = make([]string, 0, len(mounted.DiskIDs))
	for _, id := range mounted.DiskIDs {
		volume.DiskIDs = append(volume.DiskIDs, id.String())
	}
	// A single disk is never converted to; its metadata is dup or single
	// depending on the btrfs-progs that formatted it.
	if volume.Layout == LayoutSingle {
		return volume
	}
	if converted, total := conversionProgress(mounted, volume.Layout); converted < total {
		volume.Conversion = &Conversion{State: ConversionWaiting, ConvertedBytes: converted, TotalBytes: total}
		if mounted.Operation == hoststate.VolumeOperationBalance {
			volume.Conversion.State = ConversionRunning
		}
	}
	return volume
}

// DataVolumeFilesystemUUID returns the filesystem UUID of the one data volume
// this device created, or "" when there is none or more than one. It reads
// only the stored plans: the file service checks the mounted volume against
// it before every request, and asking the Host Agent there made each request
// wait for a full disk read, and fail as unavailable when that read was slow.
func (s *Service) DataVolumeFilesystemUUID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var found *Volume
	for _, plan := range s.plans {
		if plan.State != PlanStateSucceeded || plan.Volume == nil {
			continue
		}
		if found != nil && found.ID != plan.Volume.ID {
			return ""
		}
		found = plan.Volume
	}
	if found == nil {
		return ""
	}
	return found.FilesystemUUID
}

func (s *Service) loadPlans(ctx context.Context) error {
	if s.store == nil {
		return nil
	}
	rows, err := s.store.db.QueryContext(ctx, "SELECT document_json FROM storage_plans")
	if err != nil {
		return err
	}
	var plans []ExecutionPlan
	for rows.Next() {
		var document string
		if err := rows.Scan(&document); err != nil {
			return err
		}
		var plan ExecutionPlan
		if err := json.Unmarshal([]byte(document), &plan); err != nil {
			return fmt.Errorf("decode persisted storage plan: %w", err)
		}
		plans = append(plans, plan)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, plan := range plans {
		s.plans[plan.ID] = plan
	}
	return nil
}

func (s *Service) savePlanLocked(ctx context.Context, plan ExecutionPlan) error {
	if s.store == nil {
		return nil
	}
	document, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	_, err = s.store.db.ExecContext(ctx, `
INSERT INTO storage_plans(id, document_json, updated_at) VALUES(?,?,?)
ON CONFLICT(id) DO UPDATE SET document_json=excluded.document_json, updated_at=excluded.updated_at`,
		plan.ID, string(document), s.now().UTC().Format(time.RFC3339Nano))
	if err == nil {
		_, err = s.store.db.ExecContext(ctx, `INSERT INTO audit_events
(id, actor_user_id, action, resource_type, resource_id, occurred_at, detail) VALUES(?,?,?,?,?,?,?)`,
			"audit:"+strings.TrimPrefix(randomID(), "plan:"), "system", "storage.plan."+string(plan.State), "storage_plan", plan.ID,
			s.now().UTC().Format(time.RFC3339Nano), plan.Failure)
	}
	return err
}

// eligibleDisk is the disk with the ID if a plan may erase it: an unused
// SATA or NVMe disk that is not the system disk, nor part of the data volume
// while it is offline. Signatures another system left behind do not count.
func eligibleDisk(state hoststate.State, diskID, volumeUUID string) (hoststate.Disk, error) {
	for _, disk := range state.Disks {
		if disk.ID.String() != diskID {
			continue
		}
		if len(disk.ObservationIssues) != 0 || disk.CapacityBytes == 0 || strings.TrimSpace(disk.Model) == "" ||
			strings.HasPrefix(disk.ID.String(), "observation:") || disk.Role != hoststate.DiskRoleUnassigned ||
			disk.Transport == hoststate.TransportUSB || disk.Transport == hoststate.TransportUnknown || disk.Removable || disk.InUse {
			return hoststate.Disk{}, ErrDiskNotEligible
		}
		for _, signature := range disk.Signatures {
			if volumeUUID != "" && strings.EqualFold(signature.UUID, volumeUUID) {
				return hoststate.Disk{}, ErrDiskNotEligible
			}
		}
		return disk, nil
	}
	return hoststate.Disk{}, ErrDiskNotFound
}

func FingerprintDisk(disk hoststate.Disk) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%s", disk.ID.String(), disk.Model, disk.CapacityBytes, disk.Transport)))
	return hex.EncodeToString(digest[:16])
}

// confirmationPhrase names the last characters of every disk to erase.
func confirmationPhrase(diskIDs []string) string {
	const visible = 7
	suffixes := make([]string, len(diskIDs))
	for i, diskID := range diskIDs {
		if len(diskID) > visible {
			diskID = diskID[len(diskID)-visible:]
		}
		suffixes[i] = diskID
	}
	return "ERASE " + strings.Join(suffixes, " ")
}

func clonePlan(plan ExecutionPlan) ExecutionPlan {
	plan.Actions = append([]Action{}, plan.Actions...)
	disks := make([]PlannedDisk, len(plan.Disks))
	for i, disk := range plan.Disks {
		disk.Signatures = append([]Signature{}, disk.Signatures...)
		disks[i] = disk
	}
	plan.Disks = disks
	if plan.Volume != nil {
		volume := *plan.Volume
		volume.DiskIDs = append([]string(nil), volume.DiskIDs...)
		if volume.Conversion != nil {
			conversion := *volume.Conversion
			volume.Conversion = &conversion
		}
		plan.Volume = &volume
	}
	return plan
}

func randomID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return "plan:" + hex.EncodeToString(value)
}
