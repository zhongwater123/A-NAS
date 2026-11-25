package media

import "context"

// RemoveAccount forgets a deleted account
// (docs/specs/account-enable-and-delete.md): the libraries in its personal
// space with their videos, and its progress, favorites and collections.
// Libraries it made in shared folders stay for everyone else. The frames and
// converted subtitles cached for the videos go too, as when a library is
// deleted: they are pictures and text of what was in the personal space.
func (s *Service) RemoveAccount(ctx context.Context, userID, spaceID string) error {
	ids, err := s.videoIDs(ctx, "library_id IN (SELECT id FROM libraries WHERE space_id = ?)", spaceID)
	if err != nil {
		return err
	}
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
	if err := tx.Commit(); err != nil {
		return err
	}
	s.dropFrames(ids)
	return nil
}
