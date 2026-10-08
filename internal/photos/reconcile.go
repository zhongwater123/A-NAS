package photos

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
)

// ReconcileReport describes what Reconcile repaired or could not repair.
type ReconcileReport struct {
	// StagingRemoved counts partial imports abandoned by a crash or restart.
	StagingRemoved int `json:"stagingRemoved"`
	// OrphanObjectsRemoved counts content objects that no photo asset
	// references, left by a crash between publishing and committing, or
	// between purging and unlinking.
	OrphanObjectsRemoved int `json:"orphanObjectsRemoved"`
	// MissingObjects counts referenced originals whose file is gone; their
	// assets cannot be opened until the original is restored from a backup.
	MissingObjects int `json:"missingObjects"`
	// DerivedRemoved counts derived files without a Catalog row.
	DerivedRemoved int `json:"derivedRemoved"`
	// DerivedRequeued counts derived results whose file was gone; they are
	// rebuilt by the media jobs.
	DerivedRequeued int `json:"derivedRequeued"`
	// UnexpectedEntries counts files Reconcile does not recognise and leaves
	// untouched.
	UnexpectedEntries int `json:"unexpectedEntries"`
}

// knownDerivations lists every derivation that has files under derived/.
var knownDerivations = []string{thumbnailDerivation}

// Reconcile brings the content store back in line with the Catalog after a
// crash. It never removes an object that any photo asset references, and it
// skips staging files that imports are still writing.
func (s *Service) Reconcile(ctx context.Context) (ReconcileReport, error) {
	s.commitMu.Lock()
	defer s.commitMu.Unlock()
	var report ReconcileReport

	stagingEntries, err := fs.ReadDir(s.root.FS(), stagingDir)
	if err != nil {
		return report, err
	}
	for _, entry := range stagingEntries {
		name := filepath.Join(stagingDir, entry.Name())
		s.stagingMu.Lock()
		_, writing := s.staging[name]
		s.stagingMu.Unlock()
		if writing {
			continue
		}
		if err := s.root.RemoveAll(name); err != nil {
			return report, err
		}
		report.StagingRemoved++
	}

	// Rows without assets cannot normally exist, because purge deletes the
	// row together with its last reference; treat any as orphans as well.
	if _, err := s.db.ExecContext(ctx, "DELETE FROM objects WHERE NOT EXISTS (SELECT 1 FROM assets WHERE assets.object_id = objects.id)"); err != nil {
		return report, err
	}
	referenced := make(map[string]bool)
	rows, err := s.db.QueryContext(ctx, "SELECT id FROM objects")
	if err != nil {
		return report, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return report, err
		}
		referenced[id] = false
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return report, err
	}

	err = fs.WalkDir(s.root.FS(), objectsDir, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		id := entry.Name()
		if !entry.Type().IsRegular() || !validObjectID(id) || name != filepath.ToSlash(objectPath(id)) {
			report.UnexpectedEntries++
			return nil
		}
		if _, ok := referenced[id]; ok {
			referenced[id] = true
			return nil
		}
		if err := s.removeObject(id); err != nil {
			return err
		}
		report.OrphanObjectsRemoved++
		return nil
	})
	if err != nil {
		return report, err
	}
	for _, present := range referenced {
		if !present {
			report.MissingObjects++
		}
	}
	if err := s.reconcileDerived(ctx, &report); err != nil {
		return report, err
	}
	return report, nil
}

func (s *Service) reconcileDerived(ctx context.Context, report *ReconcileReport) error {
	recorded := make(map[string]bool)
	rows, err := s.db.QueryContext(ctx, "SELECT object_id, derivation FROM derived_files")
	if err != nil {
		return err
	}
	for rows.Next() {
		var objectID, derivation string
		if err := rows.Scan(&objectID, &derivation); err != nil {
			_ = rows.Close()
			return err
		}
		recorded[derivedPath(derivation, objectID)] = false
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}

	err = fs.WalkDir(s.root.FS(), derivedDir, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		path := filepath.FromSlash(name)
		if _, ok := recorded[path]; ok {
			recorded[path] = true
			return nil
		}
		if !entry.Type().IsRegular() || !s.isDerivedPath(path) {
			report.UnexpectedEntries++
			return nil
		}
		if err := s.root.Remove(path); err != nil {
			return err
		}
		report.DerivedRemoved++
		return nil
	})
	if err != nil {
		return err
	}

	for _, derivation := range knownDerivations {
		rows, err := s.db.QueryContext(ctx, "SELECT object_id FROM derived_files WHERE derivation = ?", derivation)
		if err != nil {
			return err
		}
		var missing []string
		for rows.Next() {
			var objectID string
			if err := rows.Scan(&objectID); err != nil {
				_ = rows.Close()
				return err
			}
			if !recorded[derivedPath(derivation, objectID)] {
				missing = append(missing, objectID)
			}
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, objectID := range missing {
			err := s.withTx(ctx, func(tx *sql.Tx) error {
				if _, err := tx.ExecContext(ctx, "DELETE FROM derived_files WHERE object_id = ? AND derivation = ?", objectID, derivation); err != nil {
					return err
				}
				return s.enqueueDerivations(ctx, tx, objectID)
			})
			if err != nil {
				return err
			}
			report.DerivedRequeued++
		}
	}
	if report.DerivedRequeued > 0 {
		s.wakeMedia()
	}
	return nil
}

// isDerivedPath reports whether path is where a known derivation of a valid
// object would be stored.
func (s *Service) isDerivedPath(path string) bool {
	objectID := strings.TrimSuffix(filepath.Base(path), ".jpg")
	if !validObjectID(objectID) {
		return false
	}
	for _, derivation := range knownDerivations {
		if path == derivedPath(derivation, objectID) {
			return true
		}
	}
	return false
}

func validObjectID(id string) bool {
	if len(id) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(id)
	return err == nil && hex.EncodeToString(decoded) == id
}
