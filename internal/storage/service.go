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
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/zhongwater123/A-NAS/internal/hoststate"
)

var (
	ErrDiskNotFound       = errors.New("disk not found")
	ErrDiskNotEligible    = errors.New("disk is not eligible for data-volume creation")
	ErrPlanNotFound       = errors.New("execution plan not found")
	ErrPlanExpired        = errors.New("execution plan expired")
	ErrPlanState          = errors.New("execution plan is not in the required state")
	ErrConfirmation       = errors.New("confirmation phrase does not match")
	ErrDiskChanged        = errors.New("disk identity changed after planning")
	ErrVolumeExists       = errors.New("a data volume already exists")
	ErrOperationAttention = errors.New("operation needs administrator attention")
)

type PlanState string

const (
	PlanStatePlanned        PlanState = "planned"
	PlanStateConfirmed      PlanState = "confirmed"
	PlanStateRunning        PlanState = "running"
	PlanStateSucceeded      PlanState = "succeeded"
	PlanStateFailed         PlanState = "failed"
	PlanStateNeedsAttention PlanState = "needs_attention"
	PlanStateExpired        PlanState = "expired"
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

type ExecutionPlan struct {
	ID                 string    `json:"id"`
	DiskID             string    `json:"diskId"`
	DiskModel          string    `json:"diskModel"`
	CapacityBytes      uint64    `json:"capacityBytes"`
	Fingerprint        string    `json:"fingerprint"`
	Signatures         []string  `json:"signatures"`
	ConfirmationPhrase string    `json:"confirmationPhrase"`
	Actions            []Action  `json:"actions"`
	State              PlanState `json:"state"`
	CreatedAt          time.Time `json:"createdAt"`
	ExpiresAt          time.Time `json:"expiresAt"`
	Failure            string    `json:"failure,omitempty"`
	Volume             *Volume   `json:"volume,omitempty"`
}

type Volume struct {
	ID             string      `json:"id"`
	DiskID         string      `json:"diskId"`
	FilesystemUUID string      `json:"filesystemUuid,omitempty"`
	CapacityBytes  uint64      `json:"capacityBytes,omitempty"`
	AvailableBytes uint64      `json:"availableBytes,omitempty"`
	State          VolumeState `json:"state"`
}

type AuditEvent struct {
	ID           string    `json:"id"`
	ActorUserID  string    `json:"actorUserId"`
	Action       string    `json:"action"`
	ResourceType string    `json:"resourceType"`
	ResourceID   string    `json:"resourceId"`
	OccurredAt   time.Time `json:"occurredAt"`
	Detail       string    `json:"detail,omitempty"`
}

type CreateVolumeRequest struct {
	PlanID      string
	DiskID      string
	Fingerprint string
}

type VolumeExecutor interface {
	CreateVolume(context.Context, CreateVolumeRequest) (Volume, error)
}

type Options struct {
	Now   func() time.Time
	NewID func() string
	Store *Store
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
)`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

type Service struct {
	reader   hoststate.Reader
	executor VolumeExecutor
	now      func() time.Time
	newID    func() string

	mu    sync.Mutex
	plans map[string]ExecutionPlan
	store *Store
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
	service := &Service{reader: reader, executor: executor, now: now, newID: newID, plans: make(map[string]ExecutionPlan), store: options.Store}
	if err := service.loadPlans(context.Background()); err != nil {
		return nil, err
	}
	return service, nil
}

func (s *Service) PlanCreateVolume(ctx context.Context, diskID string) (ExecutionPlan, error) {
	s.mu.Lock()
	for _, existing := range s.plans {
		if existing.State == PlanStateSucceeded && existing.Volume != nil {
			s.mu.Unlock()
			return ExecutionPlan{}, ErrVolumeExists
		}
		if existing.State == PlanStateRunning || ((existing.State == PlanStatePlanned || existing.State == PlanStateConfirmed) && s.now().UTC().Before(existing.ExpiresAt)) {
			s.mu.Unlock()
			return ExecutionPlan{}, ErrPlanState
		}
	}
	s.mu.Unlock()
	disk, err := s.findEligibleDisk(ctx, diskID)
	if err != nil {
		return ExecutionPlan{}, err
	}
	now := s.now().UTC()
	plan := ExecutionPlan{
		ID:                 s.newID(),
		DiskID:             disk.ID.String(),
		DiskModel:          disk.Model,
		CapacityBytes:      disk.CapacityBytes,
		Fingerprint:        FingerprintDisk(disk),
		Signatures:         append([]string(nil), disk.Filesystems...),
		ConfirmationPhrase: confirmationPhrase(disk.ID.String()),
		Actions: []Action{
			{Kind: "erase_signatures", Description: "清除目标磁盘上现有的分区和文件系统签名"},
			{Kind: "create_partition_table", Description: "创建 GPT 和单个 A-NAS 数据分区"},
			{Kind: "create_btrfs", Description: "创建标签为 ANAS_DATA 的单盘 Btrfs 文件系统"},
			{Kind: "mount_volume", Description: "按文件系统 UUID 持久挂载并创建空间子卷"},
		},
		State:     PlanStatePlanned,
		CreatedAt: now,
		ExpiresAt: now.Add(10 * time.Minute),
	}
	if strings.TrimSpace(plan.ID) == "" {
		return ExecutionPlan{}, errors.New("generate execution plan ID: empty ID")
	}
	s.mu.Lock()
	for _, existing := range s.plans {
		if existing.State == PlanStateSucceeded && existing.Volume != nil {
			s.mu.Unlock()
			return ExecutionPlan{}, ErrVolumeExists
		}
		if existing.State == PlanStateRunning || ((existing.State == PlanStatePlanned || existing.State == PlanStateConfirmed) && now.Before(existing.ExpiresAt)) {
			s.mu.Unlock()
			return ExecutionPlan{}, ErrPlanState
		}
	}
	s.plans[plan.ID] = plan
	err = s.savePlanLocked(context.Background(), plan)
	s.mu.Unlock()
	if err != nil {
		return ExecutionPlan{}, err
	}
	return clonePlan(plan), nil
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

func (s *Service) ExecutePlan(ctx context.Context, planID string) (ExecutionPlan, error) {
	s.mu.Lock()
	plan, ok := s.plans[planID]
	if !ok {
		s.mu.Unlock()
		return ExecutionPlan{}, ErrPlanNotFound
	}
	if plan.State == PlanStateSucceeded {
		s.mu.Unlock()
		return clonePlan(plan), nil
	}
	if !s.now().UTC().Before(plan.ExpiresAt) {
		plan.State = PlanStateExpired
		s.plans[planID] = plan
		_ = s.savePlanLocked(ctx, plan)
		s.mu.Unlock()
		return clonePlan(plan), ErrPlanExpired
	}
	if plan.State != PlanStateConfirmed {
		s.mu.Unlock()
		return clonePlan(plan), ErrPlanState
	}
	plan.State = PlanStateRunning
	s.plans[planID] = plan
	if err := s.savePlanLocked(ctx, plan); err != nil {
		s.mu.Unlock()
		return ExecutionPlan{}, err
	}
	s.mu.Unlock()

	disk, err := s.findEligibleDisk(ctx, plan.DiskID)
	if err != nil || FingerprintDisk(disk) != plan.Fingerprint {
		return s.needsAttention(planID, ErrDiskChanged)
	}
	if s.executor == nil {
		return s.needsAttention(planID, errors.New("volume executor is unavailable"))
	}
	volume, err := s.executor.CreateVolume(ctx, CreateVolumeRequest{
		PlanID: plan.ID, DiskID: plan.DiskID, Fingerprint: plan.Fingerprint,
	})
	if err != nil {
		return s.needsAttention(planID, err)
	}

	s.mu.Lock()
	plan = s.plans[planID]
	plan.State = PlanStateSucceeded
	plan.Volume = &volume
	s.plans[planID] = plan
	if err := s.savePlanLocked(ctx, plan); err != nil {
		s.mu.Unlock()
		return ExecutionPlan{}, err
	}
	s.mu.Unlock()
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
	s.mu.Lock()
	plan := s.plans[planID]
	plan.State = PlanStateNeedsAttention
	plan.Failure = cause.Error()
	s.plans[planID] = plan
	_ = s.savePlanLocked(context.Background(), plan)
	s.mu.Unlock()
	return clonePlan(plan), fmt.Errorf("%w: %v", ErrOperationAttention, cause)
}

func (s *Service) ListVolumes(ctx context.Context) ([]Volume, error) {
	s.mu.Lock()
	volumes := make([]Volume, 0, 1)
	seen := make(map[string]struct{})
	for _, plan := range s.plans {
		if plan.State == PlanStateSucceeded && plan.Volume != nil {
			if _, ok := seen[plan.Volume.ID]; ok {
				continue
			}
			seen[plan.Volume.ID] = struct{}{}
			volumes = append(volumes, *plan.Volume)
		}
	}
	s.mu.Unlock()
	state, readErr := s.reader.Read(ctx)
	for index := range volumes {
		volumes[index].State = VolumeStateUnavailable
		if readErr != nil {
			continue
		}
		for _, disk := range state.Disks {
			if disk.ID.String() == volumes[index].DiskID {
				volumes[index].CapacityBytes = disk.CapacityBytes
				if disk.Role == hoststate.DiskRoleData {
					volumes[index].State = VolumeStateAvailable
				}
				break
			}
		}
	}
	return volumes, nil
}

func (s *Service) ListAudit(ctx context.Context) ([]AuditEvent, error) {
	if s.store == nil {
		return nil, nil
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT id, actor_user_id, action, resource_type, resource_id, occurred_at, detail
FROM audit_events ORDER BY occurred_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []AuditEvent
	for rows.Next() {
		var event AuditEvent
		var occurredAt string
		if err := rows.Scan(&event.ID, &event.ActorUserID, &event.Action, &event.ResourceType, &event.ResourceID, &occurredAt, &event.Detail); err != nil {
			return nil, err
		}
		event.OccurredAt, _ = time.Parse(time.RFC3339Nano, occurredAt)
		events = append(events, event)
	}
	return events, rows.Err()
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
		if plan.State == PlanStateRunning {
			plan.State = PlanStateNeedsAttention
			plan.Failure = "host service restarted while the destructive operation was running; inspect the disk before retrying"
			if err := s.savePlanLocked(ctx, plan); err != nil {
				return err
			}
		}
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

func (s *Service) findEligibleDisk(ctx context.Context, diskID string) (hoststate.Disk, error) {
	state, err := s.reader.Read(ctx)
	if err != nil {
		return hoststate.Disk{}, err
	}
	for _, disk := range state.Disks {
		if disk.ID.String() != diskID {
			continue
		}
		if disk.Role != hoststate.DiskRoleUnassigned || disk.Transport == hoststate.TransportUSB || disk.Transport == hoststate.TransportUnknown || disk.Removable || disk.InUse {
			return hoststate.Disk{}, ErrDiskNotEligible
		}
		return disk, nil
	}
	return hoststate.Disk{}, ErrDiskNotFound
}

func FingerprintDisk(disk hoststate.Disk) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%s", disk.ID.String(), disk.Model, disk.CapacityBytes, disk.Transport)))
	return hex.EncodeToString(digest[:16])
}

func confirmationPhrase(diskID string) string {
	const visible = 7
	if len(diskID) > visible {
		diskID = diskID[len(diskID)-visible:]
	}
	return "ERASE " + diskID
}

func clonePlan(plan ExecutionPlan) ExecutionPlan {
	plan.Actions = append([]Action(nil), plan.Actions...)
	plan.Signatures = append([]string(nil), plan.Signatures...)
	if plan.Volume != nil {
		volume := *plan.Volume
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
