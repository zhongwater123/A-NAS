package accounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Enabling a disabled account again and deleting one
// (docs/specs/account-enable-and-delete.md).

var (
	// ErrHostEnablePending reports an account enabled again whose Web sign-in
	// works but whose SMB access or assistant could not be enabled yet.
	// Enabling again and identity synchronization finish them.
	ErrHostEnablePending = errors.New("the account is enabled, but its host services could not all be enabled yet")
	// ErrDeletionPending refuses to change an account that is waiting to be
	// deleted; cancelling the deletion comes first.
	ErrDeletionPending = errors.New("the account is waiting to be deleted")
	// ErrDeletionStarted refuses to cancel a deletion that has begun removing
	// the account's data.
	ErrDeletionStarted = errors.New("the account is being deleted")
	// ErrNotDisabled refuses to delete an account that is not disabled.
	ErrNotDisabled  = errors.New("only a disabled account can be deleted")
	ErrNoDeletion   = errors.New("the account is not waiting to be deleted")
	ErrInvalidInput = errors.New("invalid request")
)

// DefaultDeletionDelay is how long a deleted account waits, disabled and
// with its data untouched, before it is removed for good.
const DefaultDeletionDelay = 30 * 24 * time.Hour

// DeletionChoice is what happens to a part of a deleted account's data: the
// administrator who deleted it receives it, or it is deleted with the account.
type DeletionChoice string

const (
	DeletionTransfer DeletionChoice = "transfer"
	DeletionDelete   DeletionChoice = "delete"
)

func (c DeletionChoice) valid() bool { return c == DeletionTransfer || c == DeletionDelete }

// Deletion is an account waiting to be deleted, or being deleted.
type Deletion struct {
	DueAt         time.Time      `json:"dueAt"`
	PersonalSpace DeletionChoice `json:"personalSpace"`
	Library       DeletionChoice `json:"library"`
	// Started says the account's data is being removed; the deletion can no
	// longer be cancelled.
	Started bool `json:"started"`
	// Retrying says the last attempt to remove it failed; it is tried again.
	Retrying bool `json:"retrying,omitempty"`
}

// DeletionRequest is what an administrator chooses when deleting an account,
// with their own password again.
type DeletionRequest struct {
	Password      string
	PersonalSpace DeletionChoice
	Library       DeletionChoice
}

// deletionDetail is the audit detail of a deletion.
type deletionDetail struct {
	Username      string         `json:"username"`
	PersonalSpace DeletionChoice `json:"personalSpace,omitempty"`
	Library       DeletionChoice `json:"library,omitempty"`
	DueAt         *time.Time     `json:"dueAt,omitempty"`
	Folder        string         `json:"folder,omitempty"`
	Album         string         `json:"album,omitempty"`
}

func (d deletionDetail) String() string {
	encoded, _ := json.Marshal(d)
	return string(encoded)
}

// EnableUser gives a disabled account back its Web sign-in, SMB access and
// assistant, with the password it had. Shares it sent stay revoked. Enabling
// an active account again finishes what a failed attempt left on the host.
func (s *Service) EnableUser(ctx context.Context, actor User, userID string) error {
	if !actor.ActiveAdministrator() || actor.ID == userID {
		return ErrForbidden
	}
	var username string
	var status UserStatus
	var mustChange, deleting bool
	err := s.store.db.QueryRowContext(ctx, `SELECT u.username, u.status, u.must_change_password,
EXISTS (SELECT 1 FROM account_deletions d WHERE d.user_id = u.id)
FROM users u WHERE u.id = ?`, userID).Scan(&username, &status, &mustChange, &deleting)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUserNotFound
	}
	if err != nil {
		return err
	}
	enabler, ok := s.credentials.(CredentialEnabler)
	if !ok {
		return errors.New("credential enabler is unavailable")
	}
	// A synchronization that read the account as disabled finishes first
	// (identityMu): it would switch SMB off again after this.
	s.identityMu.Lock()
	defer s.identityMu.Unlock()
	switch {
	case deleting:
		return ErrDeletionPending
	case status == UserStatusActive:
		return s.enableHost(ctx, enabler, username, mustChange)
	case status != UserStatusDisabled:
		return ErrForbidden
	}
	// Web sign-in comes back first, as it ended first: the status is also
	// what identity synchronization uses to finish the host steps.
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE users SET status = 'active' WHERE id = ? AND status = 'disabled'
AND NOT EXISTS (SELECT 1 FROM account_deletions WHERE user_id = ?)`, userID, userID)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed == 0 {
		// A deletion was asked for since the account was read.
		return ErrDeletionPending
	}
	if err := s.appendAuditTo(ctx, tx, actor.ID, "user.enabled", "user", userID, ""); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.enableHost(ctx, enabler, username, mustChange)
}

// enableHost unlocks the account on the host. An account that must choose a
// new password keeps SMB and its assistant off until it does, as after a reset.
func (s *Service) enableHost(ctx context.Context, enabler CredentialEnabler, username string, mustChange bool) error {
	if err := enabler.EnableCredential(ctx, EnableRequest{Username: username, SMB: !mustChange}); err != nil {
		return fmt.Errorf("%w: %v", ErrHostEnablePending, err)
	}
	return nil
}

// ScheduleDeletion deletes a disabled member's account after the deletion
// delay. Until then nothing of it changes and the deletion can be cancelled;
// then its personal space and private photo library go to the administrator
// or are deleted, as chosen, and the rest is removed.
func (s *Service) ScheduleDeletion(ctx context.Context, actor User, userID string, request DeletionRequest) (User, error) {
	if !actor.ActiveAdministrator() || actor.ID == userID {
		return User{}, ErrForbidden
	}
	if !request.PersonalSpace.valid() || !request.Library.valid() {
		return User{}, ErrInvalidInput
	}
	if err := s.confirmPassword(ctx, actor, request.Password); err != nil {
		return User{}, err
	}
	user, err := scanUser(s.store.db.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE id = ?", userID))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, err
	}
	// The device has one administrator, who cannot be disabled.
	if user.Role != RoleMember {
		return User{}, ErrForbidden
	}
	if user.Status != UserStatusDisabled {
		return User{}, ErrNotDisabled
	}
	now := s.now().UTC()
	deletion := Deletion{DueAt: now.Add(s.deletionDelay), PersonalSpace: request.PersonalSpace, Library: request.Library}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `INSERT INTO account_deletions(user_id, requested_by, requested_at, due_at, personal_space, library)
SELECT ?,?,?,?,?,? WHERE EXISTS (SELECT 1 FROM users WHERE id = ? AND status = 'disabled')
ON CONFLICT(user_id) DO NOTHING`, userID, actor.ID, formatTime(now), formatTime(deletion.DueAt),
		request.PersonalSpace, request.Library, userID)
	if err != nil {
		return User{}, err
	}
	if inserted, err := result.RowsAffected(); err != nil {
		return User{}, err
	} else if inserted == 0 {
		if _, err := s.deletionOf(ctx, tx, userID); err == nil {
			return User{}, ErrDeletionPending
		}
		return User{}, ErrNotDisabled
	}
	detail := deletionDetail{Username: user.Username, PersonalSpace: request.PersonalSpace, Library: request.Library, DueAt: &deletion.DueAt}
	if err := s.appendAuditTo(ctx, tx, actor.ID, "user.deletion_scheduled", "user", userID, detail.String()); err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	s.wakeDeletions()
	user.Deletion = &deletion
	return user, nil
}

// CancelDeletion keeps an account that is waiting to be deleted; it stays
// disabled.
func (s *Service) CancelDeletion(ctx context.Context, actor User, userID string) error {
	if !actor.ActiveAdministrator() {
		return ErrForbidden
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var username string
	if err := tx.QueryRowContext(ctx, "SELECT username FROM users WHERE id = ?", userID).Scan(&username); errors.Is(err, sql.ErrNoRows) {
		return ErrUserNotFound
	} else if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM account_deletions WHERE user_id = ? AND started_at IS NULL", userID)
	if err != nil {
		return err
	}
	if removed, err := result.RowsAffected(); err != nil {
		return err
	} else if removed == 0 {
		if _, err := s.deletionOf(ctx, tx, userID); err == nil {
			return ErrDeletionStarted
		}
		return ErrNoDeletion
	}
	if err := s.appendAuditTo(ctx, tx, actor.ID, "user.deletion_cancelled", "user", userID, deletionDetail{Username: username}.String()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.wakeDeletions()
	return nil
}

// deletionOf reads an account's deletion through db.
func (s *Service) deletionOf(ctx context.Context, db sqlExecutor, userID string) (Deletion, error) {
	var deletion Deletion
	var dueAt string
	err := db.QueryRowContext(ctx, `SELECT due_at, personal_space, library, started_at IS NOT NULL, last_error <> ''
FROM account_deletions WHERE user_id = ?`, userID).Scan(&dueAt, &deletion.PersonalSpace, &deletion.Library, &deletion.Started, &deletion.Retrying)
	if err != nil {
		return Deletion{}, err
	}
	deletion.DueAt, _ = time.Parse(time.RFC3339Nano, dueAt)
	return deletion, nil
}

// deletions reads every account deletion by user ID.
func (s *Service) deletions(ctx context.Context) (map[string]Deletion, error) {
	rows, err := s.store.db.QueryContext(ctx, `SELECT user_id, due_at, personal_space, library, started_at IS NOT NULL, last_error <> ''
FROM account_deletions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	deletions := map[string]Deletion{}
	for rows.Next() {
		var userID, dueAt string
		var deletion Deletion
		if err := rows.Scan(&userID, &dueAt, &deletion.PersonalSpace, &deletion.Library, &deletion.Started, &deletion.Retrying); err != nil {
			return nil, err
		}
		deletion.DueAt, _ = time.Parse(time.RFC3339Nano, dueAt)
		deletions[userID] = deletion
	}
	return deletions, rows.Err()
}

// confirmPassword checks an administrator's own password again, for an action
// a session alone must not be enough for.
func (s *Service) confirmPassword(ctx context.Context, actor User, password string) error {
	var encodedHash string
	if err := s.store.db.QueryRowContext(ctx, "SELECT password_hash FROM users WHERE id = ?", actor.ID).Scan(&encodedHash); err != nil || encodedHash == "" {
		return ErrInvalidCredentials
	}
	if matches, err := s.passwords.matches(ctx, password, encodedHash); err != nil || !matches {
		return ErrInvalidCredentials
	}
	return nil
}
