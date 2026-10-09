package photos

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Derived data is rebuilt from originals and may be deleted at any time. A
// derivation names one capability at one version; derivations that depend
// only on the original's bytes are keyed by content object, so copies and
// duplicates share them and a repeated job converges on one current result.

// ThumbnailState reports whether an asset's thumbnail can be served yet.
type ThumbnailState string

const (
	ThumbnailReady   ThumbnailState = "ready"
	ThumbnailPending ThumbnailState = "pending"
	ThumbnailFailed  ThumbnailState = "failed"
)

const (
	thumbnailDerivation = "thumbnail/v1"
	derivedDir          = "derived"

	jobLease       = 5 * time.Minute
	jobMaxAttempts = 3
	jobRetryDelay  = time.Minute
)

// Stable job error classes; they never include original content.
const (
	jobErrorUndecodable     = "undecodable"
	jobErrorMissingOriginal = "missing_original"
	jobErrorIO              = "io"
	// jobErrorInterrupted marks a job whose lease ran out on every attempt,
	// as when decoding the original keeps killing the service.
	jobErrorInterrupted = "interrupted"
)

var ErrThumbnailUnavailable = errors.New("photo thumbnail is not available yet")

func derivedPath(derivation, objectID string) string {
	return filepath.Join(derivedDir, strings.ReplaceAll(derivation, "/", "-"), objectID[0:2], objectID+".jpg")
}

// Thumbnail opens an asset's grid thumbnail. Callers fall back to the
// original while the thumbnail is pending or failed.
func (s *Service) Thumbnail(ctx context.Context, p Principal, assetID string) (Content, error) {
	record, err := s.visibleAsset(ctx, s.db, p, assetID)
	if err != nil {
		return Content{}, err
	}
	if record.Thumbnail != ThumbnailReady {
		return Content{}, ErrThumbnailUnavailable
	}
	file, err := s.root.Open(derivedPath(thumbnailDerivation, record.objectID))
	if errors.Is(err, fs.ErrNotExist) {
		return Content{}, ErrThumbnailUnavailable
	}
	if err != nil {
		return Content{}, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return Content{}, err
	}
	return Content{
		Name: record.Name, MediaType: "image/jpeg", SizeBytes: info.Size(),
		ETag: `"` + record.objectID + "." + thumbnailDerivation + `"`, Reader: file,
	}, nil
}

// enqueueDerivations schedules the media work for a newly referenced object
// unless its results already exist.
func (s *Service) enqueueDerivations(ctx context.Context, tx *sql.Tx, objectID string) error {
	now := formatTime(s.now())
	_, err := tx.ExecContext(ctx, `
INSERT INTO jobs(object_id, derivation, state, not_before, created_at)
SELECT ?, ?, 'pending', ?, ?
WHERE NOT EXISTS (SELECT 1 FROM derived_files WHERE object_id = ? AND derivation = ?)
ON CONFLICT(object_id, derivation) DO NOTHING`,
		objectID, thumbnailDerivation, now, now, objectID, thumbnailDerivation)
	return err
}

func (s *Service) wakeMedia() {
	select {
	case s.mediaWake <- struct{}{}:
	default:
	}
}

// RunMedia processes media jobs until ctx ends. It runs as the photo service
// and needs no user session; an empty queue is rechecked every idle interval
// or as soon as an import adds work.
func (s *Service) RunMedia(ctx context.Context, idle time.Duration, logError func(error)) {
	for {
		processed, err := s.ProcessMediaJob(ctx)
		if err != nil && ctx.Err() == nil && logError != nil {
			logError(err)
		}
		if processed && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-s.mediaWake:
		case <-time.After(idle):
		}
	}
}

type claimedJob struct {
	objectID    string
	derivation  string
	attempts    int
	orientation int
}

// ProcessMediaJob runs one ready media job and reports whether there was one.
// A job whose original cannot be processed is retried with a delay and then
// marked failed; the error returned is only for Catalog or storage faults.
func (s *Service) ProcessMediaJob(ctx context.Context) (bool, error) {
	job, err := s.claimJob(ctx, thumbnailDerivation)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	thumbnail, width, height, class := s.renderJob(job)
	if class != "" {
		return true, s.failJob(ctx, job, class)
	}
	return true, s.completeJob(ctx, job, thumbnail, width, height)
}

// claimJob leases the next ready job of one derivation; thumbnails and AI
// work run in separate loops with different rules.
func (s *Service) claimJob(ctx context.Context, derivation string) (claimedJob, error) {
	// Every claim counts as an attempt, so a job that never finishes stops
	// being reclaimed once its attempts are used up.
	if _, err := s.db.ExecContext(ctx, `
UPDATE jobs SET state = 'failed', lease_until = NULL, error_class = ?
WHERE derivation = ? AND state = 'running' AND lease_until <= ? AND attempts >= ?`,
		jobErrorInterrupted, derivation, formatTime(s.now()), jobMaxAttempts); err != nil {
		return claimedJob{}, err
	}
	var job claimedJob
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		now := formatTime(s.now())
		if err := tx.QueryRowContext(ctx, `
SELECT j.object_id, j.derivation, j.attempts, o.orientation
FROM jobs j JOIN objects o ON o.id = j.object_id
WHERE j.derivation = ? AND ((j.state = 'pending' AND j.not_before <= ?) OR (j.state = 'running' AND j.lease_until <= ?))
ORDER BY j.not_before, j.object_id
LIMIT 1`, derivation, now, now).Scan(&job.objectID, &job.derivation, &job.attempts, &job.orientation); err != nil {
			return err
		}
		job.attempts++
		_, err := tx.ExecContext(ctx,
			"UPDATE jobs SET state = 'running', attempts = ?, lease_until = ? WHERE object_id = ? AND derivation = ?",
			job.attempts, formatTime(s.now().Add(jobLease)), job.objectID, job.derivation)
		return err
	})
	return job, err
}

// renderJob treats a decoder panic like an undecodable original: the same
// bytes would panic again, and the service must keep serving.
func (s *Service) renderJob(job claimedJob) (thumbnail []byte, width, height int, class string) {
	defer func() {
		if recover() != nil {
			thumbnail, width, height, class = nil, 0, 0, jobErrorUndecodable
		}
	}()
	original, err := s.openObject(job.objectID)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, 0, jobErrorMissingOriginal
	}
	if err != nil {
		return nil, 0, 0, jobErrorIO
	}
	defer original.Close()
	thumbnail, width, height, err = renderThumbnail(original, job.orientation)
	if err != nil {
		return nil, 0, 0, jobErrorUndecodable
	}
	return thumbnail, width, height, ""
}

// completeJob stores the result and retires the job under commitMu, so that
// Reconcile never sees a result file without its row and a purge of the
// object either precedes the write (and the job is gone) or removes both.
func (s *Service) completeJob(ctx context.Context, job claimedJob, thumbnail []byte, width, height int) error {
	s.commitMu.Lock()
	defer s.commitMu.Unlock()
	var exists bool
	if err := s.db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM jobs WHERE object_id = ? AND derivation = ?)",
		job.objectID, job.derivation).Scan(&exists); err != nil || !exists {
		// Purged while rendering.
		return err
	}
	target := derivedPath(job.derivation, job.objectID)
	if err := s.writeDerived(target, thumbnail); err != nil {
		return errors.Join(err, s.failJob(ctx, job, jobErrorIO))
	}
	if err := s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO derived_files(object_id, derivation, media_type, width, height, size_bytes, created_at)
VALUES(?, ?, 'image/jpeg', ?, ?, ?, ?)
ON CONFLICT(object_id, derivation) DO UPDATE SET width = excluded.width, height = excluded.height,
    size_bytes = excluded.size_bytes, created_at = excluded.created_at`,
			job.objectID, job.derivation, width, height, len(thumbnail), formatTime(s.now())); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM jobs WHERE object_id = ? AND derivation = ?", job.objectID, job.derivation); err != nil {
			return err
		}
		// The thumbnail is the AI input.
		return s.enqueueEmbedding(ctx, tx, job.objectID)
	}); err != nil {
		return err
	}
	s.wakeAI()
	return nil
}

func (s *Service) failJob(ctx context.Context, job claimedJob, class string) error {
	permanent := class == jobErrorUndecodable || class == jobErrorMissingOriginal || class == jobErrorInvalidResult
	if permanent || job.attempts >= jobMaxAttempts {
		_, err := s.db.ExecContext(ctx,
			"UPDATE jobs SET state = 'failed', lease_until = NULL, error_class = ? WHERE object_id = ? AND derivation = ?",
			class, job.objectID, job.derivation)
		return err
	}
	retryAt := s.now().Add(jobRetryDelay << (job.attempts - 1))
	_, err := s.db.ExecContext(ctx,
		"UPDATE jobs SET state = 'pending', lease_until = NULL, not_before = ?, error_class = ? WHERE object_id = ? AND derivation = ?",
		formatTime(retryAt), class, job.objectID, job.derivation)
	return err
}

// writeDerived replaces a derived file atomically through staging.
func (s *Service) writeDerived(target string, content []byte) error {
	if err := s.root.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	temporary := filepath.Join(stagingDir, s.randomHex()+".derived")
	file, err := s.root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(content)
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		_ = s.root.Remove(temporary)
		return err
	}
	if err := s.root.Rename(temporary, target); err != nil {
		_ = s.root.Remove(temporary)
		return fmt.Errorf("publish derived file: %w", err)
	}
	return nil
}

// removeDerived deletes every derived file of an object whose rows are gone.
func (s *Service) removeDerived(objectID string, derivations []string) error {
	var removeErr error
	for _, derivation := range derivations {
		err := s.root.Remove(derivedPath(derivation, objectID))
		if !errors.Is(err, fs.ErrNotExist) {
			removeErr = errors.Join(removeErr, err)
		}
	}
	return removeErr
}
