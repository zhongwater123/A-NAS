package photos

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
)

// Each library can turn people and pets off
// (docs/architecture/photo-people.md#关闭与清除): the owner of a private
// library, an administrator for the shared one. Turning off stops face and
// pet scans for the photos only such libraries hold, and clears what was
// derived: the library's automatic assignments, its nameless people, and for
// those photos their faces, pets, vectors and avatar sheets. Names and what
// members decided are user data and stay: a face a member confirmed or
// rejected, or that belongs to a person a member named or hid, keeps its box
// but loses its vector, so that a scan after turning on again hands it to
// the face found in the same place. What clustering had decided comes back
// provisional, as after new models (photo-people.md#人物与用户数据).

// inPeopleLibrary is the condition that the object whose id is the SQL
// expression object has a photo, trashed or not, in a library with people on.
func inPeopleLibrary(object string) string {
	return "EXISTS (SELECT 1 FROM assets pa JOIN libraries pl ON pl.id = pa.library_id WHERE pa.object_id = " +
		object + " AND pl.people = 1)"
}

// canSetPeople: the owner of a private library; an administrator for the
// shared library, like its other AI switches. Viewing grants are read-only.
func canSetPeople(p Principal, lib library) bool {
	if !p.valid() {
		return false
	}
	if lib.kind == LibraryKindShared {
		return p.Admin
	}
	return lib.ownerUserID == p.UserID
}

func peopleOn(ctx context.Context, q queryer, libraryID string) (bool, error) {
	var on bool
	err := q.QueryRowContext(ctx, "SELECT people FROM libraries WHERE id = ?", libraryID).Scan(&on)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return on, err
}

// SetPeople turns people and pets on or off for a library, clearing what was
// derived when it goes off and scanning the library's photos again when it
// comes back on.
func (s *Service) SetPeople(ctx context.Context, p Principal, libraryID string, on bool) (Library, error) {
	var lib library
	var sheets [][2]string
	changed := false
	s.commitMu.Lock()
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var err error
		if lib, err = s.visibleLibrary(ctx, tx, p, libraryID); err != nil {
			return err
		}
		if !canSetPeople(p, lib) {
			return ErrForbidden
		}
		if was, err := peopleOn(ctx, tx, lib.id); err != nil || was == on {
			return err
		}
		changed = true
		if _, err := tx.ExecContext(ctx, "UPDATE libraries SET people = ? WHERE id = ?", on, lib.id); err != nil {
			return err
		}
		action := "library.people_off"
		if on {
			action = "library.people_on"
			err = s.enqueueLibraryFaces(ctx, tx, lib.id)
		} else {
			sheets, err = s.clearPeople(ctx, tx, lib.id)
		}
		if err != nil {
			return err
		}
		return s.audit(ctx, tx, p.UserID, action, lib.id, "")
	})
	if err == nil {
		for _, sheet := range sheets {
			if removeErr := s.root.Remove(derivedPath(sheet[1], sheet[0])); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
				// Reconcile removes a sheet without its row.
				err = removeErr
			}
		}
	}
	s.commitMu.Unlock()
	if err != nil {
		return Library{}, err
	}
	if changed && on {
		if err := s.enqueueMissingPets(ctx, s.objectCalibration.Model); err != nil {
			return Library{}, err
		}
		s.wakeAI()
	}
	public := publicLibrary(p, lib, s.now())
	public.People, public.CanSetPeople = on, true
	return public, nil
}

// enqueueLibraryFaces queues the face scan of every photo of a library that
// has its thumbnail and no scan.
func (s *Service) enqueueLibraryFaces(ctx context.Context, tx *sql.Tx, libraryID string) error {
	now := formatTime(s.now())
	_, err := tx.ExecContext(ctx, `
INSERT INTO jobs(object_id, derivation, state, not_before, created_at)
SELECT DISTINCT a.object_id, ?, 'pending', ?, ? FROM assets a
JOIN derived_files t ON t.object_id = a.object_id AND t.derivation = ?
WHERE a.library_id = ? AND NOT EXISTS (SELECT 1 FROM scans s WHERE s.object_id = a.object_id AND s.derivation = ?)
ON CONFLICT(object_id, derivation) DO NOTHING`,
		facesDerivation, now, now, thumbnailDerivation, libraryID, facesDerivation)
	return err
}

// clearPeople clears what was derived for a library that has just turned
// people off and returns the avatar sheets, as object and derivation, whose
// files are to go once it commits.
func (s *Service) clearPeople(ctx context.Context, tx *sql.Tx, libraryID string) ([][2]string, error) {
	for _, statement := range []string{
		`DELETE FROM person_occurrences WHERE library_id = ? AND source <> 'user'
  AND (person_id IS NULL OR person_id IN (SELECT id FROM people WHERE name = '' AND hidden = 0))`,
		"UPDATE person_occurrences SET source = 'moved' WHERE library_id = ? AND source = 'auto'",
	} {
		if _, err := tx.ExecContext(ctx, statement, libraryID); err != nil {
			return nil, err
		}
	}
	// The photos of the library that no library with people on still holds.
	gone := "SELECT a.object_id FROM assets a WHERE a.library_id = ? AND NOT " + inPeopleLibrary("a.object_id")
	rows, err := tx.QueryContext(ctx, "SELECT object_id, derivation FROM derived_files WHERE derivation IN (?, ?) AND object_id IN ("+gone+")",
		facesDerivation, petsDerivation, libraryID)
	if err != nil {
		return nil, err
	}
	var sheets [][2]string
	for rows.Next() {
		var sheet [2]string
		if err := rows.Scan(&sheet[0], &sheet[1]); err != nil {
			_ = rows.Close()
			return nil, err
		}
		sheets = append(sheets, sheet)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for _, statement := range []string{
		`UPDATE occurrences SET vector = X'', landmarks = NULL WHERE object_id IN (` + gone + `) AND (
  id IN (SELECT occurrence_id FROM person_occurrences) OR id IN (SELECT occurrence_id FROM occurrence_rejections))`,
		"DELETE FROM occurrences WHERE length(vector) > 0 AND object_id IN (" + gone + ")",
		"DELETE FROM scans WHERE object_id IN (" + gone + ")",
		"DELETE FROM jobs WHERE derivation IN ('" + facesDerivation + "', '" + petsDerivation + "') AND object_id IN (" + gone + ")",
		"DELETE FROM derived_files WHERE derivation IN ('" + facesDerivation + "', '" + petsDerivation + "') AND object_id IN (" + gone + ")",
		// Nameless people nobody confirmed or hid were only ever derived.
		`DELETE FROM people WHERE library_id = ? AND name = '' AND hidden = 0
  AND NOT EXISTS (SELECT 1 FROM person_occurrences po WHERE po.person_id = people.id)`,
	} {
		if _, err := tx.ExecContext(ctx, statement, libraryID); err != nil {
			return nil, err
		}
	}
	return sheets, nil
}

// enqueuePeopleScans queues the scans a photo newly in a library with people
// on is missing: a copy or an import of an original that so far only
// libraries with people off held.
func (s *Service) enqueuePeopleScans(ctx context.Context, tx *sql.Tx, objectID string) error {
	if err := s.enqueueFaces(ctx, tx, objectID); err != nil {
		return err
	}
	var model string
	var encoded []byte
	err := tx.QueryRowContext(ctx, `
SELECT d.model, d.boxes FROM detections d WHERE d.object_id = ? AND d.derivation = ?
  AND NOT EXISTS (SELECT 1 FROM scans p WHERE p.object_id = d.object_id AND p.derivation = ?)`,
		objectID, detectionDerivation, petsDerivation).Scan(&model, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	boxes, err := decodeBoxes(encoded)
	if err != nil || len(s.dogsIn(model, boxes)) == 0 {
		return nil
	}
	return s.enqueuePets(ctx, tx, objectID, model, boxes)
}
