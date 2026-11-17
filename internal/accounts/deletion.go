package accounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
)

// DeletionStep removes what one part of A-NAS keeps for a deleted account,
// and says what it handed to the administrator. It runs again after a
// failure, so what is already gone counts as removed.
type DeletionStep struct {
	Name   string
	Remove func(context.Context, Removal) (Handover, error)
}

// ForgetStep is a step that hands nothing to the administrator.
func ForgetStep(name string, forget func(context.Context, Removal) error) DeletionStep {
	return DeletionStep{Name: name, Remove: func(ctx context.Context, removal Removal) (Handover, error) {
		return Handover{}, forget(ctx, removal)
	}}
}

// Handover is what a step gave the administrator who deleted the account:
// the folder of their personal space and the album of their private library
// that hold its files and photos, with the names they got, which have a
// number added when the plain one was taken. Empty is nothing.
type Handover struct {
	Folder string
	Album  string
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
	Administrator    User
	AdministratorUID int
}

// deletionRetry is how soon a deletion that failed is tried again.
const deletionRetry = 15 * time.Minute

// deletionCheck is how long RunDeletions waits at most before it looks for
// due deletions again, however far the next one is.
const deletionCheck = time.Hour

// deletionStepTimeout bounds one step, so that a service that never answers
// holds up the other deletions only this long. A step that runs out is tried
// again; the Host Agent finishes a removal whose answer was not awaited.
const deletionStepTimeout = time.Hour

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
	due, wait, err := s.dueDeletions(ctx)
	if err != nil {
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
			if errors.Is(err, errDeletionNotDue) {
				// Cancelled since the list was read; one asked for again
				// woke this loop when it was scheduled and has its own time.
				continue
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

// dueDeletions lists the accounts whose deletion is due, and how long until
// the next one is, at most deletionCheck.
func (s *Service) dueDeletions(ctx context.Context) ([]string, time.Duration, error) {
	wait := deletionCheck
	now := s.now().UTC()
	rows, err := s.store.db.QueryContext(ctx, "SELECT user_id, due_at FROM account_deletions ORDER BY due_at, user_id")
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var due []string
	for rows.Next() {
		var userID, dueAt string
		if err := rows.Scan(&userID, &dueAt); err != nil {
			return nil, 0, err
		}
		at, _ := time.Parse(time.RFC3339Nano, dueAt)
		if at.After(now) {
			wait = min(wait, at.Sub(now))
			continue
		}
		due = append(due, userID)
	}
	return due, wait, rows.Err()
}

// removeAccount runs the steps that remain for one due account, then removes
// the account. From the first step on, the deletion can no longer be
// cancelled.
func (s *Service) removeAccount(ctx context.Context, userID string, steps []DeletionStep) error {
	if err := s.startDeletion(ctx, userID); err != nil {
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
		stepCtx, cancel := context.WithTimeout(ctx, deletionStepTimeout)
		handover, err := step.Remove(stepCtx, removal)
		cancel()
		if err != nil {
			return fmt.Errorf("%s: %w", step.Name, err)
		}
		done = append(done, step.Name)
		// What a step handed over is kept with the step, for the notice once
		// every step is done, possibly attempts later.
		if _, err := s.store.db.ExecContext(ctx, `UPDATE account_deletions SET completed_steps = ?,
handed_folder = COALESCE(NULLIF(?, ''), handed_folder), handed_album = COALESCE(NULLIF(?, ''), handed_album)
WHERE user_id = ?`, strings.Join(done, ","), handover.Folder, handover.Album, userID); err != nil {
			return err
		}
	}
	return s.removeAccountRecords(ctx, removal)
}

// startDeletion records that the account's deletion has started, from when
// identity synchronization leaves the account out. It waits for a
// synchronization in progress, which may still be making the account on the
// host: one that read the account before this point and reached the Host
// Agent after the host step would make the removed account again.
//
// The deletion is read again here. Removing the accounts listed before this
// one, or waiting for the synchronization, can take hours, in which an
// administrator may have cancelled it, or cancelled it and asked again with
// other choices: that request waits its own 30 days. Only a deletion that is
// due now starts, and from then on it cannot be cancelled.
func (s *Service) startDeletion(ctx context.Context, userID string) error {
	s.identityMu.Lock()
	defer s.identityMu.Unlock()
	now := s.now().UTC()
	var dueAt string
	var startedAt sql.NullString
	err := s.store.db.QueryRowContext(ctx, "SELECT due_at, started_at FROM account_deletions WHERE user_id = ?", userID).Scan(&dueAt, &startedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return errDeletionNotDue
	}
	if err != nil {
		return err
	}
	if startedAt.Valid {
		return nil
	}
	if at, _ := time.Parse(time.RFC3339Nano, dueAt); at.After(now) {
		return errDeletionNotDue
	}
	// due_at names the request that was read: one made in between is not it.
	result, err := s.store.db.ExecContext(ctx, "UPDATE account_deletions SET started_at = ? WHERE user_id = ? AND started_at IS NULL AND due_at = ?",
		formatTime(now), userID, dueAt)
	if err != nil {
		return err
	}
	if started, err := result.RowsAffected(); err != nil {
		return err
	} else if started == 0 {
		return errDeletionNotDue
	}
	return nil
}

// errDeletionNotDue reports a deletion that was listed as due and, by the
// time its turn came, was cancelled or replaced by a later request.
var errDeletionNotDue = errors.New("the account's deletion is no longer due")

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
// step must have deleted the space's snapshots first. The Host Agent reads
// the removal from the control plane itself and only learns the account from
// this request (AccountRemoval).
func (s *Service) HostRemoval() DeletionStep {
	return DeletionStep{Name: "host", Remove: func(ctx context.Context, removal Removal) (Handover, error) {
		remover, ok := s.credentials.(AccountRemover)
		if !ok {
			return Handover{}, errors.New("account removal is unavailable")
		}
		request := AccountRemoval{UserID: removal.UserID, Username: removal.Username, UID: removal.UID, PrivateSpaceID: removal.PrivateSpaceID}
		if removal.PersonalSpace == DeletionTransfer {
			request.TransferTo = removal.Administrator.Username
			request.TransferToUID = removal.AdministratorUID
			request.Folder = HandoverFolder(removal.Username)
		}
		folder, err := remover.RemoveAccount(ctx, request)
		return Handover{Folder: folder}, err
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
	// The administrator is told the names the steps gave what they handed
	// over: a step that handed nothing over left its name empty.
	detail := deletionDetail{Username: removal.Username, PersonalSpace: removal.PersonalSpace, Library: removal.Library}
	if err := tx.QueryRowContext(ctx, "SELECT requested_by, handed_folder, handed_album FROM account_deletions WHERE user_id = ?",
		removal.UserID).Scan(&requestedBy, &detail.Folder, &detail.Album); err != nil {
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
