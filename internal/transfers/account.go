package transfers

import "context"

// RemoveAccount forgets a deleted account's transfers
// (docs/specs/account-enable-and-delete.md). None can still move bytes: the
// account has had no session since it was disabled. An upload's temporary
// file is left for the reconciliation of its folder, which removes stale
// ones.
func (s *Service) RemoveAccount(ctx context.Context, userID string) error {
	s.mu.Lock()
	for id, live := range s.live {
		if live.owner.ID != userID {
			continue
		}
		live.canceled = true
		if live.cancel != nil {
			live.cancel()
		}
		delete(s.live, id)
	}
	s.mu.Unlock()
	_, err := s.store.db.ExecContext(ctx, "DELETE FROM transfers WHERE owner_id = ?", userID)
	return err
}
