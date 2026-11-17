package media

import "context"

// RemoveAccount forgets a deleted account
// (docs/specs/account-enable-and-delete.md): the libraries in its personal
// space with their videos, and its progress, favorites and collections.
// Libraries it made in shared folders stay for everyone else.
func (s *Service) RemoveAccount(ctx context.Context, userID, spaceID string) error {
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []struct {
		query string
		arg   string
	}{
		// Folders, shows and videos go with their library, and favorites and
		// collection entries with the videos (triggers).
		{"DELETE FROM libraries WHERE space_id = ?", spaceID},
		{"DELETE FROM progress WHERE user_id = ?", userID},
		{"DELETE FROM favorites WHERE user_id = ?", userID},
		{"DELETE FROM collections WHERE owner_user_id = ?", userID},
	} {
		if _, err := tx.ExecContext(ctx, statement.query, statement.arg); err != nil {
			return err
		}
	}
	return tx.Commit()
}
