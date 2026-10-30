package family

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"time"
)

// cursorKey continues a listing after an item: its sort time and ID.
type cursorKey struct {
	at string
	id string
}

func (c cursorKey) String() string {
	return base64.RawURLEncoding.EncodeToString([]byte(c.at + "|" + c.id))
}

func parseCursor(value string) (*cursorKey, error) {
	if value == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, err
	}
	at, id, ok := strings.Cut(string(raw), "|")
	if !ok || id == "" {
		return nil, errors.New("malformed cursor")
	}
	return &cursorKey{at: at, id: id}, nil
}

// AlbumPhoto is a photo or video in a baby's cloud album.
type AlbumPhoto struct {
	AssetID  string     `json:"assetId"`
	RecordID string     `json:"recordId"`
	TakenAt  *time.Time `json:"takenAt,omitempty"`
	Video    bool       `json:"video"`
	// MonthOfLife groups the album: the month of life the photo was taken
	// in, from 0 for the first month, or -1 before birth.
	MonthOfLife int `json:"monthOfLife"`
}

// AlbumPage is one page of a cloud album, newest photos first.
type AlbumPage struct {
	Photos []AlbumPhoto `json:"photos"`
	Next   string       `json:"next,omitempty"`
}

// albumPageSize is larger than a record page: the album is a dense grid.
const albumPageSize = 120

// Album pages through the photos and videos of a baby's channel that the
// caller can see, newest taken first. A photo in several records appears
// once. Vaccination certificates stay out of the album.
func (s *Store) Album(ctx context.Context, actor Actor, babyID, cursor string) (AlbumPage, error) {
	if _, _, err := s.requireMember(ctx, s.db, actor); err != nil {
		return AlbumPage{}, err
	}
	baby, err := s.baby(ctx, s.db, babyID)
	if err != nil {
		return AlbumPage{}, err
	}
	birth, _ := parseDate(baby.BirthDate)
	after, err := parseCursor(cursor)
	if err != nil {
		return AlbumPage{}, ErrInvalid
	}
	page := AlbumPage{Photos: []AlbumPhoto{}}
	for len(page.Photos) < albumPageSize {
		// Photos without a capture time sort by when they were added, which
		// is when they were uploaded.
		query := `SELECT p.asset_id, MIN(p.record_id), MAX(COALESCE(p.taken_at, r.created_at)) AS sort_at, MAX(p.video)
FROM record_photos p JOIN records r ON r.id = p.record_id
WHERE r.baby_id = ? AND r.deleted_at IS NULL AND p.role = 'album'
GROUP BY p.asset_id`
		args := []any{babyID}
		if after != nil {
			query += " HAVING sort_at < ? OR (sort_at = ? AND p.asset_id < ?)"
			args = append(args, after.at, after.at, after.id)
		}
		query += " ORDER BY sort_at DESC, p.asset_id DESC LIMIT ?"
		args = append(args, albumPageSize)
		candidates, err := s.albumCandidates(ctx, query, args...)
		if err != nil {
			return AlbumPage{}, err
		}
		if len(candidates) == 0 {
			page.Next = ""
			break
		}
		ids := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			ids = append(ids, candidate.photo.AssetID)
		}
		visible, known, err := s.readable(ctx, actor, ids)
		if err != nil {
			return AlbumPage{}, err
		}
		for _, candidate := range candidates {
			found, ok := visible[candidate.photo.AssetID]
			if !known {
				found, ok = Photo{ID: candidate.photo.AssetID, Video: candidate.photo.Video}, true
			}
			if !ok {
				continue
			}
			photo := candidate.photo
			if found.TakenAt != nil {
				photo.TakenAt = found.TakenAt
			}
			photo.Video = found.Video
			photo.MonthOfLife = monthOfLife(birth, photo.TakenAt, candidate.sortAt)
			page.Photos = append(page.Photos, photo)
		}
		last := candidates[len(candidates)-1]
		after = &cursorKey{at: last.sortKey, id: last.photo.AssetID}
		page.Next = after.String()
		if len(candidates) < albumPageSize {
			page.Next = ""
			break
		}
	}
	return page, nil
}

type albumCandidate struct {
	photo   AlbumPhoto
	sortKey string
	sortAt  time.Time
}

func (s *Store) albumCandidates(ctx context.Context, query string, args ...any) ([]albumCandidate, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var candidates []albumCandidate
	for rows.Next() {
		var candidate albumCandidate
		var video bool
		if err := rows.Scan(&candidate.photo.AssetID, &candidate.photo.RecordID, &candidate.sortKey, &video); err != nil {
			return nil, err
		}
		candidate.photo.Video = video
		candidate.sortAt = parseTime(candidate.sortKey)
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

// monthOfLife places a photo in the baby's life by its capture time, or by
// when it was added when the photo does not say when it was taken.
func monthOfLife(birth time.Time, taken *time.Time, added time.Time) int {
	moment := added
	if taken != nil {
		moment = *taken
	}
	local := moment.In(time.Local)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	age, ok := ageOn(birth, day)
	if !ok {
		return -1
	}
	return age.TotalMonths
}
