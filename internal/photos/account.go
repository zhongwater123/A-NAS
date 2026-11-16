package photos

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// AccountRemoval is a deleted account's private library as the photo service
// removes it (docs/specs/account-enable-and-delete.md).
type AccountRemoval struct {
	UserID string
	// Transfer gives the library's photos to the administrator first, in a
	// new album of the administrator's private library named Album.
	Transfer      bool
	AdminID       string
	AdminUsername string
	Album         string
}

// RemoveAccount removes a deleted account's private library: its photos,
// albums, folders, people and trash, its corrections and the account's own
// photo audit, which only the account could read. When asked, its photos are
// first copied into the administrator's private library, sharing their
// originals, with the user tags and corrections they had; their people and
// folders are not. Originals no other photo uses are deleted.
//
// What the account added to the shared library stays there; with its
// uploader gone, an administrator manages it (Policy).
//
// It all happens in one transaction, so a retry finds either the library or
// nothing.
func (s *Service) RemoveAccount(ctx context.Context, removal AccountRemoval) error {
	if removal.UserID == "" || (removal.Transfer && (removal.AdminID == "" || removal.AdminID == removal.UserID)) {
		return errors.New("invalid account removal")
	}
	s.commitMu.Lock()
	defer s.commitMu.Unlock()
	var orphaned []string
	derived := make(map[string][]string)
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		lib, err := s.privateLibrary(ctx, tx, removal.UserID)
		if errors.Is(err, ErrNotFound) {
			return s.forgetAccount(ctx, tx, removal.UserID)
		}
		if err != nil {
			return err
		}
		if removal.Transfer {
			if err := s.handOverLibrary(ctx, tx, lib, removal); err != nil {
				return err
			}
		}
		objects, err := libraryObjects(ctx, tx, lib.id)
		if err != nil {
			return err
		}
		for _, statement := range []string{
			// Album members, tags, corrections and handles go with their photos.
			"DELETE FROM albums WHERE library_id = ?",
			"DELETE FROM assets WHERE library_id = ?",
			"DELETE FROM occurrence_rejections WHERE library_id = ?",
			"DELETE FROM person_occurrences WHERE library_id = ?",
			"DELETE FROM people WHERE library_id = ?",
			"DELETE FROM directories WHERE library_id = ?",
			"DELETE FROM text_changes WHERE library_id = ?",
			"DELETE FROM libraries WHERE id = ?",
		} {
			if _, err := tx.ExecContext(ctx, statement, lib.id); err != nil {
				return fmt.Errorf("remove the private library: %w", err)
			}
		}
		for _, objectID := range objects {
			var referenced bool
			if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM assets WHERE object_id = ? OR motion_object_id = ?)",
				objectID, objectID).Scan(&referenced); err != nil {
				return err
			}
			if referenced {
				continue
			}
			derivations, err := derivationsOf(ctx, tx, objectID)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM objects WHERE id = ?", objectID); err != nil {
				return err
			}
			orphaned = append(orphaned, objectID)
			derived[objectID] = derivations
		}
		return s.forgetAccount(ctx, tx, removal.UserID)
	})
	if err != nil {
		return err
	}
	s.detected.forget(orphaned)
	// A file left behind by a failure here has no Catalog row; Reconcile
	// removes it.
	var removeErr error
	for _, id := range orphaned {
		removeErr = errors.Join(removeErr, s.removeObject(id), s.removeDerived(id, derived[id]))
	}
	return removeErr
}

// forgetAccount removes what the catalog keeps of an account outside any
// library: the handles its assistant was given and its audit.
func (s *Service) forgetAccount(ctx context.Context, tx *sql.Tx, userID string) error {
	for _, statement := range []string{
		"DELETE FROM text_handles WHERE account = ?",
		"DELETE FROM audit_events WHERE actor_user_id = ?",
	} {
		if _, err := tx.ExecContext(ctx, statement, userID); err != nil {
			return err
		}
	}
	return nil
}

// handOverLibrary copies the photos of lib that are not in its trash into the
// administrator's private library and a new album there.
func (s *Service) handOverLibrary(ctx context.Context, tx *sql.Tx, lib library, removal AccountRemoval) error {
	records, err := s.queryAssets(ctx, tx, "WHERE a.library_id = ? AND a.trashed_at IS NULL ORDER BY IFNULL(a.taken_at, a.imported_at), a.id", lib.id)
	if err != nil || len(records) == 0 {
		return err
	}
	admin := Principal{UserID: removal.AdminID, Username: removal.AdminUsername, Admin: true}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO libraries(id, kind, owner_user_id, owner_name, created_at) VALUES(?, 'private', ?, ?, ?) ON CONFLICT DO NOTHING",
		s.randomID("library"), admin.UserID, admin.Username, formatTime(s.now())); err != nil {
		return err
	}
	target, err := s.privateLibrary(ctx, tx, admin.UserID)
	if err != nil {
		return err
	}
	name, ok := cleanName(removal.Album)
	if !ok {
		return ErrInvalidName
	}
	albumID := s.randomID("album")
	for n := 1; ; n++ {
		candidate := name
		if n > 1 {
			candidate = fmt.Sprintf("%s (%d)", name, n)
		}
		var taken bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM albums WHERE library_id = ? AND name = ?)", target.id, candidate).Scan(&taken); err != nil {
			return err
		}
		if !taken {
			name = candidate
			break
		}
	}
	now := formatTime(s.now())
	if _, err := tx.ExecContext(ctx, "INSERT INTO albums(id, library_id, name, created_by, created_at) VALUES(?, ?, ?, ?, ?)",
		albumID, target.id, name, admin.UserID, now); err != nil {
		return err
	}
	if err := s.audit(ctx, tx, admin.UserID, "photo.album_created", albumID, name); err != nil {
		return err
	}
	for _, record := range records {
		copied, err := s.insertCopy(ctx, tx, admin, record, target, "")
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO album_assets(album_id, asset_id, added_by, added_at) VALUES(?, ?, ?, ?)",
			albumID, copied.ID, admin.UserID, now); err != nil {
			return err
		}
	}
	return nil
}

// libraryObjects lists the originals and motion parts a library's photos use.
func libraryObjects(ctx context.Context, tx *sql.Tx, libraryID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT object_id FROM assets WHERE library_id = ?
UNION SELECT motion_object_id FROM assets WHERE library_id = ? AND motion_object_id IS NOT NULL`, libraryID, libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var objects []string
	for rows.Next() {
		var objectID string
		if err := rows.Scan(&objectID); err != nil {
			return nil, err
		}
		objects = append(objects, objectID)
	}
	return objects, rows.Err()
}
