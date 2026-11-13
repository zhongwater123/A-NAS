package scoring

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// IsRater reports whether an administrator chose account as a rater.
func (s *Store) IsRater(ctx context.Context, account string) (bool, error) {
	var found int
	err := s.db.QueryRowContext(ctx, "SELECT 1 FROM raters WHERE account_id = ?", account).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Raters lists the chosen accounts.
func (s *Store) Raters(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT account_id FROM raters ORDER BY added_at, account_id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	accounts := []string{}
	for rows.Next() {
		var account string
		if err := rows.Scan(&account); err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}

// SetRater adds or removes account. Removing keeps every rating it made;
// its unsubmitted photos return to others when their leases end.
func (s *Store) SetRater(ctx context.Context, account, by string, rater bool) error {
	if account == "" || len(account) > 128 {
		return invalid("account is empty")
	}
	if !rater {
		_, err := s.db.ExecContext(ctx, "DELETE FROM raters WHERE account_id = ?", account)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO raters (account_id, added_by, added_at) VALUES (?, ?, ?) ON CONFLICT (account_id) DO NOTHING",
		account, by, s.timestamp())
	return err
}

// Sources lists the folders in the bound folder that can become a batch:
// every directory not starting with "_" or ".".
func (s *Store) Sources(_ context.Context) ([]string, error) {
	entries, err := os.ReadDir(s.photosDir)
	if err != nil {
		return nil, err
	}
	sources := []string{}
	for _, entry := range entries {
		if entry.IsDir() && validSource(entry.Name()) {
			sources = append(sources, entry.Name())
		}
	}
	slices.Sort(sources)
	return sources, nil
}

func validSource(name string) bool {
	return name != "" && !strings.HasPrefix(name, "_") && !strings.HasPrefix(name, ".") &&
		!strings.ContainsAny(name, "/\\\x00") && len(name) <= 255 && utf8.ValidString(name)
}

// BatchInput is what an administrator fills in for a new batch.
type BatchInput struct {
	Name     string
	Source   string
	Required int
	Rubric   string
	// Dimensions are the 打分项 to score, overall among them; none is
	// overall alone.
	Dimensions []string
}

const (
	maxNameRunes   = 40
	maxRubricRunes = 2000
)

func (input *BatchInput) normalize() error {
	input.Name = strings.TrimSpace(input.Name)
	input.Rubric = strings.TrimSpace(input.Rubric)
	switch {
	case input.Name == "" || utf8.RuneCountInString(input.Name) > maxNameRunes:
		return invalid("name must have 1 to %d characters", maxNameRunes)
	case !validSource(input.Source):
		return invalid("choose a folder of the scoring folder")
	case input.Required < 1 || input.Required > 10:
		return invalid("each photo needs 1 to 10 raters")
	case utf8.RuneCountInString(input.Rubric) > maxRubricRunes:
		return invalid("the scoring guide has at most %d characters", maxRubricRunes)
	}
	dimensions, err := normalizeDimensions(input.Dimensions)
	input.Dimensions = dimensions
	return err
}

// Batch is a batch as administrators see it.
type Batch struct {
	ID         string
	Name       string
	Source     string
	Required   int
	Rubric     string
	Dimensions []string
	State      string
	CreatedBy  string
	CreatedAt  time.Time
	// Import counts the files found in the source and how far preview
	// rendering is; Files is 0 until the folder was scanned.
	Files, Imported, Skipped int
	// SkippedBy counts the skipped files by reason.
	SkippedBy map[string]int
	// Photos excludes anchors; Complete have Required raters, Started some.
	Photos, Anchors, Complete, Started, Flagged int
	// Answers counts every answer; with one, the dimensions are fixed.
	Answers int
}

// CreateBatch starts importing input.Source as a new batch.
func (s *Store) CreateBatch(ctx context.Context, by string, input BatchInput) (Batch, error) {
	if err := input.normalize(); err != nil {
		return Batch{}, err
	}
	if info, err := os.Stat(filepath.Join(s.photosDir, input.Source)); err != nil || !info.IsDir() {
		return Batch{}, invalid("folder %q is not in the scoring folder", input.Source)
	}
	id := "b" + randomToken(6)
	_, err := s.db.ExecContext(ctx, `INSERT INTO batches (id, name, source, required, rubric, dimensions, state, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'importing', ?, ?)`, id, input.Name, input.Source, input.Required, input.Rubric,
		joinDimensions(input.Dimensions), by, s.timestamp())
	if err != nil {
		return Batch{}, err
	}
	s.Wake()
	return s.Batch(ctx, id)
}

// BatchPatch changes a batch; nil fields stay.
type BatchPatch struct {
	Name     *string
	Rubric   *string
	Required *int
	// Dimensions change only while the batch has no answer: an answer
	// scores the dimensions there were.
	Dimensions []string
	// State is "open" or "closed"; a batch that is importing opens itself.
	State *string
}

// UpdateBatch applies patch.
func (s *Store) UpdateBatch(ctx context.Context, id string, patch BatchPatch) (Batch, error) {
	current, err := s.Batch(ctx, id)
	if err != nil {
		return Batch{}, err
	}
	input := BatchInput{Name: current.Name, Source: current.Source, Required: current.Required, Rubric: current.Rubric, Dimensions: current.Dimensions}
	if patch.Name != nil {
		input.Name = *patch.Name
	}
	if patch.Rubric != nil {
		input.Rubric = *patch.Rubric
	}
	if patch.Required != nil {
		input.Required = *patch.Required
	}
	if patch.Dimensions != nil {
		input.Dimensions = patch.Dimensions
	}
	if err := input.normalize(); err != nil {
		return Batch{}, err
	}
	if !slices.Equal(input.Dimensions, current.Dimensions) && current.Answers > 0 {
		return Batch{}, ErrInUse
	}
	state := current.State
	if patch.State != nil {
		switch {
		case *patch.State != "open" && *patch.State != "closed":
			return Batch{}, invalid("state must be open or closed")
		case current.State == "importing":
			return Batch{}, ErrNotReady
		}
		state = *patch.State
	}
	// The answer check and the change are one statement, so that a first
	// answer cannot slip in between and score dimensions that are gone.
	result, err := s.db.ExecContext(ctx, `UPDATE batches SET name = ?, rubric = ?, required = ?, state = ?, dimensions = ?
		WHERE id = ? AND (dimensions = ? OR NOT EXISTS (SELECT 1 FROM ratings WHERE batch_id = ?))`,
		input.Name, input.Rubric, input.Required, state, joinDimensions(input.Dimensions), id, joinDimensions(input.Dimensions), id)
	if err != nil {
		return Batch{}, err
	}
	if changed, err := result.RowsAffected(); err != nil || changed == 0 {
		return Batch{}, errors.Join(err, ErrInUse)
	}
	return s.Batch(ctx, id)
}

// DeleteBatch removes a batch nobody has rated yet, such as one made from
// the wrong folder. Previews no other batch uses are removed with it.
func (s *Store) DeleteBatch(ctx context.Context, id string) error {
	var orphans []string
	err := s.within(ctx, func(tx *sql.Tx) error {
		var found, rated int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM batches WHERE id = ?", id).Scan(&found); err != nil {
			return err
		}
		if found == 0 {
			return ErrNotFound
		}
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM ratings WHERE batch_id = ?", id).Scan(&rated); err != nil {
			return err
		}
		if rated > 0 {
			return ErrInUse
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM batches WHERE id = ?", id); err != nil {
			return err
		}
		err := each(ctx, tx, "SELECT sha256 FROM contents WHERE sha256 NOT IN (SELECT sha256 FROM items)", nil, func(rows *sql.Rows) error {
			var sha string
			if err := rows.Scan(&sha); err != nil {
				return err
			}
			orphans = append(orphans, sha)
			return nil
		})
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM contents WHERE sha256 NOT IN (SELECT sha256 FROM items)")
		return err
	})
	if err != nil {
		return err
	}
	for _, sha := range orphans {
		for _, name := range []string{s.previewPath(sha), s.thumbnailPath(sha)} {
			if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
				s.logger.WarnContext(ctx, "removing an unused preview failed", "error", err)
			}
		}
	}
	return nil
}

const batchColumns = `b.id, b.name, b.source, b.required, b.rubric, b.dimensions, b.state, b.created_by, b.created_at,
	(SELECT count(*) FROM ratings r WHERE r.batch_id = b.id),
	(SELECT count(*) FROM import_files f WHERE f.batch_id = b.id),
	(SELECT count(*) FROM import_files f WHERE f.batch_id = b.id AND f.status = 'done'),
	(SELECT count(*) FROM import_files f WHERE f.batch_id = b.id AND f.status = 'skipped'),
	(SELECT count(*) FROM items i WHERE i.batch_id = b.id AND i.anchor_score IS NULL),
	(SELECT count(*) FROM items i WHERE i.batch_id = b.id AND i.anchor_score IS NOT NULL),
	(SELECT count(*) FROM items i WHERE i.batch_id = b.id AND i.anchor_score IS NULL AND i.covered >= b.required),
	(SELECT count(*) FROM items i WHERE i.batch_id = b.id AND i.anchor_score IS NULL AND i.covered > 0 AND i.covered < b.required),
	(SELECT count(DISTINCT r.item_id) FROM ratings r WHERE r.batch_id = b.id AND r.kind = 'flag' AND r.superseded_at IS NULL)`

func scanBatch(row interface{ Scan(...any) error }) (Batch, error) {
	var batch Batch
	var created, dimensions string
	err := row.Scan(&batch.ID, &batch.Name, &batch.Source, &batch.Required, &batch.Rubric, &dimensions, &batch.State, &batch.CreatedBy, &created,
		&batch.Answers, &batch.Files, &batch.Imported, &batch.Skipped, &batch.Photos, &batch.Anchors, &batch.Complete, &batch.Started, &batch.Flagged)
	if err != nil {
		return Batch{}, err
	}
	batch.CreatedAt, _ = time.Parse(sqlitedb.TimeLayout, created)
	batch.Dimensions = splitDimensions(dimensions)
	batch.SkippedBy = map[string]int{}
	return batch, nil
}

// skippedBy fills in the batches' skipped files by reason.
func (s *Store) skippedBy(ctx context.Context, batches []Batch) error {
	byID := make(map[string]*Batch, len(batches))
	for i := range batches {
		byID[batches[i].ID] = &batches[i]
	}
	return each(ctx, s.db, `SELECT batch_id, coalesce(reason, ''), count(*) FROM import_files WHERE status = 'skipped'
		GROUP BY batch_id, reason`, nil, func(rows *sql.Rows) error {
		var id, reason string
		var count int
		if err := rows.Scan(&id, &reason, &count); err != nil {
			return err
		}
		if batch := byID[id]; batch != nil {
			batch.SkippedBy[reason] = count
		}
		return nil
	})
}

// Batch returns one batch.
func (s *Store) Batch(ctx context.Context, id string) (Batch, error) {
	batch, err := scanBatch(s.db.QueryRowContext(ctx, "SELECT "+batchColumns+" FROM batches b WHERE b.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Batch{}, ErrNotFound
	}
	if err != nil {
		return Batch{}, err
	}
	batches := []Batch{batch}
	err = s.skippedBy(ctx, batches)
	return batches[0], err
}

// Batches lists every batch, newest first.
func (s *Store) Batches(ctx context.Context) ([]Batch, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+batchColumns+" FROM batches b ORDER BY b.created_at DESC, b.id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	batches := []Batch{}
	for rows.Next() {
		batch, err := scanBatch(rows)
		if err != nil {
			return nil, err
		}
		batches = append(batches, batch)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// The single connection is free again only once the rows are closed.
	_ = rows.Close()
	return batches, s.skippedBy(ctx, batches)
}

// RaterBatch is an open batch as a rater sees it: nothing about others.
type RaterBatch struct {
	ID         string
	Name       string
	Rubric     string
	Dimensions []string
	// Rated counts the rater's own ratings, including skips and marks.
	Rated int
	// Finished is set when no photo of the batch is left for this rater.
	Finished bool
}

// OpenBatches lists the open batches for a rater.
func (s *Store) OpenBatches(ctx context.Context, rater string) ([]RaterBatch, error) {
	if ok, err := s.IsRater(ctx, rater); err != nil || !ok {
		return nil, errors.Join(err, ErrNotRater)
	}
	type open struct {
		RaterBatch
		required int
	}
	var batches []open
	err := each(ctx, s.db, `SELECT b.id, b.name, b.rubric, b.dimensions, b.required,
		(SELECT count(*) FROM ratings r WHERE r.batch_id = b.id AND r.rater = ? AND r.superseded_at IS NULL)
		FROM batches b WHERE b.state = 'open' ORDER BY b.created_at DESC, b.id`, []any{rater}, func(rows *sql.Rows) error {
		var batch open
		var dimensions string
		if err := rows.Scan(&batch.ID, &batch.Name, &batch.Rubric, &dimensions, &batch.required, &batch.Rated); err != nil {
			return err
		}
		batch.Dimensions = splitDimensions(dimensions)
		batches = append(batches, batch)
		return nil
	})
	if err != nil {
		return nil, err
	}
	result := []RaterBatch{}
	now := sqlitedb.FormatTime(s.now())
	for _, batch := range batches {
		item, err := nextItem(ctx, s.db, batch.ID, rater, batch.required, now, 0)
		if err != nil {
			return nil, err
		}
		batch.Finished = item == 0
		result = append(result, batch.RaterBatch)
	}
	return result, nil
}

// Anchor is a reference photo shown before scoring.
type Anchor struct {
	ID            int64
	Score         int
	Width, Height int
}

// Anchors lists a batch's reference photos, lowest score first, to raters
// and administrators.
func (s *Store) Anchors(ctx context.Context, batchID string) ([]Anchor, error) {
	if _, err := s.Batch(ctx, batchID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT i.id, i.anchor_score, c.preview_width, c.preview_height
		FROM items i JOIN contents c ON c.sha256 = i.sha256
		WHERE i.batch_id = ? AND i.anchor_score IS NOT NULL ORDER BY i.anchor_score, i.id`, batchID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	anchors := []Anchor{}
	for rows.Next() {
		var anchor Anchor
		if err := rows.Scan(&anchor.ID, &anchor.Score, &anchor.Width, &anchor.Height); err != nil {
			return nil, err
		}
		anchors = append(anchors, anchor)
	}
	return anchors, rows.Err()
}

// AnchorPreview returns the preview file of one of a batch's anchors.
func (s *Store) AnchorPreview(ctx context.Context, batchID string, anchorID int64) (string, error) {
	var sha string
	err := s.db.QueryRowContext(ctx,
		"SELECT sha256 FROM items WHERE id = ? AND batch_id = ? AND anchor_score IS NOT NULL", anchorID, batchID).Scan(&sha)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return s.previewPath(sha), nil
}

func (s *Store) previewPath(sha string) string {
	return filepath.Join(s.dir, "previews", sha[:2], sha+".jpg")
}

func (s *Store) thumbnailPath(sha string) string {
	return filepath.Join(s.dir, "thumbnails", sha[:2], sha+".jpg")
}
