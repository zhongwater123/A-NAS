package music

import "context"

// RemoveAccount forgets a deleted account
// (docs/specs/account-enable-and-delete.md): its favorites, plays,
// playlists, sources and settings, and the tracks found in its personal space.
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
		{"DELETE FROM favorites WHERE user_id = ?", userID},
		{"DELETE FROM plays WHERE user_id = ?", userID},
		// Their tracks go with the playlists.
		{"DELETE FROM playlists WHERE user_id = ?", userID},
		{"DELETE FROM sources WHERE user_id = ?", userID},
		{"DELETE FROM settings WHERE user_id = ?", userID},
		{"DELETE FROM tracks WHERE space_id = ?", spaceID},
	} {
		if _, err := tx.ExecContext(ctx, statement.query, statement.arg); err != nil {
			return err
		}
	}
	return tx.Commit()
}
