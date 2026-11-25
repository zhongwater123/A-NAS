package family

import (
	"context"
	"database/sql"
	"errors"
)

// RemoveAccount forgets a deleted account
// (docs/specs/account-enable-and-delete.md): its membership, join request,
// seen hints and reminders, its own to-dos and the family audit only it
// could read. What it added to the channel, its comments and the shared
// to-dos it added, closed or owns stay, under the name they were written
// with; the family gets a guardian again from the earliest active member if
// it was the last one.
func (s *Store) RemoveAccount(ctx context.Context, accountID string) error {
	if accountID == "" {
		return errors.New("invalid account removal")
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, statement := range []string{
			"DELETE FROM members WHERE account_id = ?",
			"DELETE FROM join_requests WHERE account_id = ?",
			"DELETE FROM hints_seen WHERE account_id = ?",
			"DELETE FROM audit_events WHERE actor_user_id = ?",
			"DELETE FROM todos WHERE viewer_id = ?",
		} {
			if _, err := tx.ExecContext(ctx, statement, accountID); err != nil {
				return err
			}
		}
		_, err := s.loadMembership(ctx, tx)
		return err
	})
}
