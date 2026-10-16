package files

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// ScheduleFrequency is how often a space's schedule takes a snapshot.
type ScheduleFrequency string

const (
	FrequencyHourly ScheduleFrequency = "hourly"
	FrequencyDaily  ScheduleFrequency = "daily"
	FrequencyWeekly ScheduleFrequency = "weekly"
)

// RetentionMode decides which scheduled snapshots a schedule keeps.
type RetentionMode string

const (
	// RetentionCount keeps the newest Keep scheduled snapshots.
	RetentionCount RetentionMode = "count"
	// RetentionTiered keeps the newest snapshot of each of the latest hours,
	// days, weeks and months, so a frequent schedule still reaches far back.
	RetentionTiered RetentionMode = "tiered"
)

type ScheduleRunStatus string

const (
	ScheduleRunSucceeded ScheduleRunStatus = "succeeded"
	ScheduleRunFailed    ScheduleRunStatus = "failed"
)

const (
	// MaxScheduledSnapshots bounds what one schedule keeps, because every
	// snapshot holds a catalog row per file.
	MaxScheduledSnapshots = 100
	// A failed run is retried after scheduleRetryDelay until the next run is due.
	scheduleRetryDelay = 15 * time.Minute
	scheduleTick       = time.Minute
	// systemActor records scheduled snapshots and their expiry in the audit log.
	systemActor = "system"
)

// SnapshotScheduleSettings is what a person sets on a space's schedule.
type SnapshotScheduleSettings struct {
	Enabled   bool              `json:"enabled"`
	Frequency ScheduleFrequency `json:"frequency"`
	// Time is the local "HH:MM" of daily and weekly runs; hourly runs use its minute.
	Time string `json:"time"`
	// Weekday is the day of weekly runs, 0 for Sunday through 6 for Saturday.
	Weekday int `json:"weekday"`
	// Keep is how many scheduled snapshots stay under RetentionCount; older
	// ones are deleted after each successful run. Manual snapshots are never
	// deleted.
	Keep int `json:"keep"`
	// Retention is RetentionCount when empty.
	Retention RetentionMode `json:"retention"`
	// Tiers apply under RetentionTiered.
	Tiers RetentionTiers `json:"tiers"`
}

// RetentionTiers says how many hours, days, weeks and months keep their
// newest scheduled snapshot. Periods without a snapshot do not count, so a
// device that was off still keeps as many restore points. A schedule never
// sets a tier finer than its frequency: daily schedules keep no hours, and
// weekly ones neither hours nor days.
type RetentionTiers struct {
	Hourly  int `json:"hourly"`
	Daily   int `json:"daily"`
	Weekly  int `json:"weekly"`
	Monthly int `json:"monthly"`
}

func (t RetentionTiers) total() int { return t.Hourly + t.Daily + t.Weekly + t.Monthly }

// SnapshotSchedule is one space's schedule and how it last ran. A space
// without a stored schedule reports a disabled default.
type SnapshotSchedule struct {
	SpaceID string `json:"spaceId"`
	SnapshotScheduleSettings
	UpdatedAt time.Time    `json:"updatedAt,omitzero"`
	LastRun   *ScheduleRun `json:"lastRun,omitempty"`
	// NextRunAt is when the next attempt is expected; zero when disabled.
	NextRunAt time.Time `json:"nextRunAt,omitzero"`
	// CanManage tells the caller whether they may change the schedule.
	CanManage   bool `json:"canManage"`
	minuteOfDay int
}

// ScheduleRun is the outcome of a schedule's latest attempt.
type ScheduleRun struct {
	At     time.Time         `json:"at"`
	Status ScheduleRunStatus `json:"status"`
	// Error is a stable code when the run failed: volume_unavailable,
	// space_unavailable or snapshot_failed.
	Error      string `json:"error,omitempty"`
	SnapshotID string `json:"snapshotId,omitempty"`
}

// SpaceLookup finds a space without a signed-in user. Scheduled snapshots are
// taken for the space rather than for a session, by the snapshot backend.
type SpaceLookup interface {
	Space(context.Context, string) (accounts.Space, error)
	// ActiveSpaceIDs lists the shared space and the private spaces of
	// active accounts, which get the default schedule until someone sets one.
	ActiveSpaceIDs(context.Context) ([]string, error)
}

// defaultSchedule protects a space nobody has set a schedule for: a daily
// snapshot at 03:00, keeping the latest 14.
func defaultSchedule(spaceID string) SnapshotSchedule {
	return SnapshotSchedule{SpaceID: spaceID, SnapshotScheduleSettings: SnapshotScheduleSettings{
		Enabled: true, Frequency: FrequencyDaily, Time: "03:00", Keep: 14, Retention: RetentionCount,
	}, minuteOfDay: 3 * 60}
}

// SnapshotSchedule returns the schedule of a space the actor can see. It reads
// only the catalog, so it answers while the data volume is offline.
func (s *Service) SnapshotSchedule(ctx context.Context, actor accounts.User, spaceID string) (SnapshotSchedule, error) {
	space, err := s.visibleSpace(ctx, actor, spaceID)
	if err != nil {
		return SnapshotSchedule{}, err
	}
	schedules, err := s.loadSchedules(ctx, "WHERE space_id = ?", spaceID)
	if err != nil {
		return SnapshotSchedule{}, err
	}
	schedule := defaultSchedule(spaceID)
	// The default counts from now, as the scheduler records it the first
	// time it sees the space; it never snapshots a slot already past.
	schedule.UpdatedAt = s.now().UTC()
	if len(schedules) == 1 {
		schedule = schedules[0]
	}
	schedule.CanManage = canManageSnapshots(actor, space)
	schedule.NextRunAt = schedule.nextRun(s.now(), s.location)
	return schedule, nil
}

func (s *Service) UpdateSnapshotSchedule(ctx context.Context, actor accounts.User, spaceID string, settings SnapshotScheduleSettings) (SnapshotSchedule, error) {
	space, err := s.visibleSpace(ctx, actor, spaceID)
	if err != nil {
		return SnapshotSchedule{}, err
	}
	if !canManageSnapshots(actor, space) {
		return SnapshotSchedule{}, ErrForbidden
	}
	minuteOfDay, err := settings.minuteOfDay()
	if err != nil {
		return SnapshotSchedule{}, err
	}
	if settings.Retention == "" {
		settings.Retention = RetentionCount
	}
	if err := settings.checkRetention(); err != nil {
		return SnapshotSchedule{}, err
	}
	tiers := settings.Tiers
	if _, err := s.store.db.ExecContext(ctx, `
INSERT INTO snapshot_schedules(space_id, enabled, frequency, minute_of_day, weekday, keep, retention,
    keep_hourly, keep_daily, keep_weekly, keep_monthly, updated_by, updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(space_id) DO UPDATE SET enabled = excluded.enabled, frequency = excluded.frequency,
    minute_of_day = excluded.minute_of_day, weekday = excluded.weekday, keep = excluded.keep,
    retention = excluded.retention, keep_hourly = excluded.keep_hourly, keep_daily = excluded.keep_daily,
    keep_weekly = excluded.keep_weekly, keep_monthly = excluded.keep_monthly,
    updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
		spaceID, settings.Enabled, settings.Frequency, minuteOfDay, settings.Weekday, settings.Keep, settings.Retention,
		tiers.Hourly, tiers.Daily, tiers.Weekly, tiers.Monthly, actor.ID, formatTime(s.now())); err != nil {
		return SnapshotSchedule{}, err
	}
	detail := "disabled"
	if settings.Enabled {
		detail = fmt.Sprintf("%s %s weekday=%d keep=%d", settings.Frequency, settings.Time, settings.Weekday, settings.Keep)
		if settings.Retention == RetentionTiered {
			detail = fmt.Sprintf("%s %s weekday=%d tiered hourly=%d daily=%d weekly=%d monthly=%d", settings.Frequency, settings.Time,
				settings.Weekday, tiers.Hourly, tiers.Daily, tiers.Weekly, tiers.Monthly)
		}
	}
	_ = s.audit(ctx, actor.ID, "snapshot.schedule_updated", spaceID, spaceID, detail)
	return s.SnapshotSchedule(ctx, actor, spaceID)
}

func (settings SnapshotScheduleSettings) minuteOfDay() (int, error) {
	switch settings.Frequency {
	case FrequencyHourly, FrequencyDaily, FrequencyWeekly:
	default:
		return 0, ErrInvalidSchedule
	}
	if settings.Weekday < 0 || settings.Weekday > 6 || settings.Keep < 1 || settings.Keep > MaxScheduledSnapshots {
		return 0, ErrInvalidSchedule
	}
	parsed, err := time.Parse("15:04", settings.Time)
	if err != nil || len(settings.Time) != len("15:04") {
		return 0, ErrInvalidSchedule
	}
	return parsed.Hour()*60 + parsed.Minute(), nil
}

// checkRetention accepts tiers only under RetentionTiered, where at least one
// tier keeps something, none is finer than the frequency, and together they
// keep at most MaxScheduledSnapshots.
func (settings SnapshotScheduleSettings) checkRetention() error {
	tiers := settings.Tiers
	switch settings.Retention {
	case RetentionCount:
		if tiers != (RetentionTiers{}) {
			return ErrInvalidSchedule
		}
		return nil
	case RetentionTiered:
	default:
		return ErrInvalidSchedule
	}
	if tiers.Hourly < 0 || tiers.Daily < 0 || tiers.Weekly < 0 || tiers.Monthly < 0 ||
		tiers.total() < 1 || tiers.total() > MaxScheduledSnapshots {
		return ErrInvalidSchedule
	}
	if (settings.Frequency != FrequencyHourly && tiers.Hourly > 0) || (settings.Frequency == FrequencyWeekly && tiers.Daily > 0) {
		return ErrInvalidSchedule
	}
	return nil
}

// visibleSpace authorizes reading a space's catalog records, which live on the
// system disk and stay readable while the data volume is offline.
func (s *Service) visibleSpace(ctx context.Context, actor accounts.User, spaceID string) (accounts.Space, error) {
	if s.policy == nil {
		return accounts.Space{}, ErrForbidden
	}
	allowed, err := s.policy.CanAccessSpace(ctx, actor, spaceID, false)
	if err != nil || !allowed {
		return accounts.Space{}, ErrForbidden
	}
	spaces, err := s.policy.ListSpaces(ctx, actor)
	if err != nil {
		return accounts.Space{}, err
	}
	for _, space := range spaces {
		if space.ID == spaceID {
			return space, nil
		}
	}
	return accounts.Space{}, ErrForbidden
}

func (s *Service) loadSchedules(ctx context.Context, where string, args ...any) ([]SnapshotSchedule, error) {
	rows, err := s.store.db.QueryContext(ctx, `
SELECT space_id, enabled, frequency, minute_of_day, weekday, keep, retention, keep_hourly, keep_daily, keep_weekly, keep_monthly,
    updated_at, last_run_at, last_status, last_error, last_snapshot_id
FROM snapshot_schedules `+where+` ORDER BY space_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var schedules []SnapshotSchedule
	for rows.Next() {
		var schedule SnapshotSchedule
		var updatedAt, lastRunAt, lastStatus, lastError, lastSnapshotID string
		tiers := &schedule.Tiers
		if err := rows.Scan(&schedule.SpaceID, &schedule.Enabled, &schedule.Frequency, &schedule.minuteOfDay, &schedule.Weekday,
			&schedule.Keep, &schedule.Retention, &tiers.Hourly, &tiers.Daily, &tiers.Weekly, &tiers.Monthly,
			&updatedAt, &lastRunAt, &lastStatus, &lastError, &lastSnapshotID); err != nil {
			return nil, err
		}
		schedule.Time = fmt.Sprintf("%02d:%02d", schedule.minuteOfDay/60, schedule.minuteOfDay%60)
		schedule.UpdatedAt = parseOptionalTime(updatedAt)
		if at := parseOptionalTime(lastRunAt); !at.IsZero() {
			schedule.LastRun = &ScheduleRun{At: at, Status: ScheduleRunStatus(lastStatus), Error: lastError, SnapshotID: lastSnapshotID}
		}
		schedules = append(schedules, schedule)
	}
	return schedules, rows.Err()
}

// latestSlot is the most recent time at or before now that the schedule
// named, in the given time zone.
func (s SnapshotSchedule) latestSlot(now time.Time, location *time.Location) time.Time {
	local := now.In(location)
	year, month, day := local.Date()
	hour, minute := s.minuteOfDay/60, s.minuteOfDay%60
	at := func(dayOffset, hour int) time.Time {
		return time.Date(year, month, day+dayOffset, hour, minute, 0, 0, location)
	}
	switch s.Frequency {
	case FrequencyHourly:
		if slot := at(0, local.Hour()); !slot.After(local) {
			return slot
		}
		return at(0, local.Hour()-1)
	case FrequencyWeekly:
		back := (int(local.Weekday()) - s.Weekday + 7) % 7
		if slot := at(-back, hour); !slot.After(local) {
			return slot
		}
		return at(-back-7, hour)
	default:
		if slot := at(0, hour); !slot.After(local) {
			return slot
		}
		return at(-1, hour)
	}
}

// slotAfter is the time the schedule names next after slot.
func (s SnapshotSchedule) slotAfter(slot time.Time, location *time.Location) time.Time {
	local := slot.In(location)
	year, month, day := local.Date()
	switch s.Frequency {
	case FrequencyHourly:
		return time.Date(year, month, day, local.Hour()+1, local.Minute(), 0, 0, location)
	case FrequencyWeekly:
		return time.Date(year, month, day+7, local.Hour(), local.Minute(), 0, 0, location)
	default:
		return time.Date(year, month, day+1, local.Hour(), local.Minute(), 0, 0, location)
	}
}

// due reports whether a snapshot should be taken now: the latest slot came
// after the schedule was last changed and has not been covered by a
// successful run, and any failed attempt in it is older than the retry delay.
// A device that was off over several slots therefore runs once on return.
func (s SnapshotSchedule) due(now time.Time, location *time.Location) bool {
	if !s.Enabled {
		return false
	}
	slot := s.latestSlot(now, location)
	if !slot.After(s.UpdatedAt) {
		return false
	}
	if s.LastRun == nil || s.LastRun.At.Before(slot) {
		return true
	}
	return s.LastRun.Status == ScheduleRunFailed && now.Sub(s.LastRun.At) >= scheduleRetryDelay
}

func (s SnapshotSchedule) nextRun(now time.Time, location *time.Location) time.Time {
	if !s.Enabled {
		return time.Time{}
	}
	if s.due(now, location) {
		return now.UTC()
	}
	slot := s.latestSlot(now, location)
	next := s.slotAfter(slot, location)
	if run := s.LastRun; run != nil && run.Status == ScheduleRunFailed && !run.At.Before(slot) && slot.After(s.UpdatedAt) {
		if retry := run.At.Add(scheduleRetryDelay); retry.Before(next) {
			return retry.UTC()
		}
	}
	return next.UTC()
}

// RunSnapshotSchedules takes the snapshots that schedules are due for, looking
// every minute until ctx ends.
func (s *Service) RunSnapshotSchedules(ctx context.Context, spaces SpaceLookup, logger *slog.Logger) {
	ticker := time.NewTicker(scheduleTick)
	defer ticker.Stop()
	for {
		if err := s.RunDueSnapshots(ctx, spaces, logger); err != nil && ctx.Err() == nil {
			logger.ErrorContext(ctx, "snapshot schedules failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// RunDueSnapshots runs every schedule that is due once. A failed snapshot is
// recorded on its schedule; only catalog failures are returned.
func (s *Service) RunDueSnapshots(ctx context.Context, spaces SpaceLookup, logger *slog.Logger) error {
	if err := s.recordDefaultSchedules(ctx, spaces); err != nil {
		return err
	}
	schedules, err := s.loadSchedules(ctx, "WHERE enabled = 1")
	if err != nil {
		return err
	}
	var failures []error
	for _, schedule := range schedules {
		if ctx.Err() != nil {
			break
		}
		now := s.now()
		if !schedule.due(now, s.location) {
			continue
		}
		if err := s.runSchedule(ctx, spaces, schedule, now, logger); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// recordDefaultSchedules writes the default schedule for every active space
// that has none, counting from now. A schedule someone saved, even a
// disabled one, is left alone.
func (s *Service) recordDefaultSchedules(ctx context.Context, spaces SpaceLookup) error {
	ids, err := spaces.ActiveSpaceIDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		schedule := defaultSchedule(id)
		if _, err := s.store.db.ExecContext(ctx, `
INSERT INTO snapshot_schedules(space_id, enabled, frequency, minute_of_day, weekday, keep, updated_by, updated_at)
VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(space_id) DO NOTHING`,
			id, schedule.Enabled, schedule.Frequency, schedule.minuteOfDay, schedule.Weekday, schedule.Keep, "system", formatTime(s.now())); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) runSchedule(ctx context.Context, spaces SpaceLookup, schedule SnapshotSchedule, now time.Time, logger *slog.Logger) error {
	snapshot, err := s.takeScheduledSnapshot(ctx, spaces, schedule.SpaceID, now)
	run := ScheduleRun{At: s.now().UTC(), Status: ScheduleRunSucceeded, SnapshotID: snapshot.ID}
	if err != nil {
		run.Status, run.Error = ScheduleRunFailed, scheduleErrorCode(err)
		logger.WarnContext(ctx, "scheduled snapshot failed", "space_id", schedule.SpaceID, "error", err)
	}
	if _, err := s.store.db.ExecContext(ctx,
		"UPDATE snapshot_schedules SET last_run_at = ?, last_status = ?, last_error = ?, last_snapshot_id = ? WHERE space_id = ?",
		formatTime(run.At), run.Status, run.Error, run.SnapshotID, schedule.SpaceID); err != nil {
		return err
	}
	if err == nil {
		if err := s.expireScheduledSnapshots(ctx, schedule); err != nil {
			logger.ErrorContext(ctx, "expiring scheduled snapshots failed", "space_id", schedule.SpaceID, "error", err)
		}
	}
	return nil
}

func (s *Service) takeScheduledSnapshot(ctx context.Context, spaces SpaceLookup, spaceID string, now time.Time) (Snapshot, error) {
	space, err := spaces.Space(ctx, spaceID)
	if err != nil {
		return Snapshot{}, err
	}
	if !s.allowUnverifiedVolume {
		if s.volumeGuard == nil || s.volumeGuard.Check(ctx, s.volumeRoot, false) != nil {
			return Snapshot{}, ErrVolumeUnavailable
		}
	}
	s.spaceRootsMu.Lock()
	s.scheduledSpaces[space.ID] = space
	s.spaceRootsMu.Unlock()
	local := now.In(s.location)
	name := "定时快照 " + local.Format("2006-01-02 15:04")
	snapshot, err := s.createSnapshot(ctx, space.ID, systemActor, name, SnapshotOriginScheduled)
	if errors.Is(err, ErrConflict) {
		// A retry within the minute of an earlier snapshot.
		snapshot, err = s.createSnapshot(ctx, space.ID, systemActor, name+local.Format(":05"), SnapshotOriginScheduled)
	}
	return snapshot, err
}

// expireScheduledSnapshots deletes the scheduled snapshots of a space that
// its retention no longer keeps. Manual snapshots are never expired.
func (s *Service) expireScheduledSnapshots(ctx context.Context, schedule SnapshotSchedule) error {
	snapshots, err := s.spaceSnapshots(ctx, schedule.SpaceID, SnapshotOriginScheduled)
	if err != nil {
		return err
	}
	kept := schedule.retained(snapshots, s.location)
	var failures []error
	for _, snapshot := range snapshots {
		if kept[snapshot.ID] {
			continue
		}
		if err := s.deleteSnapshot(ctx, snapshot); err != nil {
			failures = append(failures, fmt.Errorf("expire snapshot %s: %w", snapshot.ID, err))
			continue
		}
		_ = s.audit(ctx, systemActor, "snapshot.expired", snapshot.SpaceID, snapshot.ID, snapshot.Name)
	}
	return errors.Join(failures...)
}

// retained names the scheduled snapshots, given newest first, that the
// schedule's retention keeps.
func (s SnapshotSchedule) retained(snapshots []Snapshot, location *time.Location) map[string]bool {
	kept := map[string]bool{}
	if s.Retention != RetentionTiered {
		for index, snapshot := range snapshots {
			if index < s.Keep {
				kept[snapshot.ID] = true
			}
		}
		return kept
	}
	for _, tier := range []struct {
		count  int
		period func(time.Time) string
	}{
		{s.Tiers.Hourly, func(at time.Time) string { return at.Format("2006-01-02 15") }},
		{s.Tiers.Daily, func(at time.Time) string { return at.Format("2006-01-02") }},
		{s.Tiers.Weekly, func(at time.Time) string { year, week := at.ISOWeek(); return fmt.Sprintf("%d-W%02d", year, week) }},
		{s.Tiers.Monthly, func(at time.Time) string { return at.Format("2006-01") }},
	} {
		// The newest snapshot of each period stands for it.
		periods, last := 0, ""
		for _, snapshot := range snapshots {
			if periods == tier.count {
				break
			}
			period := tier.period(snapshot.CreatedAt.In(location))
			if period == last {
				continue
			}
			last = period
			periods++
			kept[snapshot.ID] = true
		}
	}
	return kept
}

func scheduleErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrVolumeUnavailable):
		return "volume_unavailable"
	case errors.Is(err, accounts.ErrSpaceNotFound), errors.Is(err, sql.ErrNoRows):
		return "space_unavailable"
	default:
		return "snapshot_failed"
	}
}
