package montage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
)

// What a job asks of its project's song: the store is the jobs' Music
// (pipeline.go).

// Excerpt is the part of the song the film is cut to.
type Excerpt struct {
	// Key names the song's copy, its analysis and the excerpt's start.
	Key string `json:"key"`
	// Music is the excerpt as the compute Worker's domain reads it.
	Music json.RawMessage `json:"music"`
	// Start is where the excerpt starts in the song, in seconds; Frames
	// is how long the film is, at FilmRate.
	Start  float64 `json:"start"`
	Frames int     `json:"frames"`
}

// Song errors.
var (
	// ErrSongAnalyzing: the song's analysis is not done yet.
	ErrSongAnalyzing = errors.New("the song is still being analysed")
	// ErrSongUnusable: the song cannot be used, or none was chosen.
	ErrSongUnusable = errors.New("the song cannot be used")
)

// DefaultStart stands for the excerpt the eighth version's rule picks: a
// project whose member never dragged the start follows it, also when the
// analysis finishes after the job began.
const DefaultStart = -1.0

// Chosen is owner's project's song and the excerpt's start, DefaultStart
// when the member kept the default; no song is "".
func (s *Store) Chosen(ctx context.Context, owner, projectID string) (string, float64, error) {
	return s.chosenIn(ctx, s.db, owner, projectID)
}

// chosenIn is Chosen read through q, as a transaction that holds the
// database's one connection must.
func (s *Store) chosenIn(ctx context.Context, q queryer, owner, projectID string) (string, float64, error) {
	chosen, err := s.projectSong(ctx, q, owner, projectID)
	switch {
	case errors.Is(err, ErrNoSong):
		return "", 0, nil
	case err != nil:
		return "", 0, err
	case chosen.start.Valid:
		return chosen.songID.String, chosen.start.Float64, nil
	}
	return chosen.songID.String, DefaultStart, nil
}

// ownSong is owner's song row.
func (s *Store) ownSong(ctx context.Context, owner, songID string) (songRow, error) {
	row, err := s.song(ctx, s.db, songID)
	if err == nil && row.owner != owner {
		err = ErrNoSong
	}
	if errors.Is(err, ErrNoSong) {
		return songRow{}, fmt.Errorf("%w: %v", ErrSongUnusable, err)
	}
	return row, err
}

// excerptStart resolves the job's selection against the analysis without
// asking a worker to cut it. A completed step can therefore be reused
// while the compute worker is unavailable.
func (row songRow) excerptStart(start float64) (float64, error) {
	switch row.state {
	case SongAnalyzing:
		return 0, ErrSongAnalyzing
	case SongUnusable:
		return 0, fmt.Errorf("%w: %s", ErrSongUnusable, row.reason)
	}
	st, selection := row.analysis.Structure, row.analysis.Selection
	if st == nil || selection == nil || len(row.analysis.Music) == 0 {
		return 0, ErrSongAnalyzing
	}
	if math.IsNaN(start) || math.IsInf(start, 0) {
		return 0, ErrInvalid
	}
	if start < 0 {
		return selection.Excerpt[0], nil
	}
	if len(st.Downbeats) > 0 {
		start = nearest(st.Downbeats, start)
		// The default excerpt's start is kept to two decimals and a
		// downbeat to three, so the two are compared as downbeats: a start
		// on the default's is the default excerpt, as in SongStart.
		if start == defaultDownbeat(st, selection) {
			return selection.Excerpt[0], nil
		}
	}
	return start, nil
}

func (row songRow) excerptKey(start float64) string {
	sum := sha256.Sum256([]byte(row.sha + "\x00" + row.analysis.Structure.Model + "\x00" + row.version + "\x00" + strconv.FormatFloat(start, 'g', -1, 64)))
	return "song:" + hex.EncodeToString(sum[:])
}

// ExcerptKey identifies a selection even when the worker cannot be reached.
func (s *Store) ExcerptKey(ctx context.Context, owner, songID string, start float64) (string, error) {
	row, err := s.ownSong(ctx, owner, songID)
	if err != nil {
		return "", err
	}
	start, err = row.excerptStart(start)
	if err != nil {
		return "", err
	}
	return row.excerptKey(start), nil
}

// draggedSelection is the excerpt one of owner's projects keeps for the song
// from start (SongStart), nil when none does.
func (s *Store) draggedSelection(ctx context.Context, owner, songID string, start float64) (*keptSelection, error) {
	var text string
	err := s.db.QueryRowContext(ctx, `SELECT song_selection FROM projects
		WHERE owner_user_id = ? AND song_id = ? AND song_start = ? AND song_selection IS NOT NULL LIMIT 1`, owner, songID, start).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var kept keptSelection
	if err := json.Unmarshal([]byte(text), &kept); err != nil {
		return nil, err
	}
	if len(kept.Music) == 0 || len(kept.Parts) == 0 || kept.Seconds <= 0 {
		return nil, nil
	}
	return &kept, nil
}

// named is the excerpt music as the domain reads it, with title for its
// song. An analysis is shared by every member who chose the same content, so
// it names no song; the plan's music note shows each member their own.
func named(music json.RawMessage, title string) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(music, &fields); err != nil {
		return nil, err
	}
	song, err := json.Marshal(title)
	if err != nil {
		return nil, err
	}
	fields["song"] = song
	return json.Marshal(fields)
}

// Excerpt is owner's song's excerpt from start, or the default one for
// DefaultStart (or any negative start).
func (s *Store) Excerpt(ctx context.Context, owner, songID string, start float64) (Excerpt, error) {
	row, err := s.ownSong(ctx, owner, songID)
	if err != nil {
		return Excerpt{}, err
	}
	start, err = row.excerptStart(start)
	if err != nil {
		return Excerpt{}, err
	}
	st, selection, music := row.analysis.Structure, row.analysis.Selection, row.analysis.Music
	if start != selection.Excerpt[0] {
		// The excerpt the member saw when they dragged the start is the
		// one the film is cut to; the compute Worker is asked only when no
		// project keeps it any more.
		kept, err := s.draggedSelection(ctx, owner, songID, start)
		if err != nil {
			return Excerpt{}, err
		}
		if kept == nil {
			if s.compute == nil {
				return Excerpt{}, fmt.Errorf("%w: no compute worker", ErrWorkerUnavailable)
			}
			loudness, err := decodeLoudness(row.loudness)
			if err != nil {
				return Excerpt{}, err
			}
			dragged, err := s.compute.Music(ctx, MusicRequest{Structure: *st, Loudness: loudness, Target: SongTarget, Start: &start})
			switch {
			case errors.Is(err, ErrWorkerRefused):
				return Excerpt{}, fmt.Errorf("%w: %v", ErrSongUnusable, err)
			case err != nil:
				return Excerpt{}, err
			case dragged.Unusable != "":
				return Excerpt{}, fmt.Errorf("%w: %s", ErrSongUnusable, dragged.Unusable)
			}
			kept = &keptSelection{Selection: dragged, Music: dragged.Music}
		}
		selection, music = &kept.Selection, kept.Music
	}
	music, err = named(music, row.title)
	if err != nil {
		return Excerpt{}, err
	}
	return Excerpt{Key: row.excerptKey(start), Music: music, Start: selection.Excerpt[0],
		Frames: int(math.Round(selection.Seconds * FilmRate))}, nil
}

// Audio opens owner's song's copy, for the film's sound.
func (s *Store) Audio(ctx context.Context, owner, songID string) (*os.File, error) {
	row, err := s.ownSong(ctx, owner, songID)
	if err != nil {
		return nil, err
	}
	return os.Open(s.songPath(row.sha))
}
