package scoring

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"hash"
	"io"
	"slices"
	"strings"
	"time"
)

// ExportFormat names the layout of an export; a change of meaning gets a
// new one.
const ExportFormat = "a-nas-photo-scoring/1"

// Person is what the export says about a rater's account.
type Person struct {
	Username string `json:"username,omitempty"`
	Nickname string `json:"nickname,omitempty"`
}

type exportImage struct {
	ID          string   `json:"id"`
	Paths       []string `json:"paths"`
	Width       int      `json:"width"`
	Height      int      `json:"height"`
	AnchorScore *float64 `json:"anchorScore,omitempty"`
}

type exportRating struct {
	ID         int64    `json:"id"`
	Image      string   `json:"image"`
	Rater      string   `json:"rater"`
	Kind       string   `json:"kind"`
	Score      *float64 `json:"score,omitempty"`
	RepeatOf   *int64   `json:"repeatOf,omitempty"`
	Replaces   *int64   `json:"replaces,omitempty"`
	Superseded bool     `json:"superseded"`
	ViewMs     int      `json:"viewMs"`
	Viewport   *size    `json:"viewport,omitempty"`
	Display    *size    `json:"display,omitempty"`
	PixelRatio *float64 `json:"pixelRatio,omitempty"`
	CreatedAt  string   `json:"createdAt"`
}

type size struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type exportRater struct {
	ID string `json:"id"`
	Person
}

type exportFile struct {
	Lines  int    `json:"lines"`
	SHA256 string `json:"sha256"`
}

type exportManifest struct {
	Format     string                `json:"format"`
	ExportedAt string                `json:"exportedAt"`
	Module     string                `json:"module"`
	Batch      exportBatch           `json:"batch"`
	Files      map[string]exportFile `json:"files"`
}

type exportBatch struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Source    string `json:"source"`
	Required  int    `json:"required"`
	Rubric    string `json:"rubric"`
	State     string `json:"state"`
	CreatedAt string `json:"createdAt"`
}

// jsonLines writes one JSON value per line into a zip entry and keeps its
// line count and SHA-256 for the manifest.
type jsonLines struct {
	encoder *json.Encoder
	hash    hash.Hash
	lines   int
}

func newJSONLines(archive *zip.Writer, name string, modified time.Time) (*jsonLines, error) {
	entry, err := archive.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: modified})
	if err != nil {
		return nil, err
	}
	digest := sha256.New()
	encoder := json.NewEncoder(io.MultiWriter(entry, digest))
	encoder.SetEscapeHTML(false)
	return &jsonLines{encoder: encoder, hash: digest}, nil
}

func (l *jsonLines) write(value any) error {
	l.lines++
	return l.encoder.Encode(value)
}

func (l *jsonLines) file() exportFile {
	return exportFile{Lines: l.lines, SHA256: hex.EncodeToString(l.hash.Sum(nil))}
}

// Export writes a batch as a zip of JSON lines (docs/specs/photo-scoring.md,
// 导出): every rating including superseded ones, every photo with its paths
// in the source folder, and the raters. people names the accounts it knows.
// It reads in one transaction on the store's only connection, so w should be
// a local file (StagingDir), not a slow client.
func (s *Store) Export(ctx context.Context, batchID string, w io.Writer, people map[string]Person, module string) error {
	batch, err := s.Batch(ctx, batchID)
	if err != nil {
		return err
	}
	// One read transaction, so that ratings arriving meanwhile do not
	// make the files disagree.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now().UTC()
	archive := zip.NewWriter(w)
	files := map[string]exportFile{}

	images, err := newJSONLines(archive, "images.jsonl", now)
	if err != nil {
		return err
	}
	if err := writeImages(ctx, tx, batchID, images); err != nil {
		return err
	}
	files["images.jsonl"] = images.file()
	ratings, err := newJSONLines(archive, "ratings.jsonl", now)
	if err != nil {
		return err
	}
	raterSet := map[string]bool{}
	if err := writeRatings(ctx, tx, batchID, ratings, raterSet); err != nil {
		return err
	}
	files["ratings.jsonl"] = ratings.file()
	err = each(ctx, tx, "SELECT account_id FROM raters", nil, func(rows *sql.Rows) error {
		var account string
		if err := rows.Scan(&account); err != nil {
			return err
		}
		raterSet[account] = true
		return nil
	})
	if err != nil {
		return err
	}
	raters, err := newJSONLines(archive, "raters.jsonl", now)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(raterSet))
	for id := range raterSet {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if err := raters.write(exportRater{ID: id, Person: people[id]}); err != nil {
			return err
		}
	}
	files["raters.jsonl"] = raters.file()

	manifest, err := archive.CreateHeader(&zip.FileHeader{Name: "manifest.json", Method: zip.Deflate, Modified: now})
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(manifest)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(exportManifest{
		Format: ExportFormat, ExportedAt: now.Format(time.RFC3339), Module: module, Files: files,
		Batch: exportBatch{ID: batch.ID, Name: batch.Name, Source: batch.Source, Required: batch.Required, Rubric: batch.Rubric,
			State: batch.State, CreatedAt: batch.CreatedAt.UTC().Format(time.RFC3339)},
	}); err != nil {
		return err
	}
	return archive.Close()
}

// writeImages writes every photo of the batch, anchors included, with all
// its paths in the source folder.
func writeImages(ctx context.Context, tx *sql.Tx, batchID string, images *jsonLines) error {
	return each(ctx, tx, `SELECT i.sha256, c.width, c.height, i.anchor_score,
		(SELECT group_concat(p.path, char(10)) FROM (SELECT path FROM item_paths WHERE item_id = i.id ORDER BY path) p)
		FROM items i JOIN contents c ON c.sha256 = i.sha256 WHERE i.batch_id = ? ORDER BY i.sha256`, []any{batchID}, func(rows *sql.Rows) error {
		var image exportImage
		var anchor sql.NullInt64
		var paths sql.NullString
		if err := rows.Scan(&image.ID, &image.Width, &image.Height, &anchor, &paths); err != nil {
			return err
		}
		image.Paths = []string{}
		if paths.Valid {
			image.Paths = strings.Split(paths.String, "\n")
		}
		if anchor.Valid {
			score := points(int(anchor.Int64))
			image.AnchorScore = &score
		}
		return images.write(image)
	})
}

// writeRatings writes every answer of the batch, superseded ones included,
// and notes who gave them.
func writeRatings(ctx context.Context, tx *sql.Tx, batchID string, ratings *jsonLines, raters map[string]bool) error {
	return each(ctx, tx, `SELECT r.id, i.sha256, r.rater, r.kind, r.score, r.repeat_of, r.replaces, r.superseded_at IS NOT NULL,
		r.view_ms, r.viewport_width, r.viewport_height, r.display_width, r.display_height, r.pixel_ratio, r.created_at
		FROM ratings r JOIN items i ON i.id = r.item_id WHERE r.batch_id = ? ORDER BY r.id`, []any{batchID}, func(rows *sql.Rows) error {
		var rating exportRating
		var score, repeatOf, replaces, viewportWidth, viewportHeight, displayWidth, displayHeight sql.NullInt64
		var ratio sql.NullFloat64
		if err := rows.Scan(&rating.ID, &rating.Image, &rating.Rater, &rating.Kind, &score, &repeatOf, &replaces, &rating.Superseded,
			&rating.ViewMs, &viewportWidth, &viewportHeight, &displayWidth, &displayHeight, &ratio, &rating.CreatedAt); err != nil {
			return err
		}
		if score.Valid {
			value := points(int(score.Int64))
			rating.Score = &value
		}
		if repeatOf.Valid {
			rating.RepeatOf = &repeatOf.Int64
		}
		if replaces.Valid {
			rating.Replaces = &replaces.Int64
		}
		if viewportWidth.Valid && viewportWidth.Int64 > 0 {
			rating.Viewport = &size{int(viewportWidth.Int64), int(viewportHeight.Int64)}
		}
		if displayWidth.Valid && displayWidth.Int64 > 0 {
			rating.Display = &size{int(displayWidth.Int64), int(displayHeight.Int64)}
		}
		if ratio.Valid && ratio.Float64 > 0 {
			rating.PixelRatio = &ratio.Float64
		}
		raters[rating.Rater] = true
		return ratings.write(rating)
	})
}
