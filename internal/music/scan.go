package music

import (
	"context"
	"errors"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/mediaworker"
)

// Scan phases: listing the music folders, reading the tags of new and changed
// files, then measuring the durations that need the whole file.
const (
	PhaseListing   = "listing"
	PhaseReading   = "reading"
	PhaseMeasuring = "measuring"
)

// ScanStatus reports a member's latest scan. It lives in memory: after a
// restart the next visit scans again.
type ScanStatus struct {
	State      string     `json:"state"`
	Phase      string     `json:"phase,omitempty"`
	Done       int        `json:"done"`
	Total      int        `json:"total"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	// Error is volume_unavailable or failed when the last scan stopped early.
	Error string `json:"error,omitempty"`
}

type Status struct {
	Version int64      `json:"version"`
	Tracks  int        `json:"tracks"`
	Scan    ScanStatus `json:"scan"`
}

type scanJob struct {
	mu      sync.Mutex
	status  ScanStatus
	running bool
	// again asks a running scan to start over when it ends, because the
	// member changed music folders or files while it ran.
	again bool
}

func (j *scanJob) snapshot() ScanStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.status
}

func (j *scanJob) progress(phase string, done, total int) {
	j.mu.Lock()
	j.status.Phase, j.status.Done, j.status.Total = phase, done, total
	j.mu.Unlock()
}

func (j *scanJob) step() {
	j.mu.Lock()
	j.status.Done++
	j.mu.Unlock()
}

// Status reports the member's library size and scan.
func (s *Service) Status(ctx context.Context, actor accounts.User) (Status, error) {
	scopes, err := s.scopes(ctx, actor)
	if err != nil {
		return Status{}, err
	}
	count, err := s.store.countIn(ctx, scopes)
	if err != nil {
		return Status{}, err
	}
	status := Status{Version: s.version.Load(), Tracks: count, Scan: ScanStatus{State: "idle"}}
	s.mu.Lock()
	job := s.scans[actor.ID]
	s.mu.Unlock()
	if job != nil {
		status.Scan = job.snapshot()
	}
	return status, nil
}

// StartScan brings the member's library up to date in the background. The
// scan reads files as the member, so ctx must carry the member's session; it
// stops early if the session ends. Asking while a scan runs schedules one
// more pass.
func (s *Service) StartScan(ctx context.Context, actor accounts.User) (Status, error) {
	scanCtx := accounts.WithSessionToken(s.base, accounts.SessionToken(ctx))
	s.mu.Lock()
	job := s.scans[actor.ID]
	if job == nil {
		job = &scanJob{}
		s.scans[actor.ID] = job
	}
	job.mu.Lock()
	if job.running {
		job.again = true
	} else {
		started := s.now().UTC()
		job.running = true
		job.status = ScanStatus{State: "scanning", Phase: PhaseListing, StartedAt: &started}
		s.wg.Add(1)
		go s.runScan(scanCtx, actor, job)
	}
	job.mu.Unlock()
	s.mu.Unlock()
	return s.Status(ctx, actor)
}

func (s *Service) runScan(ctx context.Context, actor accounts.User, job *scanJob) {
	defer s.wg.Done()
	for {
		var err error
		select {
		case s.work <- struct{}{}:
			started := time.Now()
			var summary scanSummary
			summary, err = s.scan(ctx, actor, job)
			<-s.work
			if err == nil {
				s.logger.InfoContext(ctx, "music library scanned", "user_id", actor.ID, "tracks", summary.tracks,
					"read", summary.read, "removed", summary.removed, "measured", summary.measured, "elapsed", time.Since(started).Round(time.Millisecond))
			}
		case <-ctx.Done():
			err = ctx.Err()
		}
		job.mu.Lock()
		if err == nil && job.again {
			job.again = false
			job.status.Phase, job.status.Done, job.status.Total = PhaseListing, 0, 0
			job.mu.Unlock()
			continue
		}
		finished := s.now().UTC()
		job.running, job.again = false, false
		job.status.State, job.status.Phase, job.status.FinishedAt = "idle", "", &finished
		switch {
		case err == nil:
		case errors.Is(err, files.ErrVolumeUnavailable):
			job.status.Error = "volume_unavailable"
		default:
			job.status.Error = "failed"
			if !errors.Is(err, context.Canceled) {
				s.logger.ErrorContext(ctx, "music library scan failed", "user_id", actor.ID, "error", err)
			}
		}
		job.mu.Unlock()
		return
	}
}

type scanSummary struct{ tracks, read, removed, measured int }

// batchSize is how many tracks a scan writes per transaction; each commit
// is visible to clients through a new library version.
const batchSize = 25

// ignoredFolders are folders other systems keep beside user files.
var ignoredFolders = map[string]bool{"@eadir": true, "#recycle": true, "$recycle.bin": true, "system volume information": true, "lost+found": true}

func ignored(relative string) bool {
	for _, part := range strings.Split(relative, "/") {
		if strings.HasPrefix(part, ".") || ignoredFolders[strings.ToLower(part)] {
			return true
		}
	}
	return false
}

func dirOf(relative string) string {
	if dir := path.Dir(relative); dir != "." {
		return dir
	}
	return ""
}

func stem(name string) string { return strings.ToLower(strings.TrimSuffix(name, path.Ext(name))) }

// quickDuration formats state their length in a header, so a scan measures
// them while reading tags; MP3 needs every frame counted.
var quickDuration = map[string]bool{"flac": true, "m4a": true, "ogg": true, "wav": true}

func (s *Service) scan(ctx context.Context, actor accounts.User, job *scanJob) (scanSummary, error) {
	var summary scanSummary
	sources, err := s.Sources(ctx, actor)
	if err != nil {
		return summary, err
	}
	audio := map[string]files.TreeEntry{}
	images := map[string]files.TreeEntry{}
	lyrics := map[string]string{}
	var scopes []scope
	for position, source := range sources.Items {
		folder, entries, err := s.listFolder(ctx, actor, source.SpaceID, source.FolderID)
		if errors.Is(err, files.ErrNotFound) && source.FolderID != "" {
			// The folder was deleted; the member may restore it from the trash.
			continue
		}
		if err != nil {
			return summary, err
		}
		if sources.Custom && folder != source.Path {
			// Follow a renamed or moved folder.
			if _, err := s.store.db.ExecContext(ctx, "UPDATE sources SET path = ? WHERE user_id = ? AND position = ?", folder, actor.ID, position); err != nil {
				return summary, err
			}
		}
		scopes = append(scopes, scope{SpaceID: source.SpaceID, Path: folder})
		for _, entry := range entries {
			if entry.Kind != files.EntryKindFile || ignored(entry.Path) {
				continue
			}
			dirKey := entry.SpaceID + "\x00" + dirOf(entry.Path)
			switch {
			case formatOf(entry.Name) != "":
				audio[entry.ID] = entry
			case strings.EqualFold(path.Ext(entry.Name), ".lrc"):
				lyrics[dirKey+"\x00"+stem(entry.Name)] = entry.ID
			case folderCoverRank(entry.Name) > 0:
				if best, ok := images[dirKey]; !ok || folderCoverRank(entry.Name) > folderCoverRank(best.Name) {
					images[dirKey] = entry
				}
			}
		}
	}
	scopes = normalizeScopes(scopes)
	summary.tracks = len(audio)

	existing, err := s.store.rowsIn(ctx, scopes)
	if err != nil {
		return summary, err
	}
	known := make(map[string]row, len(existing))
	var removed []string
	for _, value := range existing {
		known[value.ID] = value
		if _, ok := audio[value.ID]; !ok {
			removed = append(removed, value.ID)
		}
	}
	if err := s.store.delete(ctx, removed); err != nil {
		return summary, err
	}
	summary.removed = len(removed)

	folderCovers := map[string]string{}
	for key, image := range images {
		if cover, err := s.folderCover(ctx, actor, image); err == nil {
			folderCovers[key] = cover
		} else if errors.Is(err, files.ErrVolumeUnavailable) {
			return summary, err
		}
	}
	coverOf := func(spaceID, dir string) string {
		if cover := folderCovers[spaceID+"\x00"+dir]; cover != "" {
			return cover
		}
		return folderCovers[spaceID+"\x00"+albumDir(dir)]
	}

	ordered := make([]files.TreeEntry, 0, len(audio))
	for _, entry := range audio {
		ordered = append(ordered, entry)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].SpaceID+ordered[i].Path < ordered[j].SpaceID+ordered[j].Path })

	var toRead []files.TreeEntry
	var moved []row
	for _, entry := range ordered {
		dir := dirOf(entry.Path)
		folderCover := coverOf(entry.SpaceID, dir)
		lyricsFile := lyrics[entry.SpaceID+"\x00"+dir+"\x00"+stem(entry.Name)]
		old, ok := known[entry.ID]
		if !ok || old.SizeBytes != entry.SizeBytes || !old.ModifiedAt.Equal(entry.ModifiedAt.UTC()) {
			toRead = append(toRead, entry)
			continue
		}
		if old.Dir != dir || old.Name != entry.Name || old.FolderCover != folderCover || old.LyricsFile != lyricsFile {
			old.Dir, old.Name, old.FolderCover, old.LyricsFile = dir, entry.Name, folderCover, lyricsFile
			moved = append(moved, old)
		}
	}
	if err := s.store.write(ctx, moved); err != nil {
		return summary, err
	}
	if len(removed) > 0 || len(moved) > 0 {
		s.version.Add(1)
	}

	job.progress(PhaseReading, 0, len(toRead))
	var batch []row
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := s.store.write(ctx, batch); err != nil {
			return err
		}
		batch = batch[:0]
		s.version.Add(1)
		return nil
	}
	for _, entry := range toRead {
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		dir := dirOf(entry.Path)
		value, err := s.readTrack(ctx, actor, entry)
		job.step()
		if errors.Is(err, files.ErrVolumeUnavailable) {
			return summary, err
		}
		if err != nil {
			// Unreadable now, for example replaced mid-scan; the next scan retries.
			s.logger.DebugContext(ctx, "music file skipped", "file_id", entry.ID, "error", err)
			continue
		}
		value.FolderCover = coverOf(entry.SpaceID, dir)
		value.LyricsFile = lyrics[entry.SpaceID+"\x00"+dir+"\x00"+stem(entry.Name)]
		batch = append(batch, value)
		summary.read++
		if len(batch) >= batchSize {
			if err := flush(); err != nil {
				return summary, err
			}
		}
	}
	if err := flush(); err != nil {
		return summary, err
	}

	pending, err := s.store.pendingIn(ctx, scopes)
	if err != nil {
		return summary, err
	}
	job.progress(PhaseMeasuring, 0, len(pending))
	for _, value := range pending {
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		value.DurationMs, value.DurationState = 0, "failed"
		content, err := s.files.OpenContent(ctx, actor, value.ID)
		if errors.Is(err, files.ErrVolumeUnavailable) {
			return summary, err
		}
		if err != nil {
			job.step()
			continue
		}
		if duration, err := readDuration(content.Reader, value.Format); err == nil && duration > 0 {
			value.DurationMs, value.DurationState = duration.Milliseconds(), "done"
		} else if s.worker != nil {
			// Opus and raw AAC have no Go reader for their length.
			if probe, err := s.worker.Probe(ctx, content.Reader); err == nil && probe.Format.Duration > 0 {
				value.DurationMs, value.DurationState = int64(probe.Format.Duration*1000), "done"
			}
		}
		_ = content.Reader.Close()
		job.step()
		batch = append(batch, value)
		summary.measured++
		if len(batch) >= batchSize {
			if err := flush(); err != nil {
				return summary, err
			}
		}
	}
	return summary, flush()
}

// readTrack reads one audio file's tags, cover and, where cheap, duration.
// Formats the Go readers do not cover, and ALAC, also go through the media
// worker's probe, which runs FFmpeg in its sandbox.
func (s *Service) readTrack(ctx context.Context, actor accounts.User, entry files.TreeEntry) (row, error) {
	content, err := s.files.OpenContent(ctx, actor, entry.ID)
	if err != nil {
		return row{}, err
	}
	defer content.Reader.Close()
	format := formatOf(entry.Name)
	var tags metadata
	if tagFormats[format] {
		tags = readTags(content.Reader, format)
	}
	var probed audioFormat
	if s.worker != nil && (!tagFormats[format] || !playsInBrowser(format, tags.Codec)) {
		probe, err := s.worker.Probe(ctx, content.Reader)
		if errors.Is(err, mediaworker.ErrUnavailable) {
			// Skip the file rather than index it without its tags; the next
			// scan reads it again.
			return row{}, err
		}
		if err == nil {
			probed = fromProbe(&tags, probe)
		}
	}
	fillFromName(&tags, entry.Name)
	value := row{
		ID: entry.ID, SpaceID: entry.SpaceID, Dir: dirOf(entry.Path), Name: entry.Name, SizeBytes: entry.SizeBytes,
		ModifiedAt: entry.ModifiedAt.UTC(), Format: format, Codec: tags.Codec, Title: tags.Title, Artist: tags.Artist,
		Album: tags.Album, AlbumArtist: tags.AlbumArtist, Genre: tags.Genre, Year: tags.Year, TrackNo: tags.TrackNo,
		DiscNo: tags.DiscNo, DurationState: "pending", EmbeddedLyrics: tags.Lyrics, AddedAt: s.now().UTC(),
		SampleRate: probed.SampleRate, BitDepth: probed.Bits,
	}
	if len(tags.Picture) == 0 && probed.Cover {
		// The worker renders the attached picture as a JPEG.
		if picture, err := s.worker.Frame(ctx, content.Reader, 0, coverPixels[CoverLarge]); err == nil {
			tags.Picture = picture
		}
	}
	if len(tags.Picture) > 0 {
		if cover, err := s.covers.Put(tags.Picture); err == nil {
			value.EmbeddedCover = cover
		}
	}
	if probed.Duration > 0 {
		value.DurationMs, value.DurationState = probed.Duration.Milliseconds(), "done"
	} else if !browserFormats[format] {
		// Without a probe nothing else can measure these formats.
		value.DurationState = "failed"
	} else if quickDuration[format] {
		value.DurationState = "failed"
		if duration, err := readDuration(content.Reader, format); err == nil && duration > 0 {
			value.DurationMs, value.DurationState = duration.Milliseconds(), "done"
		}
	}
	return value, nil
}

// folderCover returns the cover ID of a folder image, reading the image only
// when it is new or changed.
func (s *Service) folderCover(ctx context.Context, actor accounts.User, image files.TreeEntry) (string, error) {
	var size int64
	var modified, cover string
	err := s.store.db.QueryRowContext(ctx, "SELECT size_bytes, modified_at, cover FROM folder_images WHERE entry_id = ?", image.ID).Scan(&size, &modified, &cover)
	if err == nil && size == image.SizeBytes && modified == formatTime(image.ModifiedAt) {
		if _, err := s.covers.Path(cover, CoverSmall); err == nil {
			return cover, nil
		}
	} else if err != nil && !errors.Is(err, errNoRows) {
		return "", err
	}
	if image.SizeBytes > maxCoverBytes {
		return "", errors.New("folder image is too large")
	}
	content, err := s.files.OpenContent(ctx, actor, image.ID)
	if err != nil {
		return "", err
	}
	raw, err := io.ReadAll(io.LimitReader(content.Reader, maxCoverBytes+1))
	_ = content.Reader.Close()
	if err != nil {
		return "", err
	}
	if cover, err = s.covers.Put(raw); err != nil {
		return "", err
	}
	_, err = s.store.db.ExecContext(ctx, `INSERT INTO folder_images(entry_id, size_bytes, modified_at, cover) VALUES(?,?,?,?)
ON CONFLICT(entry_id) DO UPDATE SET size_bytes = excluded.size_bytes, modified_at = excluded.modified_at, cover = excluded.cover`,
		image.ID, image.SizeBytes, formatTime(image.ModifiedAt), cover)
	return cover, err
}
