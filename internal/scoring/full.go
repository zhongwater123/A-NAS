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
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/atomicfile"
	"github.com/zhongwater123/A-NAS/internal/decodepanic"
	"github.com/zhongwater123/A-NAS/internal/imagerender"
)

// ErrOriginalChanged reports a photo whose original is gone from the batch's
// folder or no longer the file that was imported.
var ErrOriginalChanged = errors.New("original is gone or changed")

const (
	// fullQuality shapes the full-size JPEG; the cache keeps the latest
	// fullKept, so that the raters of the same photo share one render.
	fullQuality = 92
	fullKept    = 16
)

// FullView is a photo at its full size: a JPEG in its stored orientation,
// which the page applies, without any of the original's metadata.
type FullView struct {
	Name        string
	Orientation int
}

// ClaimFull returns the photo behind a rater's own claim at its full size,
// for judging sharpness, in batches that score it. The original is read
// again and must still be the photo that was imported.
func (s *Store) ClaimFull(ctx context.Context, token, rater string) (FullView, error) {
	var item int64
	var sha, source, dimensions string
	err := s.db.QueryRowContext(ctx, `SELECT i.id, i.sha256, b.source, b.dimensions FROM claims c
		JOIN items i ON i.id = c.item_id JOIN batches b ON b.id = c.batch_id WHERE c.token = ? AND c.rater = ?`,
		token, rater).Scan(&item, &sha, &source, &dimensions)
	if errors.Is(err, sql.ErrNoRows) {
		return FullView{}, ErrNotFound
	}
	if err != nil {
		return FullView{}, err
	}
	if !slices.Contains(splitDimensions(dimensions), Sharpness) {
		return FullView{}, ErrNotFound
	}
	if view, ok := s.cachedFull(sha); ok {
		return view, nil
	}
	var paths []string
	err = each(ctx, s.db, "SELECT path FROM item_paths WHERE item_id = ? ORDER BY path", []any{item}, func(rows *sql.Rows) error {
		var path string
		err := rows.Scan(&path)
		paths = append(paths, path)
		return err
	})
	if err != nil {
		return FullView{}, err
	}
	for _, path := range paths {
		view, err := s.renderFull(ctx, filepath.Join(s.photosDir, source, filepath.FromSlash(path)), sha)
		if errors.Is(err, ErrOriginalChanged) {
			continue
		}
		return view, err
	}
	return FullView{}, ErrOriginalChanged
}

func (s *Store) fullPath(sha string, orientation int) string {
	return filepath.Join(s.dir, "full", fmt.Sprintf("%s-o%d.jpg", sha, orientation))
}

// cachedFull finds a render of sha and marks it as the latest used.
func (s *Store) cachedFull(sha string) (FullView, bool) {
	matches, err := filepath.Glob(filepath.Join(s.dir, "full", sha+"-o*.jpg"))
	if err != nil || len(matches) == 0 {
		return FullView{}, false
	}
	name := matches[0]
	orientation, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(name), sha+"-o"), ".jpg"))
	if err != nil || orientation < 1 || orientation > 8 {
		return FullView{}, false
	}
	now := s.now()
	_ = os.Chtimes(name, now, now)
	return FullView{Name: name, Orientation: orientation}, true
}

// renderFull renders the original at name when it still hashes to sha.
func (s *Store) renderFull(ctx context.Context, name, sha string) (view FullView, err error) {
	file, err := s.photos.Open(ctx, name)
	if err != nil {
		return FullView{}, ErrOriginalChanged
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, maxOriginalBytes+1)); err != nil {
		return FullView{}, ErrOriginalChanged
	}
	if hex.EncodeToString(hash.Sum(nil)) != sha {
		return FullView{}, ErrOriginalChanged
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return FullView{}, err
	}
	orientation, _ := imagerender.ReadEXIF(file, time.UTC, "photo scoring EXIF")
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return FullView{}, err
	}
	var data []byte
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				decodepanic.Log("photo scoring full view", recovered)
				err = fmt.Errorf("photo %s cannot be decoded", sha)
			}
		}()
		err = imagerender.WithDecoded(ctx, file, func(src image.Image) error {
			var encodeErr error
			data, encodeErr = imagerender.FullJPEG(src, fullQuality)
			return encodeErr
		})
	}()
	if err != nil {
		return FullView{}, err
	}
	view = FullView{Name: s.fullPath(sha, orientation), Orientation: orientation}
	if err := atomicfile.Write(view.Name, data, 0o600); err != nil {
		return FullView{}, err
	}
	s.pruneFull()
	return view, nil
}

// pruneFull keeps the fullKept latest used renders.
func (s *Store) pruneFull() {
	entries, err := os.ReadDir(filepath.Join(s.dir, "full"))
	if err != nil {
		return
	}
	type render struct {
		name string
		used time.Time
	}
	var renders []render
	for _, entry := range entries {
		info, err := entry.Info()
		if err == nil && info.Mode().IsRegular() && strings.HasSuffix(entry.Name(), ".jpg") {
			renders = append(renders, render{filepath.Join(s.dir, "full", entry.Name()), info.ModTime()})
		}
	}
	slices.SortFunc(renders, func(a, b render) int { return b.used.Compare(a.used) })
	for _, old := range renders[min(len(renders), fullKept):] {
		if err := os.Remove(old.name); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.logger.Warn("removing an old full-size render failed", "error", err)
		}
	}
}
