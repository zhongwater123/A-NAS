package photos

import (
	"context"
	"encoding/hex"
	"errors"
	"io/fs"
	"path/filepath"
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
	// UnexpectedEntries counts files Reconcile does not recognise and leaves
	// untouched.
	UnexpectedEntries int `json:"unexpectedEntries"`
}

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
	return report, nil
}

func validObjectID(id string) bool {
	if len(id) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(id)
	return err == nil && hex.EncodeToString(decoded) == id
}
