package shares

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
)

// ErrClosed reports a store that has been closed.
var ErrClosed = errors.New("share store is closed")

// RemoveAccount forgets a deleted account
// (docs/specs/account-enable-and-delete.md). Shares it sent go, with their
// content and conversations; they were revoked when it was disabled. From
// shares it received it is removed as a recipient, with its conversation,
// saves and notices. A share whose task still runs is left for the next
// attempt.
func (s *Store) RemoveAccount(ctx context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if userID == "" {
		return ErrInvalid
	}
	for id, r := range s.records {
		if r.Share.Sender.ID == userID {
			if err := s.removeSentLocked(ctx, r); err != nil {
				return err
			}
			delete(s.records, id)
			continue
		}
		if recipient(r, userID) == nil {
			continue
		}
		for jobID, run := range s.running {
			if run.shareID == id && run.user.ID == userID {
				run.cancel()
				delete(s.running, jobID)
			}
		}
		r.Share.Recipients = slices.DeleteFunc(r.Share.Recipients, func(p Recipient) bool { return p.UserID == userID })
		delete(r.Threads, userID)
		delete(r.Saved, userID)
		for jobID, job := range r.Jobs {
			if job.Job.UserID == userID {
				delete(r.Jobs, jobID)
			}
		}
		r.Events = slices.DeleteFunc(r.Events, func(e event) bool { return e.To == userID })
		if err := s.persist(r); err != nil {
			return err
		}
	}
	return nil
}

// removeSentLocked deletes a share the account sent and its content.
func (s *Store) removeSentLocked(ctx context.Context, r *record) error {
	if r.Share.Status != "revoked" {
		s.revokeLocked(r, "Sender account was deleted")
		if err := s.persist(r); err != nil {
			return err
		}
	}
	for _, run := range s.running {
		if run.shareID == r.Share.ID {
			return ErrBusy
		}
	}
	if err := s.fence(ctx, r.Share.ID); err != nil {
		return err
	}
	if err := s.volume(ctx); err != nil {
		return err
	}
	if !validID(r.Share.ID) {
		return ErrInvalid
	}
	if err := os.RemoveAll(filepath.Join(s.dir, r.Share.ID)); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM share_documents WHERE id = ?", r.Share.ID); err != nil {
		return err
	}
	delete(s.fenced, r.Share.ID)
	return nil
}
