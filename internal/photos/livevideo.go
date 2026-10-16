package photos

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/mediaworker"
)

// A Live Photo's still and MOV are meant to travel together, but a Mac often
// hands a browser the still alone: dragging out of Photos or choosing in the
// file dialog's Photos section exports only the image. The still keeps
// Apple's content identifier, so the import records it, the asset says its
// video is missing, and a MOV uploaded on its own later completes the stills
// in the library that carry the same identifier
// (docs/specs/photo-motion.md#补传视频). Matching is exact; the name only
// narrows which older stills, imported before identifiers were recorded, are
// read again.

// ErrNoLiveStill means a Live Photo video matched no photo in the library
// that is still waiting for it.
var ErrNoLiveStill = errors.New("no photo in this library is waiting for this Live Photo video")

const appleIdentifierTag = "com.apple.quicktime.content.identifier"

// LiveVideoRequest is a Live Photo's MOV uploaded without its still.
type LiveVideoRequest struct {
	LibraryID string
	Name      string
	Content   io.Reader
}

// stillLiveID reads a staged still's Apple Live Photo identifier; "" means
// it has none.
func (s *Service) stillLiveID(name string) (string, error) {
	file, err := s.root.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	return appleContentID(file), nil
}

// AttachLiveVideo completes the Live Photos of a library whose still was
// imported without this MOV, and returns them. The MOV must carry Apple's
// identifier and match the still's exactly; only photos the caller may
// change are completed.
func (s *Service) AttachLiveVideo(ctx context.Context, p Principal, request LiveVideoRequest) ([]Asset, error) {
	name, ok := cleanName(request.Name)
	if !ok || !strings.EqualFold(filepath.Ext(name), ".mov") {
		return nil, ErrInvalidMotion
	}
	lib, err := s.visibleLibrary(ctx, s.db, p, request.LibraryID)
	if err != nil {
		return nil, err
	}
	if !canAdd(p, lib) {
		return nil, ErrForbidden
	}
	if s.media == nil {
		return nil, mediaworker.ErrUnavailable
	}
	clip, err := s.stageContent(request.Content, true)
	if err != nil {
		return nil, err
	}
	defer s.discardStaging(clip.name)
	probe, err := s.probeMotion(ctx, clip)
	if err != nil {
		return nil, err
	}
	liveID := probe.Format.Tags[appleIdentifierTag]
	if liveID == "" {
		// Without an identifier there is nothing to match; such a MOV can
		// only come with its still.
		return nil, ErrInvalidMotion
	}
	if err := s.readLiveIDs(ctx, lib.id, strings.TrimSuffix(name, filepath.Ext(name))); err != nil {
		return nil, err
	}
	stills, err := s.liveStills(ctx, p, lib, liveID)
	if err != nil {
		return nil, err
	}
	if len(stills) == 0 {
		return nil, ErrNoLiveStill
	}

	s.commitMu.Lock()
	defer s.commitMu.Unlock()
	created, err := s.publish(clip)
	if err != nil {
		return nil, err
	}
	var completed []assetRecord
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		for _, id := range stills {
			// Another upload may have completed or trashed it meanwhile.
			var still string
			err := tx.QueryRowContext(ctx, "SELECT name FROM assets WHERE id = ? AND motion_object_id IS NULL AND trashed_at IS NULL", id).Scan(&still)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			// The video takes the still's name, as a paired upload does.
			video := &motionImport{object: clip, name: strings.TrimSuffix(still, filepath.Ext(still)) + filepath.Ext(name),
				kind: "live-photo", duration: probe.Format.Duration}
			if err := s.insertMotion(ctx, tx, id, video); err != nil {
				return err
			}
			record, err := s.assetByID(ctx, tx, id)
			if err != nil {
				return err
			}
			completed = append(completed, record)
			if err := s.audit(ctx, tx, p.UserID, "photo.motion_attached", id, record.Name); err != nil {
				return err
			}
		}
		if len(completed) == 0 {
			return ErrNoLiveStill
		}
		return nil
	})
	if err != nil {
		if created {
			_ = s.removeObject(clip.id)
		}
		return nil, err
	}
	if err := s.addDuplicateHints(ctx, s.db, p, completed); err != nil {
		return nil, err
	}
	assets := make([]Asset, len(completed))
	for i, record := range completed {
		assets[i] = record.Asset
	}
	return assets, nil
}

// readLiveIDs records the identifiers of a library's stills named like the
// video that were imported before identifiers were kept.
func (s *Service) readLiveIDs(ctx context.Context, libraryID, stem string) error {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT o.id, a.name FROM assets a JOIN objects o ON o.id = a.object_id
WHERE a.library_id = ? AND a.trashed_at IS NULL AND a.motion_object_id IS NULL AND o.live_id IS NULL
  AND o.media_type IN ('image/jpeg', 'image/heic')`, libraryID)
	if err != nil {
		return err
	}
	var unread []string
	for rows.Next() {
		var objectID, name string
		if err := rows.Scan(&objectID, &name); err != nil {
			_ = rows.Close()
			return err
		}
		if strings.EqualFold(strings.TrimSuffix(name, filepath.Ext(name)), stem) {
			unread = append(unread, objectID)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, objectID := range unread {
		file, err := s.openObject(objectID)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		liveID := appleContentID(file)
		_ = file.Close()
		if _, err := s.db.ExecContext(ctx, "UPDATE objects SET live_id = ? WHERE id = ? AND live_id IS NULL", liveID, objectID); err != nil {
			return err
		}
	}
	return nil
}

// liveStills lists the photos of a library the caller may change whose still
// carries liveID and that have no video yet.
func (s *Service) liveStills(ctx context.Context, p Principal, lib library, liveID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.id, a.uploaded_by FROM assets a JOIN objects o ON o.id = a.object_id
WHERE a.library_id = ? AND a.trashed_at IS NULL AND a.motion_object_id IS NULL AND o.live_id = ?
ORDER BY a.imported_at, a.id`, lib.id, liveID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id, uploadedBy string
		if err := rows.Scan(&id, &uploadedBy); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if canChange(p, lib, uploadedBy) {
			ids = append(ids, id)
		}
	}
	return ids, errors.Join(rows.Err(), rows.Close())
}
