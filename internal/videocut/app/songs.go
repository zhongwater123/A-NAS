package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/atomicfile"
	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
	"github.com/zhongwater123/A-NAS/internal/videocut/domain"
	"github.com/zhongwater123/A-NAS/internal/videocutcompute"
)

// A member picks a song for a project from the music library while the
// session is there (docs/specs/montage-center.md, 配乐): the Product Service
// opens the file as the member, the way the music library plays it, and
// streams it here. The montage center keeps its own copy, named by its
// content, so that the member may change or delete the original afterwards;
// the copy goes once no project uses it. allin1 finds the song's beats,
// downbeats and sections in the background (MusicAnalyzer), and the compute
// Worker's music operation picks the excerpt and its slots from them
// (MusicComputer).

// Limits of a song (docs/specs/montage-center.md, 本稿补充的默认规则).
const (
	MaxSongBytes   = 100 << 20
	MinSongSeconds = 30.0
	MaxSongSeconds = 8 * 60.0
	// SongTarget is how long the film aims to be, as in the eighth version
	// (domain.options.Case.target); the excerpt follows the music from it.
	SongTarget = 90.0
	// maxTitleRunes bounds a song's title as the music library reports it.
	maxTitleRunes = 200
)

// Where the copies live under the store's root: songs/<sha256>/audio, and
// files still arriving in songs/.incoming.
const (
	songsDir    = "songs"
	incomingDir = ".incoming"
	songFile    = "audio"
)

// Song states.
const (
	SongAnalyzing = "analyzing"
	SongReady     = "ready"
	SongUnusable  = "unusable"
)

// Why a song cannot be used (docs/specs/montage-center.md, 配乐). The
// compute Worker's music operation reports the last three.
const (
	// UnusableUndecodable: FFmpeg cannot decode it, or allin1 refuses the
	// samples.
	UnusableUndecodable = "undecodable"
	// UnusableFewBars: fewer than 16 bars.
	UnusableFewBars = "few_bars"
	// UnusableNoExcerpt: no excerpt of 30 to 150 seconds around the target.
	UnusableNoExcerpt = "no_excerpt"
	// UnusableLongSlot: a slot longer than the 12 seconds a segment may
	// last.
	UnusableLongSlot = "long_slot"
	// UnusableAnalysisFailed: the analysis failed analysisAttempts times,
	// or the compute Worker refuses what allin1 found.
	UnusableAnalysisFailed = "analysis_failed"
)

// Why an analysis waits.
const (
	WaitAnalyzer = "analyzer_unavailable"
	WaitCompute  = "compute_unavailable"
	WaitMedia    = "media_unavailable"
)

// songAnalysis is what a song row keeps of its analysis.
type songAnalysis struct {
	Structure *videocutcompute.Structure `json:"structure,omitempty"`
	Seconds   float64                    `json:"seconds,omitempty"`
	Selection *videocutcompute.Selection `json:"selection,omitempty"`
	// Music is the default excerpt as the domain reads it (Selection.Music).
	Music json.RawMessage `json:"music,omitempty"`
}

type songRow struct {
	id, owner, sha, title, contentType, state, reason, waiting, version string
	duration                                                            float64
	analysis                                                            songAnalysis
	loudness                                                            []byte
}

// keptSelection is what a project keeps of the excerpt from the start its
// member dragged it to (projects.song_selection): the selection the page
// shows, and the excerpt as the domain reads it, which the project's jobs
// cut the film to without asking the compute Worker again.
type keptSelection struct {
	videocutcompute.Selection
	Music json.RawMessage `json:"music,omitempty"`
}

// SongTitle is a song's title as the montage center keeps it: trimmed, and
// at most maxTitleRunes long. The Product Service cuts a title the same way
// before it sends it (protocol.CopySong), so that a long one fits the
// header it travels in.
func SongTitle(title string) string {
	title = strings.TrimSpace(title)
	if runes := []rune(title); len(runes) > maxTitleRunes {
		title = strings.TrimSpace(string(runes[:maxTitleRunes]))
	}
	return title
}

// defaultDownbeat is the downbeat that stands for the default excerpt's
// start. That start is a section's bound kept to two decimals, and need not
// be a downbeat itself; a start the member drags always is one.
func defaultDownbeat(st *videocutcompute.Structure, selection *videocutcompute.Selection) float64 {
	return nearest(st.Downbeats, selection.Excerpt[0])
}

func (s *Store) songsRoot() string { return filepath.Join(s.root, songsDir) }

// songPath is the copy of the song with this digest.
func (s *Store) songPath(sha string) string { return filepath.Join(s.songsRoot(), sha, songFile) }

func validDigest(sha string) bool {
	if len(sha) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(sha)
	return err == nil && strings.ToLower(sha) == sha
}

// receive writes body into songs/.incoming, at most MaxSongBytes, and
// returns the file, open read-only for FFmpeg (ADR 0017), with its digest.
func (s *Store) receive(body io.Reader) (*os.File, string, error) {
	dir := filepath.Join(s.songsRoot(), incomingDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", err
	}
	file, err := os.CreateTemp(dir, "song-*")
	if err != nil {
		return nil, "", err
	}
	discard := func(err error) (*os.File, string, error) {
		return nil, "", errors.Join(err, file.Close(), os.Remove(file.Name()))
	}
	digest := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, digest), io.LimitReader(body, MaxSongBytes+1))
	switch {
	case err != nil:
		return discard(err)
	case written > MaxSongBytes:
		return discard(domain.ErrSongTooLarge)
	case written == 0:
		return discard(domain.ErrNotAudio)
	}
	if err := file.Sync(); err != nil {
		return discard(err)
	}
	readOnly, err := os.Open(file.Name())
	if err != nil {
		return discard(err)
	}
	_ = file.Close()
	return readOnly, hex.EncodeToString(digest.Sum(nil)), nil
}

// probeSong checks that file is audio of a length the montage center takes,
// and returns its length.
func (s *Store) probeSong(ctx context.Context, file *os.File) (float64, error) {
	if s.media == nil {
		return 0, domain.ErrMediaUnavailable
	}
	probe, err := s.media.Probe(ctx, file)
	switch {
	case errors.Is(err, ffmpegworker.ErrUnreadable), errors.Is(err, ffmpegworker.ErrInvalid), errors.Is(err, ffmpegworker.ErrTooLarge):
		return 0, domain.ErrNotAudio
	case err != nil:
		return 0, fmt.Errorf("%w: %v", domain.ErrMediaUnavailable, err)
	}
	if !slices.ContainsFunc(probe.Streams, func(stream ffmpegworker.ProbeStream) bool { return stream.Type == "audio" }) {
		return 0, domain.ErrNotAudio
	}
	duration := probe.Format.Duration
	if math.IsNaN(duration) || duration < MinSongSeconds || duration > MaxSongSeconds {
		return 0, domain.ErrSongLength
	}
	return duration, nil
}

// keep moves the received file to songs/<sha256>/audio; a copy that is there
// already stays, and the received one goes.
func (s *Store) keep(file *os.File, sha string) error {
	target := s.songPath(sha)
	if _, err := os.Stat(target); err == nil {
		return os.Remove(file.Name())
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), target); err != nil {
		return err
	}
	return atomicfile.SyncDir(filepath.Dir(target))
}

// PutSong copies the song body streams into the montage center for owner's
// project and starts analysing it. The project then uses it from its
// excerpt's default start; a song it used before goes unless another of
// owner's projects uses it.
func (s *Store) PutSong(ctx context.Context, owner, projectID string, source domain.SongSource, body io.Reader) (domain.SongDetail, error) {
	row, err := s.project(ctx, s.db, owner, projectID)
	if err != nil {
		return domain.SongDetail{}, err
	}
	if row.state != "ready" {
		return domain.SongDetail{}, domain.ErrNotFound
	}
	if !utf8.ValidString(source.Title) || strings.ContainsRune(source.Title, 0) {
		return domain.SongDetail{}, domain.ErrInvalid
	}
	title := SongTitle(source.Title)
	contentType := source.ContentType
	if contentType != "" && !strings.HasPrefix(contentType, "audio/") {
		return domain.SongDetail{}, domain.ErrInvalid
	}
	file, sha, err := s.receive(body)
	if err != nil {
		return domain.SongDetail{}, err
	}
	defer func() { _ = file.Close() }()
	duration, err := s.probeSong(ctx, file)
	if err != nil {
		return domain.SongDetail{}, errors.Join(err, os.Remove(file.Name()))
	}
	s.songs.Lock()
	defer s.songs.Unlock()
	if err := s.keep(file, sha); err != nil {
		return domain.SongDetail{}, errors.Join(err, os.Remove(file.Name()))
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		songID, err := s.songRowFor(ctx, tx, owner, sha, title, contentType, duration)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE projects SET song_id = ?, song_track = NULLIF(?, ''), song_start = NULL, song_selection = NULL, updated_at = ?
			WHERE id = ? AND owner_user_id = ?`, songID, source.TrackID, sqlitedb.FormatTime(s.now()), projectID, owner); err != nil {
			return err
		}
		return forgetUnusedSongs(ctx, tx, owner)
	})
	if err != nil {
		return domain.SongDetail{}, err
	}
	s.removeUnusedCopies(ctx)
	s.wakeAnalysis()
	return s.Song(ctx, owner, projectID)
}

// songRowFor finds or adds owner's row for the song with this digest. A new
// row takes the analysis another member's row has finished for the same
// content.
func (s *Store) songRowFor(ctx context.Context, tx *sql.Tx, owner, sha, title, contentType string, duration float64) (string, error) {
	now := sqlitedb.FormatTime(s.now())
	var id string
	err := tx.QueryRowContext(ctx, "SELECT id FROM songs WHERE owner_user_id = ? AND sha256 = ?", owner, sha).Scan(&id)
	if err == nil {
		_, err = tx.ExecContext(ctx, "UPDATE songs SET title = ?, updated_at = ? WHERE id = ?", title, now, id)
		return id, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	id = newID("song")
	if _, err := tx.ExecContext(ctx, `INSERT INTO songs(id, owner_user_id, sha256, title, duration, content_type, state, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, 'analyzing', ?, ?)`, id, owner, sha, title, duration, contentType, now, now); err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `UPDATE songs SET (state, reason, analysis_version, analysis_json, loudness) =
		(SELECT state, reason, analysis_version, analysis_json, loudness FROM songs o
		 WHERE o.sha256 = songs.sha256 AND o.id != songs.id AND o.state != 'analyzing' ORDER BY o.updated_at DESC LIMIT 1)
		WHERE id = ? AND EXISTS (SELECT 1 FROM songs o WHERE o.sha256 = songs.sha256 AND o.id != songs.id AND o.state != 'analyzing')`, id)
	return id, err
}

// A copy is needed by the current project, by the settings captured by an
// unfinished job, even after the project has changed its song, and by any
// version's job: editing a version renders and previews it with the song it
// was made with (#596).
const unusedSongs = `NOT EXISTS (SELECT 1 FROM projects WHERE song_id = songs.id)
	AND NOT EXISTS (SELECT 1 FROM jobs WHERE owner_user_id = songs.owner_user_id AND ` + activeStateQuery + `
		AND json_extract(CASE WHEN json_valid(settings_json) THEN settings_json END, '$.songId') = songs.id)
	AND NOT EXISTS (SELECT 1 FROM versions v JOIN jobs j ON j.id = v.job_id WHERE j.owner_user_id = songs.owner_user_id
		AND json_extract(CASE WHEN json_valid(j.settings_json) THEN j.settings_json END, '$.songId') = songs.id)`

func forgetUnusedSongs(ctx context.Context, q queryer, owner string) error {
	_, err := q.ExecContext(ctx, "DELETE FROM songs WHERE owner_user_id = ? AND "+unusedSongs, owner)
	return err
}

// releaseSong removes a finished job's song when its last reference goes.
func (s *Store) releaseSong(ctx context.Context, owner string) {
	s.songs.Lock()
	defer s.songs.Unlock()
	if err := forgetUnusedSongs(ctx, s.db, owner); err != nil {
		s.logger.WarnContext(ctx, "releasing a finished job's song failed", "error", err)
		return
	}
	s.removeUnusedCopies(ctx)
}

// removeUnusedCopies deletes the copies no song row names; the caller holds
// s.songs. A failure is logged and tried again when the service next starts
// or another song goes.
func (s *Store) removeUnusedCopies(ctx context.Context) {
	entries, err := os.ReadDir(s.songsRoot())
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.logger.WarnContext(ctx, "listing song copies failed", "error", err)
		}
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == incomingDir {
			continue
		}
		var used bool
		if validDigest(name) {
			if err := s.db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM songs WHERE sha256 = ?)", name).Scan(&used); err != nil {
				s.logger.WarnContext(ctx, "checking a song copy failed", "song", name, "error", err)
				continue
			}
		}
		if !used {
			if err := os.RemoveAll(filepath.Join(s.songsRoot(), name)); err != nil {
				s.logger.WarnContext(ctx, "removing a song copy failed", "song", name, "error", err)
			}
		}
	}
}

// ReclaimSongs forgets the songs no project uses and deletes their copies,
// with what was still arriving when the service stopped. The service calls
// it when it starts.
func (s *Store) ReclaimSongs(ctx context.Context) error {
	s.songs.Lock()
	defer s.songs.Unlock()
	if _, err := s.db.ExecContext(ctx, "DELETE FROM songs WHERE "+unusedSongs); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(s.songsRoot(), incomingDir)); err != nil {
		return err
	}
	s.removeUnusedCopies(ctx)
	return nil
}

// projectSong is a project's song and the start the member chose.
type projectSong struct {
	songID    sql.NullString
	trackID   sql.NullString
	start     sql.NullFloat64
	selection sql.NullString
}

func (s *Store) projectSong(ctx context.Context, q queryer, owner, projectID string) (projectSong, error) {
	var song projectSong
	var state string
	err := q.QueryRowContext(ctx, `SELECT state, song_id, song_track, song_start, song_selection FROM projects WHERE id = ? AND owner_user_id = ?`,
		projectID, owner).Scan(&state, &song.songID, &song.trackID, &song.start, &song.selection)
	if errors.Is(err, sql.ErrNoRows) || err == nil && state != "ready" {
		return projectSong{}, domain.ErrNotFound
	}
	if err != nil {
		return projectSong{}, err
	}
	if !song.songID.Valid {
		return projectSong{}, domain.ErrNoSong
	}
	return song, nil
}

func (s *Store) song(ctx context.Context, q queryer, id string) (songRow, error) {
	var row songRow
	var analysis sql.NullString
	err := q.QueryRowContext(ctx, `SELECT id, owner_user_id, sha256, title, content_type, state, reason, waiting, analysis_version,
		duration, analysis_json, loudness FROM songs WHERE id = ?`, id).
		Scan(&row.id, &row.owner, &row.sha, &row.title, &row.contentType, &row.state, &row.reason, &row.waiting, &row.version,
			&row.duration, &analysis, &row.loudness)
	if errors.Is(err, sql.ErrNoRows) {
		return songRow{}, domain.ErrNoSong
	}
	if err != nil {
		return songRow{}, err
	}
	if analysis.Valid {
		if err := json.Unmarshal([]byte(analysis.String), &row.analysis); err != nil {
			return songRow{}, fmt.Errorf("song %s: %w", id, err)
		}
	}
	return row, nil
}

// Song returns owner's project's song: how its analysis stands, its
// structure, and the excerpt the project uses.
func (s *Store) Song(ctx context.Context, owner, projectID string) (domain.SongDetail, error) {
	chosen, err := s.projectSong(ctx, s.db, owner, projectID)
	if err != nil {
		return domain.SongDetail{}, err
	}
	row, err := s.song(ctx, s.db, chosen.songID.String)
	if err != nil {
		return domain.SongDetail{}, err
	}
	detail := domain.SongDetail{ID: row.id, TrackID: chosen.trackID.String, Title: row.title, Duration: row.duration, Transcode: row.contentType == "",
		State: row.state, Reason: row.reason, Waiting: row.waiting}
	if st := row.analysis.Structure; st != nil {
		detail.Tempo, detail.Beats, detail.Downbeats, detail.Sections = st.BPM, st.Beats, st.Downbeats, st.Segments
	}
	if selection := row.analysis.Selection; selection != nil && row.state == SongReady {
		start := selection.Excerpt[0]
		detail.DefaultStart, detail.Selection = &start, selection
		if chosen.selection.Valid {
			var own keptSelection
			if err := json.Unmarshal([]byte(chosen.selection.String), &own); err != nil {
				return domain.SongDetail{}, err
			}
			detail.Selection = &own.Selection
		}
	}
	return detail, nil
}

// SongStart moves owner's project's excerpt to start on the downbeat nearest
// at, and returns the song with the excerpt from there. The film follows the
// excerpt: it starts there and lasts as long. On the default start's
// downbeat the project follows the default excerpt again, as if its member
// had never dragged the start.
func (s *Store) SongStart(ctx context.Context, owner, projectID string, at float64) (domain.SongDetail, error) {
	chosen, err := s.projectSong(ctx, s.db, owner, projectID)
	if err != nil {
		return domain.SongDetail{}, err
	}
	row, err := s.song(ctx, s.db, chosen.songID.String)
	if err != nil {
		return domain.SongDetail{}, err
	}
	st := row.analysis.Structure
	if row.state != SongReady || st == nil || len(st.Downbeats) == 0 {
		return domain.SongDetail{}, domain.ErrSongNotReady
	}
	if math.IsNaN(at) || math.IsInf(at, 0) || at < 0 || at > row.duration {
		return domain.SongDetail{}, domain.ErrInvalid
	}
	start := nearest(st.Downbeats, at)
	// What the project keeps: nothing on the default start, where the
	// eighth version's rule picks the excerpt, which ends on a section's
	// bound and not a fixed length after the start.
	var keptStart, kept any
	if def := row.analysis.Selection; def == nil || start != defaultDownbeat(st, def) {
		if s.compute == nil {
			return domain.SongDetail{}, fmt.Errorf("%w: no compute worker", videocutcompute.ErrWorkerUnavailable)
		}
		loudness, err := domain.DecodeLoudness(row.loudness)
		if err != nil {
			return domain.SongDetail{}, err
		}
		selection, err := s.compute.Music(ctx, videocutcompute.MusicRequest{Structure: *st, Loudness: loudness, Target: SongTarget, Start: &start})
		if errors.Is(err, videocutcompute.ErrWorkerRefused) {
			// The Worker takes the song but no excerpt from there.
			return domain.SongDetail{}, fmt.Errorf("%w: %w", domain.ErrSongStart, err)
		}
		if err != nil {
			return domain.SongDetail{}, err
		}
		if selection.Unusable != "" {
			return domain.SongDetail{}, fmt.Errorf("%w: %s", domain.ErrSongStart, selection.Unusable)
		}
		encoded, err := json.Marshal(keptSelection{Selection: selection, Music: selection.Music})
		if err != nil {
			return domain.SongDetail{}, err
		}
		keptStart, kept = start, string(encoded)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE projects SET song_start = ?, song_selection = ?, updated_at = ?
		WHERE id = ? AND owner_user_id = ? AND song_id = ?`, keptStart, kept, sqlitedb.FormatTime(s.now()), projectID, owner, row.id)
	if err != nil {
		return domain.SongDetail{}, err
	}
	if changed, err := result.RowsAffected(); err != nil || changed == 0 {
		// The member chose another song meanwhile.
		return domain.SongDetail{}, errors.Join(domain.ErrSongNotReady, err)
	}
	return s.Song(ctx, owner, projectID)
}

// nearest is the time in times closest to at; times is sorted.
func nearest(times []float64, at float64) float64 {
	i, _ := slices.BinarySearch(times, at)
	switch {
	case i == 0:
		return times[0]
	case i == len(times):
		return times[len(times)-1]
	case at-times[i-1] <= times[i]-at:
		return times[i-1]
	}
	return times[i]
}

// OpenSongAudio opens owner's project's song for the member to hear.
func (s *Store) OpenSongAudio(ctx context.Context, owner, projectID string) (domain.SongAudio, error) {
	chosen, err := s.projectSong(ctx, s.db, owner, projectID)
	if err != nil {
		return domain.SongAudio{}, err
	}
	return s.openSong(ctx, chosen.songID.String)
}

// DecodeSong streams audio, a song OpenSongAudio opened, as FLAC from start
// seconds, for a copy browsers cannot play. The media worker receives a
// descriptor of its own; audio.File stays the caller's to close.
func (s *Store) DecodeSong(ctx context.Context, audio domain.SongAudio, start float64) (io.ReadCloser, error) {
	if math.IsNaN(start) || start < 0 || start > audio.Song.Duration {
		return nil, domain.ErrInvalid
	}
	if s.media == nil {
		return nil, domain.ErrMediaUnavailable
	}
	stream, err := s.media.Audio(ctx, audio.File, ffmpegworker.AudioRequest{Start: start, Bits: 16})
	switch {
	case errors.Is(err, ffmpegworker.ErrUnreadable), errors.Is(err, ffmpegworker.ErrInvalid):
		return nil, fmt.Errorf("%w: %v", domain.ErrNotAudio, err)
	case err != nil:
		return nil, fmt.Errorf("%w: %v", domain.ErrMediaUnavailable, err)
	}
	return stream, nil
}
