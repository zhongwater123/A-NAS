package photos

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"time"
)

var ErrInvalidCursor = errors.New("invalid photo timeline cursor")

type ImportRequest struct {
	LibraryID string
	// DirectoryID is the target virtual directory; empty means the library root.
	DirectoryID   string
	Name          string
	Content       io.Reader
	MotionContent io.Reader
	MotionName    string
}

// Import persists one original, a photo or a video, and creates its photo
// asset. It returns only after the original and the Catalog row are
// durable; nothing here waits for thumbnails or AI.
func (s *Service) Import(ctx context.Context, p Principal, request ImportRequest) (Asset, error) {
	name, lib, err := s.importTarget(ctx, p, request)
	if err != nil {
		return Asset{}, err
	}
	staged, err := s.stageOriginal(ctx, request.Content)
	if err != nil {
		return Asset{}, err
	}
	defer s.discardStaging(staged.name)
	return s.importStaged(ctx, p, lib, name, request, staged)
}

// UploadResult is what an original uploaded on its own became: a new photo
// asset or, for a Live Photo's MOV, the photos it completed.
type UploadResult struct {
	Asset     *Asset
	Completed []Asset
}

// Upload imports an original uploaded on its own. A Live Photo's MOV whose
// stills in the library are still waiting for it completes them, as
// AttachLiveVideo does; without such stills it is a video like any other
// (docs/specs/photo-video.md).
func (s *Service) Upload(ctx context.Context, p Principal, request ImportRequest) (UploadResult, error) {
	name, lib, err := s.importTarget(ctx, p, request)
	if err != nil {
		return UploadResult{}, err
	}
	staged, err := s.stageOriginal(ctx, request.Content)
	if err != nil {
		return UploadResult{}, err
	}
	defer s.discardStaging(staged.name)
	if video := staged.video; video != nil && video.liveID != "" && video.clip && strings.EqualFold(filepath.Ext(name), ".mov") {
		completed, err := s.completeLivePhotos(ctx, p, lib, name, staged, video.liveID, video.duration)
		if !errors.Is(err, ErrNoLiveStill) {
			return UploadResult{Completed: completed}, err
		}
	}
	asset, err := s.importStaged(ctx, p, lib, name, request, staged)
	if err != nil {
		return UploadResult{}, err
	}
	return UploadResult{Asset: &asset}, nil
}

// importTarget checks an import's name and that p may add to its library
// and directory.
func (s *Service) importTarget(ctx context.Context, p Principal, request ImportRequest) (string, library, error) {
	name, ok := cleanName(request.Name)
	if !ok {
		return "", library{}, ErrInvalidName
	}
	lib, err := s.visibleLibrary(ctx, s.db, p, request.LibraryID)
	if err != nil {
		return "", library{}, err
	}
	if !canAdd(p, lib) {
		return "", library{}, ErrForbidden
	}
	if err := s.checkDirectory(ctx, s.db, lib, request.DirectoryID); err != nil {
		return "", library{}, err
	}
	return name, lib, nil
}

// stageOriginal stages an original and reads a video through the Media
// Worker. The caller discards the staging file.
func (s *Service) stageOriginal(ctx context.Context, content io.Reader) (stagedObject, error) {
	staged, err := s.stage(content)
	if err != nil {
		return stagedObject{}, err
	}
	if isVideoType(staged.mediaType) {
		if err := s.readVideo(ctx, &staged); err != nil {
			s.discardStaging(staged.name)
			return stagedObject{}, err
		}
	}
	return staged, nil
}

// importStaged publishes a staged original, with a Live Photo's or motion
// photo's video for a still, and creates its asset.
func (s *Service) importStaged(ctx context.Context, p Principal, lib library, name string, request ImportRequest, staged stagedObject) (Asset, error) {
	var companion *motionImport
	liveID := ""
	if staged.video != nil {
		// Only a still has a Live Photo's or motion photo's video.
		if request.MotionContent != nil {
			return Asset{}, ErrInvalidMotion
		}
	} else {
		var err error
		if companion, err = s.prepareMotion(ctx, staged, request); err != nil {
			return Asset{}, err
		}
		if companion != nil {
			defer s.discardStaging(companion.object.name)
		}
		if liveID, err = s.stillLiveID(staged.name); err != nil {
			return Asset{}, err
		}
	}

	s.commitMu.Lock()
	defer s.commitMu.Unlock()
	created, err := s.publish(staged)
	if err != nil {
		return Asset{}, err
	}
	motionCreated := false
	if companion != nil {
		motionCreated, err = s.publish(companion.object)
		if err != nil {
			if created {
				_ = s.removeObject(staged.id)
			}
			return Asset{}, err
		}
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
		// A video that so far was only a Live Photo's has no size yet.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO objects(id, size_bytes, media_type, width, height, orientation, created_at, live_id)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET live_id = IFNULL(objects.live_id, excluded.live_id),
			 width = IIF(objects.width = 0, excluded.width, objects.width), height = IIF(objects.height = 0, excluded.height, objects.height)`,
			staged.id, staged.size, staged.mediaType, metadata.width, metadata.height, metadata.orientation, now, liveID); err != nil {
			return err
		}
		if staged.video != nil {
			if err := s.insertVideo(ctx, tx, staged.id, staged.video); err != nil {
				return err
			}
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
		if companion != nil {
			if err := s.insertMotion(ctx, tx, assetID, companion); err != nil {
				return err
			}
		}
		if err := s.enqueueDerivations(ctx, tx, staged.id); err != nil {
			return err
		}
		if err := s.enqueuePeopleScans(ctx, tx, staged.id); err != nil {
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
		if motionCreated {
			_ = s.removeObject(companion.object.id)
		}
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

// details completes an asset with its duplicate hints and user metadata,
// which lists leave out.
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
	if asset.People, err = s.peopleOf(ctx, record); err != nil {
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
	// Videos take no part in AI, and so none in the assistant's reads.
	if p.Assistant && record.Video != nil {
		return assetRecord{}, ErrNotFound
	}
	if record.Trash != nil && !canChange(p, record.library, record.UploadedBy) {
		return assetRecord{}, ErrNotFound
	}
	return record, nil
}

// Open returns the original read-only. The caller must close Content.Reader.
// An assistant gets no pixels: the model sees names, times and tags only.
func (s *Service) Open(ctx context.Context, p Principal, assetID string) (Content, error) {
	if p.Assistant {
		return Content{}, ErrForbidden
	}
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

var ErrInvalidMonth = errors.New("invalid photo timeline month")

// monthLayout names a calendar month of the device's time zone, the zone
// capture times without an offset are read in.
const monthLayout = "2006-01"

// TimelineMonth counts a library's available assets in one month, by capture
// time or by import time for originals without one.
type TimelineMonth struct {
	// Month is YYYY-MM in the device's time zone.
	Month  string `json:"month"`
	Photos int    `json:"photos"`
}

// TimelineMonths counts a library's available assets by month, newest first,
// so a timeline can lay out and jump to months it has not loaded yet.
func (s *Service) TimelineMonths(ctx context.Context, p Principal, libraryID string) ([]TimelineMonth, error) {
	lib, err := s.visibleLibrary(ctx, s.db, p, libraryID)
	if err != nil {
		return nil, err
	}
	keys, err := s.strings(ctx, `SELECT IFNULL(taken_at, imported_at) FROM assets
WHERE library_id = ? AND trashed_at IS NULL`+hiddenVideos(p, "assets.object_id")+` ORDER BY IFNULL(taken_at, imported_at) DESC`, lib.id)
	if err != nil {
		return nil, err
	}
	months := []TimelineMonth{}
	for _, key := range keys {
		at, err := parseTime(key)
		if err != nil {
			return nil, err
		}
		// Local months follow the UTC order, so equal months are adjacent.
		month := at.In(s.location).Format(monthLayout)
		if last := len(months) - 1; last >= 0 && months[last].Month == month {
			months[last].Photos++
		} else {
			months = append(months, TimelineMonth{Month: month, Photos: 1})
		}
	}
	return months, nil
}

// TimelineInMonth pages through one month (YYYY-MM) of a library's timeline
// the way Timeline does.
func (s *Service) TimelineInMonth(ctx context.Context, p Principal, libraryID, month, cursor string, limit int) (Page, error) {
	start, err := time.ParseInLocation(monthLayout, month, s.location)
	if err != nil {
		return Page{}, ErrInvalidMonth
	}
	lib, err := s.visibleLibrary(ctx, s.db, p, libraryID)
	if err != nil {
		return Page{}, err
	}
	const sortKey = "IFNULL(a.taken_at, a.imported_at)"
	return s.newestFirst(ctx, p, "WHERE a.library_id = ? AND a.trashed_at IS NULL AND "+sortKey+" >= ? AND "+sortKey+" < ?",
		[]any{lib.id, formatTime(start), formatTime(start.AddDate(0, 1, 0))}, cursor, limit)
}

// newestFirst pages through the assets that where selects, newest capture
// time first, or import time for originals without one.
func (s *Service) newestFirst(ctx context.Context, p Principal, where string, args []any, cursor string, limit int) (Page, error) {
	limit = pageSize(limit)
	const sortKey = "IFNULL(a.taken_at, a.imported_at)"
	where += hiddenVideos(p, "a.object_id")
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
		`INSERT INTO assets(id, library_id, directory_id, object_id, name, uploaded_by, imported_at, taken_at, motion_object_id, motion_name, motion_kind, motion_duration)
		 SELECT ?, ?, NULLIF(?, ''), object_id, name, ?, ?, taken_at, motion_object_id, motion_name, motion_kind, motion_duration FROM assets WHERE id = ?`,
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
	if err := s.enqueuePeopleScans(ctx, tx, record.objectID); err != nil {
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
// purge. An administrator can never empty a member's private trash, and an
// assistant can never empty any.
func (s *Service) EmptyTrash(ctx context.Context, p Principal, libraryID string) (int, error) {
	if p.Assistant {
		return 0, ErrForbidden
	}
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
			for _, objectID := range []string{record.objectID, record.motionObjectID} {
				if objectID == "" {
					continue
				}
				var referenced bool
				if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM assets WHERE object_id = ? OR motion_object_id = ?)", objectID, objectID).Scan(&referenced); err != nil {
					return err
				}
				if !referenced {
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
	s.detected.forget(orphaned)
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
