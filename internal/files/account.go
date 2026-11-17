package files

import (
	"context"
	"errors"
)

// ErrSnapshotInProgress refuses to forget a space while a snapshot of it is
// being taken; asking again later succeeds.
var ErrSnapshotInProgress = errors.New("a snapshot of the space is being taken")

// RemoveAccount forgets a deleted account's personal space and the catalog's
// records of the account (docs/specs/account-enable-and-delete.md). The
// space's snapshots are deleted from the data volume first, while the Host
// Agent still knows the space; then its schedule, files and trash records go,
// and the account's trash records in other spaces, whose files the Host Agent
// deletes with the account's trash folders. Activity records stay. It runs
// again after a failure.
func (s *Service) RemoveAccount(ctx context.Context, userID, spaceID string) error {
	if userID == "" || spaceID == "" || spaceID == "space:shared" {
		return errors.New("invalid account removal")
	}
	// No scheduled snapshot starts once its schedule is gone.
	if _, err := s.store.db.ExecContext(ctx, "DELETE FROM snapshot_schedules WHERE space_id = ?", spaceID); err != nil {
		return err
	}
	unlock, err := s.lockSnapshotRetention(ctx, spaceID)
	if err != nil {
		return err
	}
	defer unlock()
	pending, err := s.pendingSnapshotsOf(ctx, spaceID)
	if err != nil {
		return err
	}
	for _, snapshotID := range pending {
		s.snapshotsMu.Lock()
		_, taking := s.takingSnapshots[snapshotID]
		s.snapshotsMu.Unlock()
		if taking {
			return ErrSnapshotInProgress
		}
		if err := s.snapshots.Delete(ctx, spaceID, snapshotID); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if _, err := s.store.db.ExecContext(ctx, "DELETE FROM pending_snapshots WHERE id = ?", snapshotID); err != nil {
			return err
		}
	}
	snapshots, err := s.spaceSnapshots(ctx, spaceID, "")
	if err != nil {
		return err
	}
	for _, snapshot := range snapshots {
		if err := s.deleteSnapshot(ctx, snapshot); err != nil {
			return err
		}
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{"DELETE FROM trash WHERE space_id = ? OR deleted_by = ?", []any{spaceID, userID}},
		{"DELETE FROM entries WHERE space_id = ?", []any{spaceID}},
	} {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.spaceRootsMu.Lock()
	delete(s.scheduledSpaces, spaceID)
	delete(s.spaceRoots, spaceID)
	s.spaceRootsMu.Unlock()
	return nil
}

func (s *Service) pendingSnapshotsOf(ctx context.Context, spaceID string) ([]string, error) {
	rows, err := s.store.db.QueryContext(ctx, "SELECT id FROM pending_snapshots WHERE space_id = ? ORDER BY created_at", spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
