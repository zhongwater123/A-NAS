package scoring

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// nextItem finds a photo of batch for rater: one the rater never rated or
// holds, whose ratings plus open claims stay under required. Photos with the
// most ratings come first, so that photos are completed before new ones are
// started; within a level the order is the batch's shuffle, entered at a
// random point. start is that point; 0 also serves as a plain existence
// check. It returns 0 when there is none.
func nextItem(ctx context.Context, db querier, batch, rater string, required int, now string, start int64) (int64, error) {
	for level := required - 1; level >= 0; level-- {
		for _, query := range nextItemQueries {
			var id int64
			err := db.QueryRowContext(ctx, query, batch, level, start, rater, now, required).Scan(&id)
			if err == nil {
				return id, nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return 0, err
			}
		}
	}
	return 0, nil
}

// nextItemQueries walk one level of the shuffle from the start point to the
// end, then from the beginning up to it.
var nextItemQueries = func() [2]string {
	const query = `SELECT i.id FROM items i
		WHERE i.batch_id = ?1 AND i.anchor_score IS NULL AND i.covered = ?2 AND i.shuffle %s ?3
		AND NOT EXISTS (SELECT 1 FROM ratings r WHERE r.item_id = i.id AND r.rater = ?4)
		AND NOT EXISTS (SELECT 1 FROM claims c WHERE c.item_id = i.id AND c.rater = ?4 AND c.submitted = 0 AND c.expires_at > ?5)
		AND ?2 + (SELECT count(*) FROM claims c WHERE c.item_id = i.id AND c.submitted = 0 AND c.expires_at > ?5) < ?6
		ORDER BY i.shuffle LIMIT 1`
	return [2]string{fmt.Sprintf(query, ">="), fmt.Sprintf(query, "<")}
}()

// Claim is a photo handed to a rater: the token is all the rater learns
// about it, not its content hash, file name or position in the batch.
type Claim struct {
	Token         string
	ExpiresAt     time.Time
	Width, Height int
}

// Claim hands rater the photos it holds in batch, oldest first, after
// topping them up to MaxHeld. A batch that is closed hands out nothing new.
func (s *Store) Claim(ctx context.Context, batchID, rater string) ([]Claim, bool, error) {
	var claims []Claim
	finished := false
	err := s.within(ctx, func(tx *sql.Tx) error {
		if ok, err := raterIn(ctx, tx, rater); err != nil || !ok {
			return errors.Join(err, ErrNotRater)
		}
		var state string
		var required int
		err := tx.QueryRowContext(ctx, "SELECT state, required FROM batches WHERE id = ?", batchID).Scan(&state, &required)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		now := s.now()
		stamp := sqlitedb.FormatTime(now)
		held, err := heldClaims(ctx, tx, batchID, rater, stamp)
		if err != nil {
			return err
		}
		for state == "open" && len(held) < MaxHeld {
			item, repeatOf, err := s.pick(ctx, tx, batchID, rater, required, stamp)
			if err != nil {
				return err
			}
			if item == 0 {
				finished = len(held) == 0
				break
			}
			token := randomToken(16)
			expires := now.Add(LeaseDuration)
			_, err = tx.ExecContext(ctx, `INSERT INTO claims (token, item_id, batch_id, rater, repeat_of, issued_at, expires_at)
				VALUES (?, ?, ?, ?, ?, ?, ?)`, token, item, batchID, rater, repeatOf, stamp, sqlitedb.FormatTime(expires))
			if err != nil {
				return err
			}
			if held, err = heldClaims(ctx, tx, batchID, rater, stamp); err != nil {
				return err
			}
		}
		if state != "open" {
			finished = len(held) == 0
		}
		claims = held
		return nil
	})
	return claims, finished, err
}

func raterIn(ctx context.Context, db querier, account string) (bool, error) {
	var found int
	err := db.QueryRowContext(ctx, "SELECT 1 FROM raters WHERE account_id = ?", account).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func heldClaims(ctx context.Context, db querier, batchID, rater, now string) ([]Claim, error) {
	rows, err := db.QueryContext(ctx, `SELECT c.token, c.expires_at, k.preview_width, k.preview_height
		FROM claims c JOIN items i ON i.id = c.item_id JOIN contents k ON k.sha256 = i.sha256
		WHERE c.batch_id = ? AND c.rater = ? AND c.submitted = 0 AND c.expires_at > ?
		ORDER BY c.issued_at, c.rowid`, batchID, rater, now)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	claims := []Claim{}
	for rows.Next() {
		var claim Claim
		var expires string
		if err := rows.Scan(&claim.Token, &expires, &claim.Width, &claim.Height); err != nil {
			return nil, err
		}
		claim.ExpiresAt, _ = time.Parse(sqlitedb.TimeLayout, expires)
		claims = append(claims, claim)
	}
	return claims, rows.Err()
}

// pick chooses what to hand out next: now and then one of the rater's own
// earlier photos again to check consistency, otherwise nextItem.
func (s *Store) pick(ctx context.Context, tx *sql.Tx, batchID, rater string, required int, now string) (int64, *int64, error) {
	var rated int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM ratings
		WHERE batch_id = ? AND rater = ? AND kind = 'score' AND repeat_of IS NULL AND superseded_at IS NULL`, batchID, rater).Scan(&rated)
	if err != nil {
		return 0, nil, err
	}
	if rated >= repeatAfter && s.random() < repeatShare {
		// One of the scores at least repeatGap before the latest, not
		// repeated yet and not held now.
		offset := int(s.random() * float64(rated-repeatGap))
		var rating, item int64
		err := tx.QueryRowContext(ctx, `SELECT r.id, r.item_id FROM ratings r
			WHERE r.batch_id = ?1 AND r.rater = ?2 AND r.kind = 'score' AND r.repeat_of IS NULL AND r.superseded_at IS NULL
			AND r.id NOT IN (SELECT o.repeat_of FROM ratings o WHERE o.repeat_of IS NOT NULL)
			AND r.id NOT IN (SELECT c.repeat_of FROM claims c WHERE c.repeat_of IS NOT NULL AND c.submitted = 0 AND c.expires_at > ?3)
			AND r.id <= (SELECT id FROM ratings WHERE batch_id = ?1 AND rater = ?2 AND kind = 'score' AND repeat_of IS NULL
				AND superseded_at IS NULL ORDER BY id DESC LIMIT 1 OFFSET ?4)
			ORDER BY r.id LIMIT 1 OFFSET ?5`, batchID, rater, now, repeatGap, offset).Scan(&rating, &item)
		if err == nil {
			return item, &rating, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, nil, err
		}
	}
	item, err := nextItem(ctx, tx, batchID, rater, required, now, s.randomShuffle())
	return item, nil, err
}

// ClaimPreview returns the preview behind a rater's own claim, submitted or
// not, so that the rater can go back to it to change the latest score.
func (s *Store) ClaimPreview(ctx context.Context, token, rater string) (string, error) {
	var sha string
	err := s.db.QueryRowContext(ctx, `SELECT i.sha256 FROM claims c JOIN items i ON i.id = c.item_id
		WHERE c.token = ? AND c.rater = ?`, token, rater).Scan(&sha)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return s.previewPath(sha), nil
}

// Submission is a rater's answer for one claim.
type Submission struct {
	// ID is chosen by the page for each answer and makes retries of the
	// same answer harmless.
	ID string
	// Kind is "score", "skip" (cannot judge) or "flag" (something is wrong
	// with the photo); only a score has Score, in hundredths, 0 to MaxScore.
	Kind  string
	Score *int
	// ViewMilliseconds is from the photo appearing to the answer.
	ViewMilliseconds int
	// Viewport and Display are the window's and the shown photo's CSS
	// sizes, PixelRatio the device's.
	ViewportWidth, ViewportHeight int
	DisplayWidth, DisplayHeight   int
	PixelRatio                    float64
}

var submissionPattern = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)

func (submission Submission) validate() error {
	switch {
	case !submissionPattern.MatchString(submission.ID):
		return invalid("submission id must have 8 to 64 letters, digits or dashes")
	case submission.Kind == "score" && (submission.Score == nil || *submission.Score < 0 || *submission.Score > MaxScore):
		return invalid("a score is 0 to 5")
	case submission.Kind != "score" && (submission.Kind != "skip" && submission.Kind != "flag" || submission.Score != nil):
		return invalid("kind must be score, skip or flag; only a score has a value")
	case submission.ViewMilliseconds < 0 || submission.ViewMilliseconds > 24*60*60*1000:
		return invalid("view time is out of range")
	case submission.ViewportWidth < 0 || submission.ViewportHeight < 0 || submission.DisplayWidth < 0 || submission.DisplayHeight < 0 ||
		submission.ViewportWidth > 100000 || submission.ViewportHeight > 100000 || submission.DisplayWidth > 100000 || submission.DisplayHeight > 100000:
		return invalid("sizes are out of range")
	case submission.PixelRatio < 0 || submission.PixelRatio > 16:
		return invalid("pixel ratio is out of range")
	}
	return nil
}

// Result tells the rater how many photos of the batch they answered.
type Result struct {
	Rated int
}

// Submit records rater's answer for the claim token. The same submission ID
// again returns the recorded result. A claim already answered can be
// answered again only while that answer is the rater's latest in the batch:
// the new answer replaces it and the old one is kept as superseded. A claim
// whose lease ended is still accepted unless the rater meanwhile rated the
// photo through another claim.
func (s *Store) Submit(ctx context.Context, token, rater string, submission Submission) (Result, error) {
	if err := submission.validate(); err != nil {
		return Result{}, err
	}
	var result Result
	err := s.within(ctx, func(tx *sql.Tx) error {
		var item int64
		var batchID string
		var repeatOf sql.NullInt64
		err := tx.QueryRowContext(ctx, "SELECT item_id, batch_id, repeat_of FROM claims WHERE token = ? AND rater = ?",
			token, rater).Scan(&item, &batchID, &repeatOf)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		var existing struct {
			claim string
			rater string
		}
		err = tx.QueryRowContext(ctx, "SELECT claim_token, rater FROM ratings WHERE submission = ?", submission.ID).
			Scan(&existing.claim, &existing.rater)
		switch {
		case err == nil && existing.claim == token && existing.rater == rater:
			result.Rated, err = ratedCount(ctx, tx, batchID, rater)
			return err
		case err == nil:
			return invalid("submission id is already used")
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if ok, err := raterIn(ctx, tx, rater); err != nil || !ok {
			return errors.Join(err, ErrNotRater)
		}
		var previous sql.NullInt64
		err = tx.QueryRowContext(ctx, "SELECT id FROM ratings WHERE claim_token = ? AND superseded_at IS NULL", token).Scan(&previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		stamp := s.timestamp()
		if previous.Valid {
			var latest int64
			err := tx.QueryRowContext(ctx, `SELECT id FROM ratings WHERE batch_id = ? AND rater = ? AND superseded_at IS NULL
				ORDER BY id DESC LIMIT 1`, batchID, rater).Scan(&latest)
			if err != nil {
				return err
			}
			if latest != previous.Int64 {
				return ErrLocked
			}
			if _, err := tx.ExecContext(ctx, "UPDATE ratings SET superseded_at = ? WHERE id = ?", stamp, previous.Int64); err != nil {
				return err
			}
		} else if !repeatOf.Valid {
			var rated int
			err := tx.QueryRowContext(ctx, `SELECT count(*) FROM ratings
				WHERE item_id = ? AND rater = ? AND repeat_of IS NULL AND superseded_at IS NULL`, item, rater).Scan(&rated)
			if err != nil {
				return err
			}
			if rated > 0 {
				return ErrAlreadyRated
			}
		}
		var score any
		if submission.Score != nil {
			score = *submission.Score
		}
		var replaces any
		if previous.Valid {
			replaces = previous.Int64
		}
		var repeat any
		if repeatOf.Valid {
			repeat = repeatOf.Int64
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO ratings (claim_token, submission, item_id, batch_id, rater, kind, score, repeat_of, replaces,
			view_ms, viewport_width, viewport_height, display_width, display_height, pixel_ratio, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			token, submission.ID, item, batchID, rater, submission.Kind, score, repeat, replaces, submission.ViewMilliseconds,
			submission.ViewportWidth, submission.ViewportHeight, submission.DisplayWidth, submission.DisplayHeight, submission.PixelRatio, stamp)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE claims SET submitted = 1 WHERE token = ?", token); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE items SET covered = (SELECT count(*) FROM ratings r
			WHERE r.item_id = items.id AND r.kind = 'score' AND r.repeat_of IS NULL AND r.superseded_at IS NULL) WHERE id = ?`, item); err != nil {
			return err
		}
		result.Rated, err = ratedCount(ctx, tx, batchID, rater)
		return err
	})
	return result, err
}

func ratedCount(ctx context.Context, db querier, batchID, rater string) (int, error) {
	var rated int
	err := db.QueryRowContext(ctx, "SELECT count(*) FROM ratings WHERE batch_id = ? AND rater = ? AND superseded_at IS NULL",
		batchID, rater).Scan(&rated)
	return rated, err
}
