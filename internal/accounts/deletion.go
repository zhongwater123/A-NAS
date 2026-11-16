package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
)

// DeletionStep removes what one part of A-NAS keeps for a deleted account.
// It runs again after a failure, so what is already gone counts as removed.
type DeletionStep struct {
	Name   string
	Remove func(context.Context, Removal) error
}

// Removal is a deleted account as the steps that remove it see it.
type Removal struct {
	UserID         string
	Username       string
	UID            int
	PrivateSpaceID string
	PersonalSpace  DeletionChoice
	Library        DeletionChoice
	// Administrator asked for the deletion and receives what is transferred.
	Administrator        User
	AdministratorUID     int
	AdministratorSpaceID string
	// Folder and Album name what the administrator receives: a folder in
	// their personal space and an album in their private library.
	Folder string
	Album  string
}

// deletionRetry is how soon a deletion that failed is tried again.
const deletionRetry = 15 * time.Minute

// deletionCheck is how long RunDeletions waits at most before it looks for
// due deletions again, however far the next one is.
const deletionCheck = time.Hour

func (s *Service) wakeDeletions() {
	select {
	case s.deletionWake <- struct{}{}:
	default:
	}
}

// RunDeletions removes accounts whose deletion is due, until ctx ends. Steps
// run in order for each account; the account itself goes last. A failed
// account is tried again later, from the step that failed.
func (s *Service) RunDeletions(ctx context.Context, steps []DeletionStep, logger *slog.Logger) {
	for {
		wait := s.runDueDeletions(ctx, steps, logger)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.deletionWake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// runDueDeletions removes every due account and says how long to wait before
// looking again.
func (s *Service) runDueDeletions(ctx context.Context, steps []DeletionStep, logger *slog.Logger) time.Duration {
	wait := deletionCheck
	now := s.now().UTC()
	rows, err := s.store.db.QueryContext(ctx, "SELECT user_id, due_at FROM account_deletions ORDER BY due_at, user_id")
	if err != nil {
		logger.ErrorContext(ctx, "account deletions could not be read", "error", err)
		return deletionRetry
	}
	var due []string
	for rows.Next() {
		var userID, dueAt string
		if err := rows.Scan(&userID, &dueAt); err != nil {
			_ = rows.Close()
			logger.ErrorContext(ctx, "account deletions could not be read", "error", err)
			return deletionRetry
		}
		at, _ := time.Parse(time.RFC3339Nano, dueAt)
		if at.After(now) {
			wait = min(wait, at.Sub(now))
			continue
		}
		due = append(due, userID)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		logger.ErrorContext(ctx, "account deletions could not be read", "error", err)
		return deletionRetry
	}
	for _, userID := range due {
		if ctx.Err() != nil {
			return wait
		}
		if err := s.removeAccount(ctx, userID, steps); err != nil {
			if ctx.Err() != nil {
				return wait
			}
			logger.ErrorContext(ctx, "account deletion failed; it is tried again", "user_id", userID, "error", err)
			message := err.Error()
			if len(message) > 500 {
				message = message[:500]
			}
			if _, err := s.store.db.ExecContext(ctx, "UPDATE account_deletions SET last_error = ? WHERE user_id = ?", message, userID); err != nil {
				logger.ErrorContext(ctx, "account deletion failure could not be recorded", "user_id", userID, "error", err)
			}
			wait = min(wait, deletionRetry)
			continue
		}
		logger.InfoContext(ctx, "account deleted", "user_id", userID)
	}
	return max(wait, time.Second)
}

// removeAccount runs the steps that remain for one due account, then removes
// the account. From the first step on, the deletion can no longer be
// cancelled.
func (s *Service) removeAccount(ctx context.Context, userID string, steps []DeletionStep) error {
	if _, err := s.store.db.ExecContext(ctx, "UPDATE account_deletions SET started_at = ? WHERE user_id = ? AND started_at IS NULL",
		formatTime(s.now().UTC()), userID); err != nil {
		return err
	}
	removal, done, err := s.removalOf(ctx, userID)
	if err != nil {
		return err
	}
	for _, step := range steps {
		if slices.Contains(done, step.Name) {
			continue
		}
		if err := step.Remove(ctx, removal); err != nil {
			return fmt.Errorf("%s: %w", step.Name, err)
		}
		done = append(done, step.Name)
		if _, err := s.store.db.ExecContext(ctx, "UPDATE account_deletions SET completed_steps = ? WHERE user_id = ?",
			strings.Join(done, ","), userID); err != nil {
			return err
		}
	}
	return s.removeAccountRecords(ctx, removal)
}

// removalOf reads a started deletion and the steps it has completed.
func (s *Service) removalOf(ctx context.Context, userID string) (Removal, []string, error) {
	removal := Removal{UserID: userID}
	var requestedBy, completed string
	err := s.store.db.QueryRowContext(ctx, `SELECT u.username, i.uid, s.id, d.personal_space, d.library, d.requested_by, d.completed_steps
FROM account_deletions d
JOIN users u ON u.id = d.user_id
JOIN linux_identities i ON i.user_id = u.id
JOIN spaces s ON s.owner_user_id = u.id AND s.kind = 'private'
WHERE d.user_id = ?`, userID).Scan(&removal.Username, &removal.UID, &removal.PrivateSpaceID,
		&removal.PersonalSpace, &removal.Library, &requestedBy, &completed)
	if err != nil {
		return Removal{}, nil, fmt.Errorf("read account deletion: %w", err)
	}
	removal.Folder = HandoverFolder(removal.Username)
	removal.Album = HandoverAlbum(removal.Username)
	if removal.PersonalSpace == DeletionTransfer || removal.Library == DeletionTransfer {
		// Only the administrator who asked receives it, and only while they
		// still administer the device.
		admin, err := scanUser(s.store.db.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE id = ?", requestedBy))
		if err != nil || !admin.ActiveAdministrator() {
			return Removal{}, nil, errors.New("the administrator who deleted the account no longer administers the device")
		}
		removal.Administrator = admin
		if removal.AdministratorUID, err = s.uidForUser(ctx, admin.ID); err != nil {
			return Removal{}, nil, err
		}
		if err := s.store.db.QueryRowContext(ctx, "SELECT id FROM spaces WHERE kind = 'private' AND owner_user_id = ?", admin.ID).
			Scan(&removal.AdministratorSpaceID); err != nil {
			return Removal{}, nil, fmt.Errorf("read the administrator's personal space: %w", err)
		}
	}
	var done []string
	if completed != "" {
		done = strings.Split(completed, ",")
	}
	return removal, done, nil
}

// HostRemoval is the step that removes the account from the host: its Linux
// identity, SMB credential, assistant and the folders kept under its name,
// with its personal space handed to the administrator or deleted. The files
// step must have deleted the space's snapshots first.
func (s *Service) HostRemoval() DeletionStep {
	return DeletionStep{Name: "host", Remove: func(ctx context.Context, removal Removal) error {
		remover, ok := s.credentials.(AccountRemover)
		if !ok {
			return errors.New("account removal is unavailable")
		}
		request := AccountRemoval{Username: removal.Username, UID: removal.UID, PrivateSpaceID: removal.PrivateSpaceID}
		if removal.PersonalSpace == DeletionTransfer {
			request.TransferTo = removal.Administrator.Username
			request.TransferToUID = removal.AdministratorUID
			request.Folder = removal.Folder
		}
		return remover.RemoveAccount(ctx, request)
	}}
}

// removeAccountRecords removes the account and what this store keeps for it,
// all at once. Its Linux UID stays allocated: files in shared folders keep it
// as their owner, and it is never given to another account.
func (s *Service) removeAccountRecords(ctx context.Context, removal Removal) error {
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var requestedBy string
	if err := tx.QueryRowContext(ctx, "SELECT requested_by FROM account_deletions WHERE user_id = ?", removal.UserID).Scan(&requestedBy); err != nil {
		return err
	}
	for _, statement := range []string{
		"DELETE FROM notifications WHERE user_id = ?",
		"DELETE FROM viewing_grants WHERE owner_user_id = ?",
		"DELETE FROM spaces WHERE kind = 'private' AND owner_user_id = ?",
		// Sessions and the avatar go with the account.
		"DELETE FROM users WHERE id = ?",
		"DELETE FROM account_deletions WHERE user_id = ?",
	} {
		if _, err := tx.ExecContext(ctx, statement, removal.UserID); err != nil {
			return err
		}
	}
	detail := deletionDetail{Username: removal.Username, PersonalSpace: removal.PersonalSpace, Library: removal.Library}
	if removal.PersonalSpace == DeletionTransfer {
		detail.Folder = removal.Folder
	}
	if removal.Library == DeletionTransfer {
		detail.Album = removal.Album
	}
	if err := s.appendAuditTo(ctx, tx, requestedBy, "user.deleted", "user", removal.UserID, detail.String()); err != nil {
		return err
	}
	if detail.Folder != "" || detail.Album != "" {
		if err := s.notifyTo(ctx, tx, requestedBy, NotificationAccountDeleted, removal.Username,
			encodeHandover(detail.Folder, detail.Album)); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.sessions.forgetUser(removal.UserID)
	return nil
}

func encodeHandover(folder, album string) string {
	encoded, _ := json.Marshal(notificationDetail{Folder: folder, Album: album})
	return string(encoded)
}
