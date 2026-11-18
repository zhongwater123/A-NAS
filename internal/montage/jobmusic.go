package montage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
)

// What a job asks of its project's song (M8a, internal/montage/pipeline.go
// there defines Music, Excerpt and the two errors the same way; whichever
// lands second drops its copy).

// Excerpt is the part of the song the film is cut to.
type Excerpt struct {
	// Key names the song's copy, its analysis and the excerpt's start.
	Key string `json:"key"`
	// Music is the excerpt as the compute Worker's domain reads it.
	Music json.RawMessage `json:"music"`
	// Start is where the excerpt starts in the song, in seconds; Frames
	// is how long the film is, at filmRate.
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

// filmRate is the film's frames per second (1920×1080 at 30).
const filmRate = 30

// DefaultStart stands for the excerpt the eighth version's rule picks: a
// project whose member never dragged the start follows it, also when the
// analysis finishes after the job began.
const DefaultStart = -1.0

// Chosen is owner's project's song and the excerpt's start, DefaultStart
// when the member kept the default; no song is "".
func (s *Store) Chosen(ctx context.Context, owner, projectID string) (string, float64, error) {
	chosen, err := s.projectSong(ctx, s.db, owner, projectID)
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

// Excerpt is owner's song's excerpt from start, or the default one for
// DefaultStart (or any negative start).
func (s *Store) Excerpt(ctx context.Context, owner, songID string, start float64) (Excerpt, error) {
	row, err := s.ownSong(ctx, owner, songID)
	if err != nil {
		return Excerpt{}, err
	}
	switch row.state {
	case SongAnalyzing:
		return Excerpt{}, ErrSongAnalyzing
	case SongUnusable:
		return Excerpt{}, fmt.Errorf("%w: %s", ErrSongUnusable, row.reason)
	}
	st, selection, music := row.analysis.Structure, row.analysis.Selection, row.analysis.Music
	if st == nil || selection == nil || len(music) == 0 {
		return Excerpt{}, ErrSongAnalyzing
	}
	if math.IsNaN(start) || math.IsInf(start, 0) {
		return Excerpt{}, ErrInvalid
	}
	if start >= 0 && len(st.Downbeats) > 0 {
		start = nearest(st.Downbeats, start)
	}
	if start >= 0 && start != selection.Excerpt[0] {
		if s.compute == nil {
			return Excerpt{}, fmt.Errorf("%w: no compute worker", ErrWorkerUnavailable)
		}
		loudness, err := decodeLoudness(row.loudness)
		if err != nil {
			return Excerpt{}, err
		}
		dragged, err := s.compute.Music(ctx, MusicRequest{Song: row.title, Structure: *st, Loudness: loudness, Target: SongTarget, Start: &start})
		switch {
		case errors.Is(err, ErrWorkerRefused):
			return Excerpt{}, fmt.Errorf("%w: %v", ErrSongUnusable, err)
		case err != nil:
			return Excerpt{}, err
		case dragged.Unusable != "":
			return Excerpt{}, fmt.Errorf("%w: %s", ErrSongUnusable, dragged.Unusable)
		}
		selection, music = &dragged, dragged.Music
	}
	from := selection.Excerpt[0]
	// The key changes with the copy, the models that analysed it, how the
	// excerpt is picked and where it starts.
	sum := sha256.Sum256([]byte(row.sha + "\x00" + st.Model + "\x00" + row.version + "\x00" + strconv.FormatFloat(from, 'f', 3, 64)))
	return Excerpt{Key: "song:" + hex.EncodeToString(sum[:]), Music: music, Start: from,
		Frames: int(math.Round(selection.Seconds * filmRate))}, nil
}

// Audio opens owner's song's copy, for the film's sound.
func (s *Store) Audio(ctx context.Context, owner, songID string) (*os.File, error) {
	row, err := s.ownSong(ctx, owner, songID)
	if err != nil {
		return nil, err
	}
	return os.Open(s.songPath(row.sha))
}
