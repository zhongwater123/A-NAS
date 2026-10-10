package photos

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
)

var ErrInvalidCursor = errors.New("invalid photo timeline cursor")

type ImportRequest struct {
	LibraryID string
	// DirectoryID is the target virtual directory; empty means the library root.
	DirectoryID string
	Name        string
	Content     io.Reader
}

// Import persists one original and creates its photo asset. It returns only
// after the original and the Catalog row are durable; nothing here waits for
// thumbnails or AI.
func (s *Service) Import(ctx context.Context, p Principal, request ImportRequest) (Asset, error) {
	name, ok := cleanName(request.Name)
	if !ok {
		return Asset{}, ErrInvalidName
	}
	lib, err := s.visibleLibrary(ctx, s.db, p, request.LibraryID)
	if err != nil {
		return Asset{}, err
	}
	if !canAdd(p, lib) {
		return Asset{}, ErrForbidden
	}
	if err := s.checkDirectory(ctx, s.db, lib, request.DirectoryID); err != nil {
		return Asset{}, err
	}
	staged, err := s.stage(request.Content)
	if err != nil {
		return Asset{}, err
	}
	defer s.discardStaging(staged.name)

	s.commitMu.Lock()
	defer s.commitMu.Unlock()
	created, err := s.publish(staged)
	if err != nil {
		return Asset{}, err
	}
	assetID := s.randomID("photo")
	var imported assetRecord
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		// The directory may have been deleted while the original streamed in.
		if err := s.checkDirectory(ctx, tx, lib, request.DirectoryID); err != nil {
			return err
		}
		now := formatTime(s.now())
		metadata := staged.metadata
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO objects(id, size_bytes, media_type, width, height, orientation, created_at)
			 VALUES(?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`,
			staged.id, staged.size, staged.mediaType, metadata.width, metadata.height, metadata.orientation, now); err != nil {
			return err
		}
		var takenAt any
		if !metadata.takenAt.IsZero() {
			takenAt = formatTime(metadata.takenAt)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO assets(id, library_id, directory_id, object_id, name, uploaded_by, imported_at, taken_at)
			 VALUES(?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?)`,
			assetID, lib.id, request.DirectoryID, staged.id, name, p.UserID, now, takenAt); err != nil {
			return err
		}
		if err := s.enqueueDerivations(ctx, tx, staged.id); err != nil {
			return err
		}
		record, err := s.assetByID(ctx, tx, assetID)
		if err != nil {
			return err
		}
		imported = record
		return s.audit(ctx, tx, p.UserID, "photo.imported", assetID, name)
	})
	if err != nil {
		if created {
			_ = s.removeObject(staged.id)
		}
		return Asset{}, err
	}
	s.wakeMedia()
	hinted := []assetRecord{imported}
	if err := s.addDuplicateHints(ctx, s.db, p, hinted); err != nil {
		return Asset{}, err
	}
	return hinted[0].Asset, nil
}

// Get returns an asset p may see. Trashed assets are visible only to those
// who may restore them.
func (s *Service) Get(ctx context.Context, p Principal, assetID string) (Asset, error) {
	record, err := s.visibleAsset(ctx, s.db, p, assetID)
	if err != nil {
		return Asset{}, err
	}
	return s.details(ctx, p, record)
}

// details completes an asset with its duplicate hints, user metadata and AI
// labels, which lists leave out.
func (s *Service) details(ctx context.Context, p Principal, record assetRecord) (Asset, error) {
	hinted := []assetRecord{record}
	if err := s.addDuplicateHints(ctx, s.db, p, hinted); err != nil {
		return Asset{}, err
	}
	asset := hinted[0].Asset
	var err error
	if asset.Tags, err = s.tagsOf(ctx, asset.ID); err != nil {
		return Asset{}, err
	}
	if asset.Albums, err = s.albumsOf(ctx, asset.ID); err != nil {
		return Asset{}, err
	}
	hidden, err := s.hiddenLabels(ctx, asset.ID)
	if err != nil {
		return Asset{}, err
	}
	if asset.AILabels, err = s.labelsOf(ctx, record.objectID, hidden); err != nil {
		return Asset{}, err
	}
	return asset, nil
}

func (s *Service) visibleAsset(ctx context.Context, q queryer, p Principal, assetID string) (assetRecord, error) {
	if !p.valid() {
		return assetRecord{}, ErrForbidden
	}
	record, err := s.assetByID(ctx, q, assetID)
	if err != nil {
		return assetRecord{}, err
	}
	if !canView(p, record.library, s.now()) {
		return assetRecord{}, ErrNotFound
	}
	if record.Trash != nil && !canChange(p, record.library, record.UploadedBy) {
		return assetRecord{}, ErrNotFound
	}
	return record, nil
}

// Open returns the original read-only. The caller must close Content.Reader.
func (s *Service) Open(ctx context.Context, p Principal, assetID string) (Content, error) {
	record, err := s.visibleAsset(ctx, s.db, p, assetID)
	if err != nil {
		return Content{}, err
	}
	file, err := s.openObject(record.objectID)
	if errors.Is(err, fs.ErrNotExist) {
		return Content{}, fmt.Errorf("original of %s is missing from the content store", record.ID)
	}
	if err != nil {
		return Content{}, err
	}
	return Content{
		Name: record.Name, MediaType: record.MediaType, SizeBytes: record.SizeBytes,
		ETag: `"` + record.objectID + `"`, Reader: file,
	}, nil
}

type Page struct {
	Assets []Asset `json:"assets"`
	// Next continues the listing; empty on the last page.
	Next string `json:"next,omitempty"`
}

const (
	defaultPageSize = 100
	maxPageSize     = 500
)

// Timeline lists the available assets of one library, newest first by
// capture time, or by import time for originals without one.
func (s *Service) Timeline(ctx context.Context, p Principal, libraryID, cursor string, limit int) (Page, error) {
	lib, err := s.visibleLibrary(ctx, s.db, p, libraryID)
	if err != nil {
		return Page{}, err
	}
	return s.newestFirst(ctx, p, "WHERE a.library_id = ? AND a.trashed_at IS NULL", []any{lib.id}, cursor, limit)
}

// newestFirst pages through the assets that where selects, newest capture
// time first, or import time for originals without one.
func (s *Service) newestFirst(ctx context.Context, p Principal, where string, args []any, cursor string, limit int) (Page, error) {
	limit = pageSize(limit)
	const sortKey = "IFNULL(a.taken_at, a.imported_at)"
	if cursor != "" {
		at, id, err := decodeCursor(cursor)
		if err != nil {
			return Page{}, err
		}
		if _, err := parseTime(at); err != nil {
			return Page{}, ErrInvalidCursor
		}
		where += " AND (" + sortKey + " < ? OR (" + sortKey + " = ? AND a.id < ?))"
		args = append(args, at, at, id)
	}
	records, err := s.queryAssets(ctx, s.db, where+" ORDER BY "+sortKey+" DESC, a.id DESC LIMIT ?", append(args, limit+1)...)
	if err != nil {
		return Page{}, err
	}
	if err := s.addDuplicateHints(ctx, s.db, p, records); err != nil {
		return Page{}, err
	}
	page := Page{Assets: []Asset{}}
	for i, record := range records {
		if i == limit {
			last := page.Assets[limit-1]
			at := last.ImportedAt
			if last.TakenAt != nil {
				at = *last.TakenAt
			}
			page.Next = encodeCursor(formatTime(at), last.ID)
			break
		}
		page.Assets = append(page.Assets, record.Asset)
	}
	return page, nil
}

func pageSize(limit int) int {
	if limit <= 0 {
		return defaultPageSize
	}
	return min(limit, maxPageSize)
}

// A cursor holds the sort key and ID of the last item on the previous page.
// Neither names nor times contain a newline.
func encodeCursor(key, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(key + "\n" + id))
}

func decodeCursor(cursor string) (string, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", "", ErrInvalidCursor
	}
	key, id, found := strings.Cut(string(raw), "\n")
	if !found || key == "" || id == "" {
		return "", "", ErrInvalidCursor
	}
	return key, id, nil
}

// AssetUpdate renames and/or moves an asset; nil fields stay unchanged. A
// DirectoryID of "" is the library root.
type AssetUpdate struct {
	Name        *string
	DirectoryID *string
}

// Update applies an AssetUpdate in one transaction, so a rejected half
// leaves the asset unchanged. The asset keeps its ID and original.
func (s *Service) Update(ctx context.Context, p Principal, assetID string, update AssetUpdate) (Asset, error) {
	var name string
	if update.Name != nil {
		var ok bool
		if name, ok = cleanName(*update.Name); !ok {
			return Asset{}, ErrInvalidName
		}
	}
	return s.changeAsset(ctx, p, assetID, func(tx *sql.Tx, record assetRecord) error {
		if record.Trash != nil {
			return ErrConflict
		}
		if update.DirectoryID != nil {
			directoryID := *update.DirectoryID
			if err := s.checkDirectory(ctx, tx, record.library, directoryID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE assets SET directory_id = NULLIF(?, '') WHERE id = ?", directoryID, record.ID); err != nil {
				return err
			}
			if err := s.audit(ctx, tx, p.UserID, "photo.moved", record.ID, directoryID); err != nil {
				return err
			}
		}
		if update.Name != nil {
			if _, err := tx.ExecContext(ctx, "UPDATE assets SET name = ? WHERE id = ?", name, record.ID); err != nil {
				return err
			}
			return s.audit(ctx, tx, p.UserID, "photo.renamed", record.ID, name)
		}
		return nil
	})
}

// Trash moves an asset to its library's trash, out of browsing, for the
// retention period.
func (s *Service) Trash(ctx context.Context, p Principal, assetID string) (Asset, error) {
	return s.changeAsset(ctx, p, assetID, func(tx *sql.Tx, record assetRecord) error {
		if record.Trash != nil {
			return ErrConflict
		}
		now := s.now()
		if _, err := tx.ExecContext(ctx, "UPDATE assets SET trashed_at = ?, trashed_by = ?, purge_after = ? WHERE id = ?",
			formatTime(now), p.UserID, formatTime(now.Add(s.trashRetention)), record.ID); err != nil {
			return err
		}
		return s.audit(ctx, tx, p.UserID, "photo.trashed", record.ID, record.Name)
	})
}

// Restore returns a trashed asset to its virtual directory, or to the library
// root if that directory no longer exists.
func (s *Service) Restore(ctx context.Context, p Principal, assetID string) (Asset, error) {
	return s.changeAsset(ctx, p, assetID, func(tx *sql.Tx, record assetRecord) error {
		if record.Trash == nil {
			return ErrConflict
		}
		if _, err := tx.ExecContext(ctx, "UPDATE assets SET trashed_at = NULL, trashed_by = NULL, purge_after = NULL WHERE id = ?", record.ID); err != nil {
			return err
		}
		return s.audit(ctx, tx, p.UserID, "photo.restored", record.ID, record.Name)
	})
}

// changeAsset runs change, which also writes its audit events, on an asset p
// may change.
func (s *Service) changeAsset(ctx context.Context, p Principal, assetID string, change func(*sql.Tx, assetRecord) error) (Asset, error) {
	var changed Asset
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		record, err := s.visibleAsset(ctx, tx, p, assetID)
		if err != nil {
			return err
		}
		if !canChange(p, record.library, record.UploadedBy) {
			return ErrForbidden
		}
		if err := change(tx, record); err != nil {
			return err
		}
		updated, err := s.assetByID(ctx, tx, assetID)
		if err != nil {
			return err
		}
		changed = updated.Asset
		return nil
	})
	return changed, err
}

// Copy creates an independent asset in targetLibraryID that reuses the
// source's original bytes. Changing or deleting either asset never affects the
// other.
func (s *Service) Copy(ctx context.Context, p Principal, assetID, targetLibraryID, targetDirectoryID string) (Asset, error) {
	var copied Asset
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		source, err := s.visibleAsset(ctx, tx, p, assetID)
		if err != nil {
			return err
		}
		if err := checkCopySource(p, source); err != nil {
			return err
		}
		target, err := s.visibleLibrary(ctx, tx, p, targetLibraryID)
		if err != nil {
			return err
		}
		if !canAdd(p, target) {
			return ErrForbidden
		}
		if err := s.checkDirectory(ctx, tx, target, targetDirectoryID); err != nil {
			return err
		}
		record, err := s.insertCopy(ctx, tx, p, source, target, targetDirectoryID)
		copied = record.Asset
		return err
	})
	return copied, err
}

// checkCopySource refuses copies that would outlive the caller's access or
// revive a trashed photo.
func checkCopySource(p Principal, source assetRecord) error {
	// Administrative Viewing Mode is read-only: copying would keep a
	// member's private photo after the grant ends.
	if viewingOnly(p, source.library) {
		return ErrForbidden
	}
	if source.Trash != nil {
		return ErrConflict
	}
	return nil
}

// insertCopy creates the copy, which starts with the source's user tags and
// AI corrections and keeps them apart from then on.
func (s *Service) insertCopy(ctx context.Context, tx *sql.Tx, p Principal, source assetRecord, target library, directoryID string) (assetRecord, error) {
	id := s.randomID("photo")
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO assets(id, library_id, directory_id, object_id, name, uploaded_by, imported_at, taken_at)
		 SELECT ?, ?, NULLIF(?, ''), object_id, name, ?, ?, taken_at FROM assets WHERE id = ?`,
		id, target.id, directoryID, p.UserID, formatTime(s.now()), source.ID); err != nil {
		return assetRecord{}, err
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO user_tags(asset_id, name, created_by, created_at) SELECT ?, name, created_by, created_at FROM user_tags WHERE asset_id = ?",
		id, source.ID); err != nil {
		return assetRecord{}, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO ai_tag_corrections(asset_id, label_id, verdict, created_by, created_at)
		 SELECT ?, label_id, verdict, created_by, created_at FROM ai_tag_corrections WHERE asset_id = ?`,
		id, source.ID); err != nil {
		return assetRecord{}, err
	}
	record, err := s.assetByID(ctx, tx, id)
	if err != nil {
		return assetRecord{}, err
	}
	return record, s.audit(ctx, tx, p.UserID, "photo.copied", id, source.ID)
}

// ListTrash returns the trashed assets of a library that p may restore or
// purge, most recently trashed first.
func (s *Service) ListTrash(ctx context.Context, p Principal, libraryID string) ([]Asset, error) {
	records, err := s.restorableTrash(ctx, p, libraryID)
	if err != nil {
		return nil, err
	}
	assets := make([]Asset, 0, len(records))
	for _, record := range records {
		assets = append(assets, record.Asset)
	}
	return assets, nil
}

func (s *Service) restorableTrash(ctx context.Context, p Principal, libraryID string) ([]assetRecord, error) {
	lib, err := s.visibleLibrary(ctx, s.db, p, libraryID)
	if err != nil {
		return nil, err
	}
	records, err := s.queryAssets(ctx, s.db,
		"WHERE a.library_id = ? AND a.trashed_at IS NOT NULL ORDER BY a.trashed_at DESC, a.id DESC", lib.id)
	if err != nil {
		return nil, err
	}
	restorable := records[:0]
	for _, record := range records {
		if canChange(p, lib, record.UploadedBy) {
			restorable = append(restorable, record)
		}
	}
	return restorable, nil
}

// Purge permanently deletes one trashed asset.
func (s *Service) Purge(ctx context.Context, p Principal, assetID string) error {
	s.commitMu.Lock()
	defer s.commitMu.Unlock()
	record, err := s.visibleAsset(ctx, s.db, p, assetID)
	if err != nil {
		return err
	}
	if !canChange(p, record.library, record.UploadedBy) {
		return ErrForbidden
	}
	if record.Trash == nil {
		return ErrConflict
	}
	_, err = s.purge(ctx, p.UserID, "photo.purged", []assetRecord{record})
	return err
}

// EmptyTrash permanently deletes every trashed asset of a library that p may
// purge. An administrator can never empty a member's private trash.
func (s *Service) EmptyTrash(ctx context.Context, p Principal, libraryID string) (int, error) {
	s.commitMu.Lock()
	defer s.commitMu.Unlock()
	records, err := s.restorableTrash(ctx, p, libraryID)
	if err != nil {
		return 0, err
	}
	return s.purge(ctx, p.UserID, "photo.purged", records)
}

// ExpireTrash permanently deletes assets whose retention has elapsed. It runs
// as the photo service itself and needs no user session.
func (s *Service) ExpireTrash(ctx context.Context) (int, error) {
	s.commitMu.Lock()
	defer s.commitMu.Unlock()
	records, err := s.queryAssets(ctx, s.db, "WHERE a.purge_after <= ? ORDER BY a.purge_after", formatTime(s.now()))
	if err != nil {
		return 0, err
	}
	return s.purge(ctx, "system", "photo.purged.retention", records)
}

// purge deletes trashed assets and every content object they were the last
// reference to. The caller holds commitMu, so no import can decide to reuse
// an object between its last reference disappearing and its file being
// unlinked.
func (s *Service) purge(ctx context.Context, actorID, action string, records []assetRecord) (int, error) {
	var purged int
	var orphaned []string
	derived := make(map[string][]string)
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		for _, record := range records {
			result, err := tx.ExecContext(ctx, "DELETE FROM assets WHERE id = ? AND trashed_at IS NOT NULL", record.ID)
			if err != nil {
				return err
			}
			deleted, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if deleted == 0 {
				// Restored since it was listed.
				continue
			}
			purged++
			var referenced bool
			if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM assets WHERE object_id = ?)", record.objectID).Scan(&referenced); err != nil {
				return err
			}
			if !referenced {
				derivations, err := derivationsOf(ctx, tx, record.objectID)
				if err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, "DELETE FROM objects WHERE id = ?", record.objectID); err != nil {
					return err
				}
				orphaned = append(orphaned, record.objectID)
				derived[record.objectID] = derivations
			}
			if err := s.audit(ctx, tx, actorID, action, record.ID, record.Name); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	s.index.forget(orphaned)
	// A file left behind by a failure here has no Catalog row; Reconcile
	// removes it.
	var removeErr error
	for _, id := range orphaned {
		removeErr = errors.Join(removeErr, s.removeObject(id), s.removeDerived(id, derived[id]))
	}
	return purged, removeErr
}

func derivationsOf(ctx context.Context, q queryer, objectID string) ([]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT derivation FROM derived_files WHERE object_id = ?", objectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var derivations []string
	for rows.Next() {
		var derivation string
		if err := rows.Scan(&derivation); err != nil {
			return nil, err
		}
		derivations = append(derivations, derivation)
	}
	return derivations, rows.Err()
}
