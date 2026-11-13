package scoring

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"io"
	"math"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	// Previews are rendered from JPEG, PNG and WebP originals.
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/webp"

	"github.com/zhongwater123/A-NAS/internal/atomicfile"
	"github.com/zhongwater123/A-NAS/internal/decodepanic"
	"github.com/zhongwater123/A-NAS/internal/filesystem"
	"github.com/zhongwater123/A-NAS/internal/imagerender"
)

// Wake makes the import worker look for work now.
func (s *Store) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// RunImports scans new batches' folders and renders previews one file at a
// time until ctx ends. Work survives restarts: every file's state is in the
// database, so an interrupted import continues where it stopped.
func (s *Store) RunImports(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		worked, err := s.ImportStep(ctx)
		if err != nil && ctx.Err() == nil {
			s.logger.ErrorContext(ctx, "photo scoring import failed", "error", err)
		}
		if worked && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-ticker.C:
		}
	}
}

// ImportStep does one unit of import work: scanning one batch's folder or
// importing one file. It reports whether there was any.
func (s *Store) ImportStep(ctx context.Context) (bool, error) {
	var batchID, source string
	var scanned bool
	err := s.db.QueryRowContext(ctx, `SELECT id, source, scanned FROM batches WHERE state = 'importing'
		ORDER BY created_at, id LIMIT 1`).Scan(&batchID, &source, &scanned)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !scanned {
		return true, s.scan(ctx, batchID, source)
	}
	var file string
	var anchor sql.NullInt64
	err = s.db.QueryRowContext(ctx, `SELECT path, anchor_score FROM import_files WHERE batch_id = ? AND status = 'pending'
		ORDER BY path LIMIT 1`, batchID).Scan(&file, &anchor)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = s.db.ExecContext(ctx, "UPDATE batches SET state = 'open' WHERE id = ? AND state = 'importing'", batchID)
		return true, err
	}
	if err != nil {
		return false, err
	}
	return true, s.importFile(ctx, batchID, source, file, anchor)
}

// anchorName is a reference photo's file name: its score from 0 to 5 with
// at most two decimals, then "-", "_" or a space.
var anchorName = regexp.MustCompile(`^(5(?:\.0{1,2})?|[0-4](?:\.[0-9]{1,2})?)[-_ ]`)

// scan lists the batch's folder once: every regular file not hidden, with
// the anchors' scores taken from their names.
func (s *Store) scan(ctx context.Context, batchID, source string) error {
	entries, err := s.photos.Scan(ctx, filepath.Join(s.photosDir, source), filesystem.ScanSkip{Prefixes: []string{"."}})
	if err != nil {
		return fmt.Errorf("scanning %q: %w", source, err)
	}
	return s.within(ctx, func(tx *sql.Tx) error {
		for _, entry := range entries {
			if entry.Info.Kind != filesystem.KindFile {
				continue
			}
			status, reason := "pending", ""
			var anchor any
			if strings.HasPrefix(entry.Path, AnchorFolder+"/") {
				match := anchorName.FindStringSubmatch(path.Base(entry.Path))
				if match == nil || strings.Count(entry.Path, "/") > 1 {
					status, reason = "skipped", "anchor_name"
				} else {
					value, _ := strconv.ParseFloat(match[1], 64)
					anchor = int(math.Round(value * 100))
				}
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO import_files (batch_id, path, anchor_score, status, reason) VALUES (?, ?, ?, ?, NULLIF(?, ''))",
				batchID, entry.Path, anchor, status, reason); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, "UPDATE batches SET scanned = 1 WHERE id = ?", batchID)
		return err
	})
}

// importFile hashes one original, renders its preview unless another batch
// already did, and adds it to the batch. A file it cannot use is skipped
// with the reason, never retried.
func (s *Store) importFile(ctx context.Context, batchID, source, file string, anchor sql.NullInt64) error {
	content, reason, err := s.render(ctx, filepath.Join(s.photosDir, source, filepath.FromSlash(file)))
	if err != nil {
		return err
	}
	return s.within(ctx, func(tx *sql.Tx) error {
		if reason != "" {
			_, err := tx.ExecContext(ctx, "UPDATE import_files SET status = 'skipped', reason = ? WHERE batch_id = ? AND path = ?", reason, batchID, file)
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO contents (sha256, width, height, preview_width, preview_height) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (sha256) DO NOTHING`, content.sha, content.width, content.height, content.previewWidth, content.previewHeight)
		if err != nil {
			return err
		}
		var anchorScore any
		if anchor.Valid {
			anchorScore = anchor.Int64
		}
		// The same photo as an anchor and as a photo to score stays an
		// anchor, so that nobody scores a photo they were shown a score for.
		_, err = tx.ExecContext(ctx, `INSERT INTO items (batch_id, sha256, anchor_score, shuffle) VALUES (?, ?, ?, ?)
			ON CONFLICT (batch_id, sha256) DO UPDATE SET anchor_score = coalesce(excluded.anchor_score, items.anchor_score)`,
			batchID, content.sha, anchorScore, s.randomShuffle())
		if err != nil {
			return err
		}
		var item int64
		if err := tx.QueryRowContext(ctx, "SELECT id FROM items WHERE batch_id = ? AND sha256 = ?", batchID, content.sha).Scan(&item); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO item_paths (item_id, path) VALUES (?, ?) ON CONFLICT DO NOTHING", item, file); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE import_files SET status = 'done' WHERE batch_id = ? AND path = ?", batchID, file)
		return err
	})
}

type content struct {
	sha                         string
	width, height               int
	previewWidth, previewHeight int
}

// render reads one original read-only. A file that is not a JPEG, PNG or
// WebP picture, too large, or that the decoder rejects or panics on yields a
// reason to skip it; err is left for failures worth retrying.
func (s *Store) render(ctx context.Context, name string) (found content, reason string, err error) {
	// Errors reading the original concern that file alone (it went away,
	// is unreadable or not a regular file): skip it rather than retry it
	// forever.
	file, err := s.photos.Open(ctx, name)
	switch {
	case errors.Is(err, filesystem.ErrUnsupportedType):
		return content{}, "not_a_file", nil
	case errors.Is(err, os.ErrNotExist):
		return content{}, "missing", nil
	case err != nil:
		return content{}, "unreadable", nil
	}
	defer func() { _ = file.Close() }()
	header := make([]byte, 512)
	n, err := io.ReadFull(file, header)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return content{}, "unreadable", nil
	}
	switch http.DetectContentType(header[:n]) {
	case "image/jpeg", "image/png", "image/webp":
	default:
		return content{}, "unsupported_type", nil
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return content{}, "unreadable", nil
	}
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(file, maxOriginalBytes+1))
	if err != nil {
		return content{}, "unreadable", nil
	}
	if size > maxOriginalBytes {
		return content{}, "too_large", nil
	}
	found.sha = hex.EncodeToString(hash.Sum(nil))
	var previewWidth, previewHeight, width, height int
	err = s.db.QueryRowContext(ctx, "SELECT width, height, preview_width, preview_height FROM contents WHERE sha256 = ?", found.sha).
		Scan(&width, &height, &previewWidth, &previewHeight)
	if err == nil {
		if _, statErr := os.Stat(s.previewPath(found.sha)); statErr == nil {
			found.width, found.height, found.previewWidth, found.previewHeight = width, height, previewWidth, previewHeight
			return found, "", nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return content{}, "", err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return content{}, "", err
	}
	orientation, _ := imagerender.ReadEXIF(file, time.UTC, "photo scoring EXIF")
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return content{}, "", err
	}
	preview, reason, err := renderPreview(ctx, file, orientation, &found)
	if err != nil || reason != "" {
		return content{}, reason, err
	}
	if err := os.MkdirAll(filepath.Dir(s.previewPath(found.sha)), 0o700); err != nil {
		return content{}, "", err
	}
	if err := atomicfile.Write(s.previewPath(found.sha), preview, 0o600); err != nil {
		return content{}, "", err
	}
	return found, "", nil
}

// renderPreview decodes under the shared budget and encodes the upright
// preview; a decoder panic is logged without the file's name and skips it.
func renderPreview(ctx context.Context, file io.ReadSeeker, orientation int, found *content) (preview []byte, reason string, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			decodepanic.Log("photo scoring preview", recovered)
			preview, reason, err = nil, "undecodable", nil
		}
	}()
	err = imagerender.WithDecoded(ctx, file, func(src image.Image) error {
		bounds := src.Bounds()
		found.width, found.height = bounds.Dx(), bounds.Dy()
		if orientation >= 5 {
			found.width, found.height = found.height, found.width
		}
		var renderErr error
		preview, found.previewWidth, found.previewHeight, renderErr = imagerender.UprightJPEG(src, orientation, PreviewEdge, previewQuality)
		return renderErr
	})
	switch {
	case errors.Is(err, imagerender.ErrTooLarge):
		return nil, "too_large", nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return nil, "", err
	case err != nil:
		return nil, "undecodable", nil
	}
	return preview, "", nil
}
