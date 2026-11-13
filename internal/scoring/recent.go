package scoring

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"slices"

	"github.com/zhongwater123/A-NAS/internal/atomicfile"
	"github.com/zhongwater123/A-NAS/internal/decodepanic"
	"github.com/zhongwater123/A-NAS/internal/imagerender"
)

// RecentScore is one of a rater's latest scores, shown on the ruler as a
// thumbnail at its score for reference.
type RecentScore struct {
	Token string
	// Score is overall in hundredths, Scores the batch's other dimensions.
	Score         int
	Scores        map[string]int
	Width, Height int
}

// RecentScores returns rater's latest Recent current scores in batch,
// oldest first. Skips and marks have no place on the ruler. A repeat is
// listed like any score: the photo it repeats is at least repeatGap scores
// back, never among these, so the ruler does not give a repeat away.
func (s *Store) RecentScores(ctx context.Context, batchID, rater string) ([]RecentScore, error) {
	if ok, err := raterIn(ctx, s.db, rater); err != nil || !ok {
		return nil, errors.Join(err, ErrNotRater)
	}
	if _, err := s.Batch(ctx, batchID); err != nil {
		return nil, err
	}
	recent := []RecentScore{}
	byRating := map[int64]*RecentScore{}
	var ids []int64
	err := each(ctx, s.db, `SELECT r.id, r.claim_token, r.score, c.preview_width, c.preview_height
		FROM ratings r JOIN items i ON i.id = r.item_id JOIN contents c ON c.sha256 = i.sha256
		WHERE r.batch_id = ? AND r.rater = ? AND r.kind = 'score' AND r.superseded_at IS NULL
		ORDER BY r.id DESC LIMIT ?`, []any{batchID, rater, Recent}, func(rows *sql.Rows) error {
		var id int64
		score := RecentScore{Scores: map[string]int{}}
		if err := rows.Scan(&id, &score.Token, &score.Score, &score.Width, &score.Height); err != nil {
			return err
		}
		ids = append(ids, id)
		recent = append(recent, score)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for i, id := range ids {
		byRating[id] = &recent[i]
	}
	if len(ids) > 0 {
		err = each(ctx, s.db, `SELECT s.rating_id, s.dimension, s.score FROM rating_scores s JOIN ratings r ON r.id = s.rating_id
			WHERE r.batch_id = ? AND r.rater = ? AND s.rating_id >= ?`, []any{batchID, rater, slices.Min(ids)}, func(rows *sql.Rows) error {
			var id int64
			var dimension string
			var value int
			if err := rows.Scan(&id, &dimension, &value); err != nil {
				return err
			}
			if score := byRating[id]; score != nil {
				score.Scores[dimension] = value
			}
			return nil
		})
	}
	slices.Reverse(recent)
	return recent, err
}

// ClaimThumbnail returns the small JPEG of the photo behind a rater's own
// claim, rendering it from the preview the first time.
func (s *Store) ClaimThumbnail(ctx context.Context, token, rater string) (string, error) {
	var sha string
	err := s.db.QueryRowContext(ctx, `SELECT i.sha256 FROM claims c JOIN items i ON i.id = c.item_id
		WHERE c.token = ? AND c.rater = ?`, token, rater).Scan(&sha)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return s.thumbnail(ctx, sha)
}

// BatchCover returns the small JPEG of the photo that stands for a batch in
// the administrators' list: the first of its photos in the batch's random
// order, anchors excluded. A batch without photos yet has none.
func (s *Store) BatchCover(ctx context.Context, batchID string) (string, error) {
	var sha string
	err := s.db.QueryRowContext(ctx, `SELECT sha256 FROM items WHERE batch_id = ? AND anchor_score IS NULL
		ORDER BY shuffle LIMIT 1`, batchID).Scan(&sha)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return s.thumbnail(ctx, sha)
}

// thumbnail returns the small JPEG of a content, rendering it from the
// preview the first time.
func (s *Store) thumbnail(ctx context.Context, sha string) (string, error) {
	name := s.thumbnailPath(sha)
	if _, err := os.Stat(name); err == nil {
		return name, nil
	}
	data, err := s.renderThumbnail(ctx, sha)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return "", err
	}
	return name, atomicfile.Write(name, data, 0o600)
}

// renderThumbnail scales the module's own preview, already upright and
// without metadata, down to ThumbnailEdge.
func (s *Store) renderThumbnail(ctx context.Context, sha string) (data []byte, err error) {
	file, err := os.Open(s.previewPath(sha))
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	defer func() {
		if recovered := recover(); recovered != nil {
			decodepanic.Log("photo scoring thumbnail", recovered)
			data, err = nil, fmt.Errorf("preview %s cannot be decoded", sha)
		}
	}()
	err = imagerender.WithDecoded(ctx, file, func(src image.Image) error {
		var renderErr error
		data, _, _, renderErr = imagerender.UprightJPEG(src, 1, ThumbnailEdge, thumbnailQuality)
		return renderErr
	})
	return data, err
}
