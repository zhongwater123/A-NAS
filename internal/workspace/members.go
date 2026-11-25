package workspace

import (
	"context"
	"time"

	"github.com/zhongwater123/A-NAS/internal/activity"
)

// Member is an account an administrator let use the workspace.
type Member struct {
	AccountID string    `json:"accountId"`
	AddedBy   string    `json:"addedBy"`
	AddedAt   time.Time `json:"addedAt"`
}

// CanUse reports whether actor may use the workspace: administrators always,
// members once an administrator added them.
func (s *Store) CanUse(ctx context.Context, actor Actor) (bool, error) {
	if actor.Admin {
		return true, nil
	}
	var found int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM members WHERE account_id = ?", actor.AccountID).Scan(&found)
	return found > 0, err
}

// Members lists the accounts an administrator added.
func (s *Store) Members(ctx context.Context) ([]Member, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT account_id, added_by, added_at FROM members ORDER BY added_at, account_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := []Member{}
	for rows.Next() {
		var member Member
		var addedAt string
		if err := rows.Scan(&member.AccountID, &member.AddedBy, &addedAt); err != nil {
			return nil, err
		}
		member.AddedAt = parseTime(addedAt)
		members = append(members, member)
	}
	return members, rows.Err()
}

// AddMember lets an account use the workspace; adding it again changes
// nothing.
func (s *Store) AddMember(ctx context.Context, actor Actor, accountID string) error {
	if !actor.Admin {
		return ErrForbidden
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, "INSERT INTO members(account_id, added_by, added_at) VALUES(?,?,?) ON CONFLICT(account_id) DO NOTHING",
		accountID, actor.AccountID, formatTime(s.now()))
	if err != nil {
		return err
	}
	if added, _ := result.RowsAffected(); added > 0 {
		if err := s.audit(ctx, tx, actor, "workspace.member_added", "member", accountID, ""); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RemoveMember stops an account from using the workspace. What it saved
// stays; its agent tokens are refused while it is not a member.
func (s *Store) RemoveMember(ctx context.Context, actor Actor, accountID string) error {
	if !actor.Admin {
		return ErrForbidden
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, "DELETE FROM members WHERE account_id = ?", accountID)
	if err != nil {
		return err
	}
	if removed, _ := result.RowsAffected(); removed > 0 {
		if err := s.audit(ctx, tx, actor, "workspace.member_removed", "member", accountID, ""); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.live.forgetAccount(accountID)
	return nil
}

// RemoveAccount forgets a deleted account: its place among the members, its
// transfer links and what it was looking at. The pages and versions it saved
// stay under the name it had.
func (s *Store) RemoveAccount(ctx context.Context, accountID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []string{
		"DELETE FROM members WHERE account_id = ?",
		"DELETE FROM transfers WHERE account_id = ?",
		"DELETE FROM audit_events WHERE actor_user_id = ?",
	} {
		if _, err := tx.ExecContext(ctx, statement, accountID); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.live.forgetAccount(accountID)
	return nil
}

// Activity returns only this account's events; administrators see no one
// else's here either.
func (s *Store) Activity(ctx context.Context, actor Actor, q activity.Query) ([]activity.Event, error) {
	q.Subject = &activity.Subject{ActorIDs: []string{actor.AccountID}}
	q.Reveal, q.TextActors = nil, nil
	return activity.Table{DB: s.db}.List(ctx, q)
}

// Upkeep removes expired transfer links and audit events past AuditDays.
func (s *Store) Upkeep(ctx context.Context) error {
	now := s.now()
	if _, err := s.db.ExecContext(ctx, "DELETE FROM transfers WHERE expires_at <= ?", formatTime(now)); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM audit_events WHERE occurred_at < ?", formatTime(now.AddDate(0, 0, -AuditDays)))
	return err
}
