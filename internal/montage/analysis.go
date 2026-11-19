package montage

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// A song is analysed once per content: the songs of several members with
// the same digest share one analysis. The montage center decodes the copy
// with the FFmpeg worker into two streams, stereo at 44.1 kHz for allin1 and
// mono at 22050 Hz for the loudness, the eighth version's inputs, and hands
// them to the music analysis Worker; then the compute Worker's music
// operation picks the default excerpt. A Worker that is away leaves the song
// waiting, with what was found so far kept, and it is tried again later, as
// long as it takes: asking costs nothing. An analysis the Worker took and
// could not finish is tried analysisAttempts times, since every attempt
// costs minutes of the Worker, and then the song cannot be used.

// AnalysisVersion names how songs are analysed now: allin1's harmonix-all
// and the eighth version's excerpt.
const AnalysisVersion = "allin1-harmonix-all/music-v8"

// Rates the analysis reads the song at (ffmpegworker.AudioRequest.PCM).
const (
	stereoRate = 44100
	monoRate   = 22050
)

// analysisRetry is how long a song waits for a Worker that was away, or
// between attempts that failed.
const analysisRetry = 5 * time.Minute

// analysisAttempts is how many times a Worker may fail on a song
// (ErrWorkerFailed) before the song is unusable.
const analysisAttempts = 3

// analysisPoll is how often waiting songs are looked at without a wake-up.
const analysisPoll = time.Minute

func (s *Store) wakeAnalysis() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// AnalyzeSongs analyses the songs waiting, one content at a time, until ctx
// ends. Songs an earlier AnalysisVersion found unusable are analysed again
// first.
func (s *Store) AnalyzeSongs(ctx context.Context) {
	if err := s.requeueStale(ctx); err != nil && ctx.Err() == nil {
		s.logger.WarnContext(ctx, "queueing songs for another analysis failed", "error", err)
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-s.wake:
		}
		if err := s.AnalyzeWaiting(ctx); err != nil && ctx.Err() == nil {
			s.logger.WarnContext(ctx, "finding a song to analyse failed", "error", err)
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(analysisPoll)
	}
}

// AnalyzeWaiting analyses every song due now, one content at a time; a song
// whose Worker is away is due again later.
func (s *Store) AnalyzeWaiting(ctx context.Context) error {
	for ctx.Err() == nil {
		sha, ok, err := s.nextSong(ctx)
		if err != nil || !ok {
			return err
		}
		s.analyze(ctx, sha)
	}
	return ctx.Err()
}

// requeueStale queues the songs that an earlier AnalysisVersion found
// unusable: what changed since may be what refused them. Songs that are ready
// keep the analysis their projects were set up with.
func (s *Store) requeueStale(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE songs SET state = 'analyzing', reason = '', waiting = '', attempts = 0, next_try = NULL,
		analysis_json = NULL, loudness = NULL, updated_at = ? WHERE state = 'unusable' AND analysis_version != ?`,
		sqlitedb.FormatTime(s.now()), AnalysisVersion)
	return err
}

func (s *Store) nextSong(ctx context.Context) (string, bool, error) {
	var sha string
	err := s.db.QueryRowContext(ctx, `SELECT sha256 FROM songs WHERE state = 'analyzing' AND (next_try IS NULL OR next_try <= ?)
		ORDER BY created_at, id LIMIT 1`, sqlitedb.FormatTime(s.now())).Scan(&sha)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return sha, err == nil, err
}

// analyze analyses the content with this digest and records it in every
// row waiting for it.
func (s *Store) analyze(ctx context.Context, sha string) {
	analysis, loudness, err := s.knownStructure(ctx, sha)
	if err != nil {
		s.logger.WarnContext(ctx, "reading a song's analysis failed", "error", err)
		s.wait(ctx, sha, WaitAnalyzer)
		return
	}
	if analysis.Structure == nil {
		found, err := s.findStructure(ctx, sha)
		switch {
		case err != nil && ctx.Err() != nil:
			// The service is stopping; the song is as due as it was.
			return
		case errors.Is(err, ErrNotAudio), errors.Is(err, ErrWorkerRefused):
			s.logger.InfoContext(ctx, "a song cannot be analysed", "song", sha, "error", err)
			s.finish(ctx, sha, SongUnusable, UnusableUndecodable, songAnalysis{}, nil)
			return
		case errors.Is(err, ErrMediaUnavailable):
			s.logger.WarnContext(ctx, "the media worker is away; the song waits", "error", err)
			s.wait(ctx, sha, WaitMedia)
			return
		case errors.Is(err, ErrWorkerFailed):
			s.logger.WarnContext(ctx, "the music analysis worker failed on a song", "song", sha, "error", err)
			s.failed(ctx, sha, WaitAnalyzer, songAnalysis{}, nil)
			return
		case err != nil:
			s.logger.WarnContext(ctx, "the music analysis worker is away; the song waits", "error", err)
			s.wait(ctx, sha, WaitAnalyzer)
			return
		}
		analysis = songAnalysis{Structure: &found.Structure, Seconds: found.Seconds}
		loudness = found.Loudness
		s.logger.InfoContext(ctx, "analysed a song", "song", sha, "seconds", found.Seconds)
		// What allin1 found is kept even if the excerpt has to wait.
		if err := s.keepStructure(ctx, sha, analysis, loudness); err != nil {
			s.logger.WarnContext(ctx, "keeping a song's analysis failed", "error", err)
			s.wait(ctx, sha, WaitAnalyzer)
			return
		}
	}
	if s.compute == nil {
		s.wait(ctx, sha, WaitCompute)
		return
	}
	// The excerpt is every member's who chose this content, so it names no
	// song: each member's own title joins it when a job asks (Excerpt).
	selection, err := s.compute.Music(ctx, MusicRequest{Structure: *analysis.Structure, Loudness: loudness, Target: SongTarget})
	switch {
	case err != nil && ctx.Err() != nil:
		return
	case errors.Is(err, ErrWorkerRefused):
		// Not one of the reasons the operation names (few_bars and the
		// others arrive in its answer): it cannot read what allin1 found.
		s.logger.WarnContext(ctx, "the compute worker refused a song's structure", "song", sha, "error", err)
		s.finish(ctx, sha, SongUnusable, UnusableAnalysisFailed, analysis, loudness)
		return
	case errors.Is(err, ErrWorkerFailed):
		// It took the structure and broke on it, or stopped. That may be
		// the Worker's moment or this song's, so it counts as an attempt.
		s.logger.WarnContext(ctx, "the compute worker failed on a song's structure", "song", sha, "error", err)
		s.failed(ctx, sha, WaitCompute, analysis, loudness)
		return
	case err != nil:
		s.logger.WarnContext(ctx, "the compute worker is away; the song waits", "error", err)
		s.wait(ctx, sha, WaitCompute)
		return
	}
	analysis.Selection, analysis.Music = &selection, selection.Music
	if selection.Unusable != "" {
		s.finish(ctx, sha, SongUnusable, selection.Unusable, analysis, loudness)
		return
	}
	s.finish(ctx, sha, SongReady, "", analysis, loudness)
}

// knownStructure is what an earlier attempt found for the content.
func (s *Store) knownStructure(ctx context.Context, sha string) (songAnalysis, []float32, error) {
	var text sql.NullString
	var blob []byte
	err := s.db.QueryRowContext(ctx, `SELECT analysis_json, loudness FROM songs WHERE sha256 = ? AND analysis_json IS NOT NULL
		ORDER BY updated_at DESC LIMIT 1`, sha).Scan(&text, &blob)
	if errors.Is(err, sql.ErrNoRows) {
		return songAnalysis{}, nil, nil
	}
	if err != nil {
		return songAnalysis{}, nil, err
	}
	var analysis songAnalysis
	if err := json.Unmarshal([]byte(text.String), &analysis); err != nil {
		return songAnalysis{}, nil, err
	}
	loudness, err := decodeLoudness(blob)
	if err != nil || analysis.Structure == nil || len(loudness) == 0 {
		return songAnalysis{}, nil, err
	}
	analysis.Selection, analysis.Music = nil, nil
	return analysis, loudness, nil
}

// findStructure decodes the copy and has the music analysis Worker read it.
func (s *Store) findStructure(ctx context.Context, sha string) (Analysis, error) {
	if s.media == nil {
		return Analysis{}, ErrMediaUnavailable
	}
	if s.music == nil {
		return Analysis{}, fmt.Errorf("%w: no music analysis worker", ErrWorkerUnavailable)
	}
	file, err := os.Open(s.songPath(sha))
	if err != nil {
		return Analysis{}, err
	}
	defer func() { _ = file.Close() }()
	stereoOut, stereoIn, err := os.Pipe()
	if err != nil {
		return Analysis{}, err
	}
	monoOut, monoIn, err := os.Pipe()
	if err != nil {
		return Analysis{}, errors.Join(err, stereoOut.Close(), stereoIn.Close())
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// The Worker reads the stereo stream to its end, then the mono one, so
	// one decoding runs at a time. A pipe ends the same way whether the
	// decoding finished or broke off, so the Worker analyses the streams
	// only once whole says both were written to their end.
	whole := make(chan error, 1)
	decoded := make(chan error, 1)
	go func() {
		err := s.decodeInto(ctx, file, stereoIn, 2, stereoRate)
		if err == nil {
			err = s.decodeInto(ctx, file, monoIn, 1, monoRate)
		} else {
			_ = monoIn.Close()
		}
		whole <- err
		decoded <- err
	}()
	analysis, err := s.music.Analyze(ctx, stereoOut, monoOut, whole)
	// A Worker that stopped reading leaves the decoding a closed pipe.
	_ = stereoOut.Close()
	_ = monoOut.Close()
	decodeErr := <-decoded
	switch {
	case decodeErr != nil && !errors.Is(decodeErr, syscall.EPIPE):
		// The decoding broke off by itself: what the Worker made of a
		// stream cut short says nothing about the song.
		return Analysis{}, decodeErr
	case err != nil:
		return Analysis{}, err
	}
	return analysis, nil
}

// decodeInto writes the copy's samples to pipe and closes it.
func (s *Store) decodeInto(ctx context.Context, file *os.File, pipe *os.File, channels, rate int) error {
	defer func() { _ = pipe.Close() }()
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	stream, err := s.media.Audio(ctx, file, ffmpegworker.AudioRequest{PCM: true, Bits: 32, Channels: channels, SampleRate: rate})
	if err == nil {
		_, err = io.Copy(pipe, stream)
		err = errors.Join(err, stream.Close())
	}
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ffmpegworker.ErrUnreadable), errors.Is(err, ffmpegworker.ErrInvalid), errors.Is(err, ffmpegworker.ErrTooLarge):
		return fmt.Errorf("%w: %v", ErrNotAudio, err)
	}
	return fmt.Errorf("%w: %w", ErrMediaUnavailable, err)
}

func (s *Store) keepStructure(ctx context.Context, sha string, analysis songAnalysis, loudness []float32) error {
	encoded, err := json.Marshal(analysis)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE songs SET analysis_json = ?, loudness = ?, analysis_version = ?, updated_at = ?
		WHERE sha256 = ? AND state = 'analyzing'`, string(encoded), encodeLoudness(loudness), AnalysisVersion, sqlitedb.FormatTime(s.now()), sha)
	return err
}

// finish records the analysis in every row of the content still waiting.
func (s *Store) finish(ctx context.Context, sha, state, reason string, analysis songAnalysis, loudness []float32) {
	encoded, err := json.Marshal(analysis)
	if err == nil {
		_, err = s.db.ExecContext(ctx, `UPDATE songs SET state = ?, reason = ?, waiting = '', next_try = NULL, analysis_json = ?,
			loudness = ?, analysis_version = ?, updated_at = ? WHERE sha256 = ? AND state = 'analyzing'`,
			state, reason, string(encoded), encodeLoudness(loudness), AnalysisVersion, sqlitedb.FormatTime(s.now()), sha)
	}
	if err != nil {
		s.logger.WarnContext(ctx, "recording a song's analysis failed", "error", err)
		s.wait(ctx, sha, WaitAnalyzer)
	}
}

// wait leaves the content's rows waiting for a Worker that is away.
func (s *Store) wait(ctx context.Context, sha, why string) {
	if _, err := s.db.ExecContext(ctx, `UPDATE songs SET waiting = ?, next_try = ?
		WHERE sha256 = ? AND state = 'analyzing'`, why, sqlitedb.FormatTime(s.now().Add(analysisRetry)), sha); err != nil {
		s.logger.WarnContext(ctx, "recording a waiting song failed", "error", err)
	}
}

// failed counts an analysis a Worker took and could not finish, and leaves
// the content waiting for that Worker (why). It is tried again later, until
// analysisAttempts of them failed: then it cannot be used, and the Worker is
// free for other songs; analysis and loudness are what was found of it so
// far.
func (s *Store) failed(ctx context.Context, sha, why string, analysis songAnalysis, loudness []float32) {
	var attempts int
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE songs SET waiting = ?, attempts = attempts + 1, next_try = ?
			WHERE sha256 = ? AND state = 'analyzing'`, why, sqlitedb.FormatTime(s.now().Add(analysisRetry)), sha); err != nil {
			return err
		}
		// A member who chose the content later joins the count of those
		// already waiting.
		return tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(attempts), 0) FROM songs WHERE sha256 = ? AND state = 'analyzing'", sha).Scan(&attempts)
	})
	if err != nil {
		s.logger.WarnContext(ctx, "recording a failed analysis failed", "error", err)
		return
	}
	if attempts >= analysisAttempts {
		s.logger.WarnContext(ctx, "a song's analysis failed too often; the song cannot be used", "song", sha, "attempts", attempts)
		s.finish(ctx, sha, SongUnusable, UnusableAnalysisFailed, analysis, loudness)
	}
}

// encodeLoudness is the curve as little-endian float32, as the Workers pass
// it and the songs table keeps it.
func encodeLoudness(values []float32) []byte {
	if values == nil {
		return nil
	}
	// Append fails only for types without a fixed size.
	out, _ := binary.Append(make([]byte, 0, 4*len(values)), binary.LittleEndian, values)
	return out
}

func decodeLoudness(data []byte) ([]float32, error) {
	if len(data)%4 != 0 {
		return nil, errors.New("a song's loudness is not whole float32 values")
	}
	out := make([]float32, len(data)/4)
	if _, err := binary.Decode(data, binary.LittleEndian, out); err != nil {
		return nil, err
	}
	return out, nil
}
