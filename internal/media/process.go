package media

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
)

const (
	// fullScanInterval is how often every library is scanned in the
	// background; opening the media center also scans stale libraries.
	fullScanInterval = 15 * time.Minute
	frameWidth       = 640
	processBatch     = 50
)

// Run scans libraries and reads pending videos until ctx ends.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	var lastScan time.Time
	for {
		if s.now().Sub(lastScan) >= fullScanInterval {
			s.scanAll(ctx)
			s.pruneCache(ctx)
			lastScan = s.now()
		}
		s.processPending(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.kick:
		}
	}
}

type pendingVideo struct {
	id, entryID  string
	probe, frame string
	duration     float64
	hasPoster    bool
}

// processPending probes durations and codecs and takes a frame of every
// pending video, one at a time, as a user who can read it.
func (s *Service) processPending(ctx context.Context) {
	if s.processor == nil {
		return
	}
	libraries, err := s.allLibraries(ctx)
	if err != nil {
		return
	}
	for _, library := range libraries {
		session, ok := s.sessionFor(ctx, library)
		if !ok {
			continue
		}
		attempted := map[string]bool{}
		for ctx.Err() == nil {
			pending, err := s.pendingVideos(ctx, library.ID)
			if err != nil || len(pending) == 0 {
				break
			}
			stop := true
			for _, video := range pending {
				if attempted[video.id] {
					continue
				}
				attempted[video.id], stop = true, false
				if err := s.processVideo(asUser(ctx, session), session.User, video); err != nil {
					if errors.Is(err, ErrUnavailable) || errors.Is(err, files.ErrVolumeUnavailable) || errors.Is(err, files.ErrForbidden) || ctx.Err() != nil {
						stop = true
						break
					}
				}
			}
			if stop {
				break
			}
		}
	}
}

func (s *Service) pendingVideos(ctx context.Context, libraryID string) ([]pendingVideo, error) {
	rows, err := s.store.db.QueryContext(ctx, `
SELECT id, entry_id, probe_state, frame_state, duration, poster_entry <> '' OR backdrop_entry <> '' OR thumb_entry <> ''
FROM videos WHERE library_id = ? AND (probe_state = 'pending' OR frame_state = 'pending') ORDER BY added_at DESC LIMIT ?`, libraryID, processBatch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pending []pendingVideo
	for rows.Next() {
		var video pendingVideo
		if err := rows.Scan(&video.id, &video.entryID, &video.probe, &video.frame, &video.duration, &video.hasPoster); err != nil {
			return nil, err
		}
		pending = append(pending, video)
	}
	return pending, rows.Err()
}

// processVideo reads one video. Errors that would repeat for the same file
// mark it failed; errors of the environment leave it pending.
func (s *Service) processVideo(ctx context.Context, actor accounts.User, video pendingVideo) error {
	content, err := s.files.OpenContent(ctx, actor, video.entryID)
	if err != nil {
		if errors.Is(err, files.ErrNotFound) {
			_, _ = s.store.db.ExecContext(ctx, "UPDATE videos SET probe_state = 'failed', probe_error = 'missing', frame_state = 'failed' WHERE id = ?", video.id)
			return nil
		}
		return err
	}
	defer content.Reader.Close()
	duration := video.duration
	if video.probe == "pending" {
		info, err := s.processor.Probe(ctx, content.Reader)
		switch {
		case errors.Is(err, ErrUnavailable) || ctx.Err() != nil:
			return ErrUnavailable
		case err != nil:
			_, err = s.store.db.ExecContext(ctx, "UPDATE videos SET probe_state = 'failed', probe_error = ?, frame_state = 'failed' WHERE id = ?", truncate(err.Error(), 300), video.id)
			return err
		}
		encoded, _ := json.Marshal(info)
		width, height, hdr := 0, 0, ""
		if info.Video != nil {
			width, height, hdr = info.Video.Width, info.Video.Height, info.Video.HDR
		}
		duration = info.Duration
		frame := video.frame
		if info.Video == nil {
			frame = "none"
		}
		if _, err := s.store.db.ExecContext(ctx, `
UPDATE videos SET probe_state = 'ready', probe_error = '', probe = ?, duration = ?, width = ?, height = ?, hdr = ?, frame_state = ? WHERE id = ?`,
			string(encoded), info.Duration, width, height, hdr, frame, video.id); err != nil {
			return err
		}
		video.frame = frame
	}
	if video.frame != "pending" {
		return nil
	}
	if s.cacheDir == "" {
		_, err := s.store.db.ExecContext(ctx, "UPDATE videos SET frame_state = 'none' WHERE id = ?", video.id)
		return err
	}
	image, err := s.processor.Frame(ctx, content.Reader, frameTime(duration), frameWidth)
	switch {
	case errors.Is(err, ErrUnavailable) || ctx.Err() != nil:
		return ErrUnavailable
	case err != nil:
		_, err = s.store.db.ExecContext(ctx, "UPDATE videos SET frame_state = 'failed' WHERE id = ?", video.id)
		return err
	}
	if err := writeAtomically(s.framePath(video.id), image); err != nil {
		return err
	}
	_, err = s.store.db.ExecContext(ctx, "UPDATE videos SET frame_state = 'ready' WHERE id = ?", video.id)
	return err
}

// frameTime skips opening titles: a tenth into the video, at most ten
// minutes in.
func frameTime(duration float64) float64 {
	if duration <= 0 || math.IsNaN(duration) {
		return 0
	}
	return math.Min(duration*0.1, 600)
}

func (s *Service) framePath(videoID string) string {
	return filepath.Join(s.cacheDir, "frames", cacheName(videoID)+".jpg")
}

// cacheName turns a resource ID into a file name.
func cacheName(id string) string {
	name := make([]rune, 0, len(id))
	for _, r := range id {
		if r == ':' || r == '/' || r == '\\' {
			r = '_'
		}
		name = append(name, r)
	}
	return string(name)
}

func (s *Service) dropFrames(ids []string) {
	if s.cacheDir == "" {
		return
	}
	for _, id := range ids {
		_ = os.Remove(s.framePath(id))
		matches, _ := filepath.Glob(filepath.Join(s.cacheDir, "subtitles", cacheName(id)+"-*"))
		for _, match := range matches {
			_ = os.Remove(match)
		}
	}
}

func writeAtomically(target string, data []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(target), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), target)
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

// pruneCache removes resized artwork no title refers to any more.
func (s *Service) pruneCache(ctx context.Context) {
	if s.cacheDir == "" {
		return
	}
	referenced := map[string]bool{}
	rows, err := s.store.db.QueryContext(ctx, `
SELECT poster_entry FROM videos UNION SELECT backdrop_entry FROM videos UNION SELECT thumb_entry FROM videos
UNION SELECT poster_entry FROM shows UNION SELECT backdrop_entry FROM shows`)
	if err != nil {
		return
	}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil && id != "" {
			referenced[cacheName(id)] = true
		}
	}
	_ = rows.Close()
	entries, err := os.ReadDir(filepath.Join(s.cacheDir, "artwork"))
	if err != nil {
		return
	}
	for _, entry := range entries {
		// Names are <entry>--<size>-<modified>.jpg.
		if key, _, _ := strings.Cut(entry.Name(), "--"); !referenced[key] {
			_ = os.Remove(filepath.Join(s.cacheDir, "artwork", entry.Name()))
		}
	}
}
