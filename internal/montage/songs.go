package montage

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
	"time"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/atomicfile"
	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
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

var (
	// ErrSongTooLarge: the file is larger than MaxSongBytes (413).
	ErrSongTooLarge = errors.New("the song is larger than 100 MiB")
	// ErrSongLength: the song is shorter than MinSongSeconds or longer than
	// MaxSongSeconds (422).
	ErrSongLength = errors.New("the song is shorter than 30 seconds or longer than 8 minutes")
	// ErrNotAudio: FFmpeg finds no audio in the file (422).
	ErrNotAudio = errors.New("the file is not audio")
	// ErrNoSong: the project has no song yet (404).
	ErrNoSong = errors.New("the project has no song")
	// ErrSongNotReady: the song is still being analysed, or cannot be used
	// (409).
	ErrSongNotReady = errors.New("the song is not analysed")
	// ErrSongStart: no usable excerpt starts at the downbeat asked for (422).
	ErrSongStart = errors.New("no usable excerpt starts there")
	// ErrMediaUnavailable: the media worker did not answer (503).
	ErrMediaUnavailable = errors.New("the media worker is not available")
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

// SongSource is what the Product Service tells of the track it streams.
type SongSource struct {
	TrackID string `json:"trackId"`
	Title   string `json:"title"`
	// ContentType is how browsers receive the file; empty when they cannot
	// play it and the media worker decodes it to FLAC (music.Track.Transcode).
	ContentType string `json:"contentType"`
}

// Media is what the montage center asks of the FFmpeg worker for its songs;
// ffmpegworker.Client provides it.
type Media interface {
	Probe(ctx context.Context, file *os.File) (ffmpegworker.Probe, error)
	Audio(ctx context.Context, file *os.File, request ffmpegworker.AudioRequest) (io.ReadCloser, error)
}

// Section is a part of a song as allin1 labels it.
type Section struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Label string  `json:"label"`
}

// Structure is allin1's result for a song, as the eighth version keeps it
// (structure-<song>.json), with the model that found it.
type Structure struct {
	Model     string    `json:"model"`
	BPM       float64   `json:"bpm"`
	Beats     []float64 `json:"beats"`
	Downbeats []float64 `json:"downbeats"`
	Segments  []Section `json:"segments"`
}

// Analysis is what the music analysis Worker answers for a song: its
// structure and its loudness, the eighth version's RMS curve (frames of
// 2048 samples every 512 at 22050 Hz).
type Analysis struct {
	Structure Structure
	Loudness  []float32
	// Seconds is how long the Worker took.
	Seconds float64
}

// MusicAnalyzer finds a song's structure in its samples: stereo, 32-bit
// float at 44.1 kHz for allin1, and mono at 22050 Hz for the loudness. Both
// arrive as streams; the analyzer reads them in that order while they are
// written. whole yields once, when the writing ended: nil when both streams
// carry the whole song. The analyzer analyses nothing before that, and
// returns any other error whole yields as it is.
type MusicAnalyzer interface {
	Analyze(ctx context.Context, stereo, mono *os.File, whole <-chan error) (Analysis, error)
}

// Part is a section of an excerpt: its level in the film, and the slots the
// shots fill, in seconds from the excerpt's start.
type Part struct {
	Start  float64      `json:"start"`
	End    float64      `json:"end"`
	Level  string       `json:"level"`
	Label  string       `json:"label"`
	Allin1 string       `json:"allin1,omitempty"`
	Slots  [][2]float64 `json:"slots"`
}

// Selection is the compute Worker's music operation: the eighth version's
// excerpt and slots (domain.music.from_structure), with whether a chorus was
// found and why the song cannot be used.
type Selection struct {
	Tempo   float64    `json:"tempo"`
	Bar     float64    `json:"bar"`
	Excerpt [2]float64 `json:"excerpt"`
	Seconds float64    `json:"seconds"`
	Parts   []Part     `json:"parts"`
	// Chorus is false when allin1 found no chorus and the loudest section
	// stood in for it: the member is asked to listen.
	Chorus   bool   `json:"chorus"`
	Unusable string `json:"unusable,omitempty"`
	// Music is the excerpt as the compute Worker's domain reads it
	// (domain.music.from_structure's dict), which jobs cut the film to.
	Music json.RawMessage `json:"-"`
}

// MusicRequest asks for the excerpt of a song: from the eighth version's
// rule, or from Start, a downbeat the member dragged it to.
type MusicRequest struct {
	// Song names the song in the plan's music note.
	Song      string
	Structure Structure
	Loudness  []float32
	Target    float64
	Start     *float64
}

// MusicComputer is the compute Worker's music operation.
type MusicComputer interface {
	Music(ctx context.Context, request MusicRequest) (Selection, error)
}

// ErrWorkerUnavailable reports that a Worker could not be reached or could
// not serve; the montage center tries again later.
var ErrWorkerUnavailable = errors.New("the worker is not available")

// ErrWorkerFailed reports that a Worker took the request and could not
// finish it: it reported an error of its own, stopped answering or ran out
// of time. The same request may work later, but every attempt costs what the
// work costs, so the montage center counts them.
var ErrWorkerFailed = errors.New("the worker failed")

// ErrWorkerRefused reports that a Worker refused the input; the same input
// would fail again.
var ErrWorkerRefused = errors.New("the worker refused the input")

// SongDetail is a project's song as the music page shows it.
type SongDetail struct {
	ID string `json:"id"`
	// TrackID is the track of the music library the song was copied from.
	TrackID  string  `json:"trackId,omitempty"`
	Title    string  `json:"title"`
	Duration float64 `json:"duration"`
	// Transcode: browsers cannot play the copy; /song/audio decodes it to
	// FLAC from ?start= and cannot seek by Range.
	Transcode bool      `json:"transcode"`
	State     string    `json:"state"`
	Reason    string    `json:"reason,omitempty"`
	Waiting   string    `json:"waiting,omitempty"`
	Tempo     float64   `json:"tempo,omitempty"`
	Beats     []float64 `json:"beats,omitempty"`
	Downbeats []float64 `json:"downbeats,omitempty"`
	Sections  []Section `json:"sections,omitempty"`
	// DefaultStart is where the eighth version's rule starts the excerpt;
	// Selection follows the project's own start when the member dragged it.
	DefaultStart *float64   `json:"defaultStart,omitempty"`
	Selection    *Selection `json:"selection,omitempty"`
}

// songAnalysis is what a song row keeps of its analysis.
type songAnalysis struct {
	Structure *Structure `json:"structure,omitempty"`
	Seconds   float64    `json:"seconds,omitempty"`
	Selection *Selection `json:"selection,omitempty"`
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
	Selection
	Music json.RawMessage `json:"music,omitempty"`
}

// SongTitle is a song's title as the montage center keeps it: trimmed, and
// at most maxTitleRunes long. The Product Service cuts a title the same way
// before it sends it (montageapi.CopySong), so that a long one fits the
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
func defaultDownbeat(st *Structure, selection *Selection) float64 {
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
		return discard(ErrSongTooLarge)
	case written == 0:
		return discard(ErrNotAudio)
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
		return 0, ErrMediaUnavailable
	}
	probe, err := s.media.Probe(ctx, file)
	switch {
	case errors.Is(err, ffmpegworker.ErrUnreadable), errors.Is(err, ffmpegworker.ErrInvalid), errors.Is(err, ffmpegworker.ErrTooLarge):
		return 0, ErrNotAudio
	case err != nil:
		return 0, fmt.Errorf("%w: %v", ErrMediaUnavailable, err)
	}
	if !slices.ContainsFunc(probe.Streams, func(stream ffmpegworker.ProbeStream) bool { return stream.Type == "audio" }) {
		return 0, ErrNotAudio
	}
	duration := probe.Format.Duration
	if math.IsNaN(duration) || duration < MinSongSeconds || duration > MaxSongSeconds {
		return 0, ErrSongLength
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
func (s *Store) PutSong(ctx context.Context, owner, projectID string, source SongSource, body io.Reader) (SongDetail, error) {
	row, err := s.project(ctx, s.db, owner, projectID)
	if err != nil {
		return SongDetail{}, err
	}
	if row.state != "ready" {
		return SongDetail{}, ErrNotFound
	}
	if !utf8.ValidString(source.Title) || strings.ContainsRune(source.Title, 0) {
		return SongDetail{}, ErrInvalid
	}
	title := SongTitle(source.Title)
	contentType := source.ContentType
	if contentType != "" && !strings.HasPrefix(contentType, "audio/") {
		return SongDetail{}, ErrInvalid
	}
	file, sha, err := s.receive(body)
	if err != nil {
		return SongDetail{}, err
	}
	defer func() { _ = file.Close() }()
	duration, err := s.probeSong(ctx, file)
	if err != nil {
		return SongDetail{}, errors.Join(err, os.Remove(file.Name()))
	}
	s.songs.Lock()
	defer s.songs.Unlock()
	if err := s.keep(file, sha); err != nil {
		return SongDetail{}, errors.Join(err, os.Remove(file.Name()))
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
		return SongDetail{}, err
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

// A copy is needed by the current project or by the settings captured
// by an unfinished job, even after the project has changed its song.
const unusedSongs = `NOT EXISTS (SELECT 1 FROM projects WHERE song_id = songs.id)
	AND NOT EXISTS (SELECT 1 FROM jobs WHERE owner_user_id = songs.owner_user_id AND ` + activeStateQuery + `
		AND json_extract(CASE WHEN json_valid(settings_json) THEN settings_json END, '$.songId') = songs.id)`

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
		return projectSong{}, ErrNotFound
	}
	if err != nil {
		return projectSong{}, err
	}
	if !song.songID.Valid {
		return projectSong{}, ErrNoSong
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
		return songRow{}, ErrNoSong
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
func (s *Store) Song(ctx context.Context, owner, projectID string) (SongDetail, error) {
	chosen, err := s.projectSong(ctx, s.db, owner, projectID)
	if err != nil {
		return SongDetail{}, err
	}
	row, err := s.song(ctx, s.db, chosen.songID.String)
	if err != nil {
		return SongDetail{}, err
	}
	detail := SongDetail{ID: row.id, TrackID: chosen.trackID.String, Title: row.title, Duration: row.duration, Transcode: row.contentType == "",
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
				return SongDetail{}, err
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
func (s *Store) SongStart(ctx context.Context, owner, projectID string, at float64) (SongDetail, error) {
	chosen, err := s.projectSong(ctx, s.db, owner, projectID)
	if err != nil {
		return SongDetail{}, err
	}
	row, err := s.song(ctx, s.db, chosen.songID.String)
	if err != nil {
		return SongDetail{}, err
	}
	st := row.analysis.Structure
	if row.state != SongReady || st == nil || len(st.Downbeats) == 0 {
		return SongDetail{}, ErrSongNotReady
	}
	if math.IsNaN(at) || math.IsInf(at, 0) || at < 0 || at > row.duration {
		return SongDetail{}, ErrInvalid
	}
	start := nearest(st.Downbeats, at)
	// What the project keeps: nothing on the default start, where the
	// eighth version's rule picks the excerpt, which ends on a section's
	// bound and not a fixed length after the start.
	var keptStart, kept any
	if def := row.analysis.Selection; def == nil || start != defaultDownbeat(st, def) {
		if s.compute == nil {
			return SongDetail{}, fmt.Errorf("%w: no compute worker", ErrWorkerUnavailable)
		}
		loudness, err := decodeLoudness(row.loudness)
		if err != nil {
			return SongDetail{}, err
		}
		selection, err := s.compute.Music(ctx, MusicRequest{Structure: *st, Loudness: loudness, Target: SongTarget, Start: &start})
		if err != nil {
			return SongDetail{}, err
		}
		if selection.Unusable != "" {
			return SongDetail{}, fmt.Errorf("%w: %s", ErrSongStart, selection.Unusable)
		}
		encoded, err := json.Marshal(keptSelection{Selection: selection, Music: selection.Music})
		if err != nil {
			return SongDetail{}, err
		}
		keptStart, kept = start, string(encoded)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE projects SET song_start = ?, song_selection = ?, updated_at = ?
		WHERE id = ? AND owner_user_id = ? AND song_id = ?`, keptStart, kept, sqlitedb.FormatTime(s.now()), projectID, owner, row.id)
	if err != nil {
		return SongDetail{}, err
	}
	if changed, err := result.RowsAffected(); err != nil || changed == 0 {
		// The member chose another song meanwhile.
		return SongDetail{}, errors.Join(ErrSongNotReady, err)
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

// SongAudio is a project's song opened to be heard.
type SongAudio struct {
	File *os.File
	Song SongDetail
	// ContentType is how the file is sent; empty when it is decoded to FLAC.
	ContentType string
	ModifiedAt  time.Time
}

// OpenSongAudio opens owner's project's song for the member to hear.
func (s *Store) OpenSongAudio(ctx context.Context, owner, projectID string) (SongAudio, error) {
	chosen, err := s.projectSong(ctx, s.db, owner, projectID)
	if err != nil {
		return SongAudio{}, err
	}
	row, err := s.song(ctx, s.db, chosen.songID.String)
	if err != nil {
		return SongAudio{}, err
	}
	file, err := os.Open(s.songPath(row.sha))
	if err != nil {
		return SongAudio{}, err
	}
	info, err := file.Stat()
	if err != nil {
		return SongAudio{}, errors.Join(err, file.Close())
	}
	return SongAudio{File: file, ContentType: row.contentType, ModifiedAt: info.ModTime(),
		Song: SongDetail{ID: row.id, Title: row.title, Duration: row.duration, Transcode: row.contentType == ""}}, nil
}

// DecodeSong streams audio, a song OpenSongAudio opened, as FLAC from start
// seconds, for a copy browsers cannot play. The media worker receives a
// descriptor of its own; audio.File stays the caller's to close.
func (s *Store) DecodeSong(ctx context.Context, audio SongAudio, start float64) (io.ReadCloser, error) {
	if math.IsNaN(start) || start < 0 || start > audio.Song.Duration {
		return nil, ErrInvalid
	}
	if s.media == nil {
		return nil, ErrMediaUnavailable
	}
	stream, err := s.media.Audio(ctx, audio.File, ffmpegworker.AudioRequest{Start: start, Bits: 16})
	switch {
	case errors.Is(err, ffmpegworker.ErrUnreadable), errors.Is(err, ffmpegworker.ErrInvalid):
		return nil, fmt.Errorf("%w: %v", ErrNotAudio, err)
	case err != nil:
		return nil, fmt.Errorf("%w: %v", ErrMediaUnavailable, err)
	}
	return stream, nil
}
