// Package scoring keeps the photo scoring module's batches, claims and
// ratings (docs/specs/photo-scoring.md). Members chosen by an administrator
// score previews of the photos in one folder of the Shared space, each photo
// until a batch's number of raters scored it, and never see one another's
// scores. All state is one SQLite database in the module's store; previews
// are JPEG files beside it. Every change of a claim or rating is one
// transaction on the database's single connection, so two raters can never
// be handed the last open place of the same photo.
package scoring

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/zhongwater123/A-NAS/internal/filesystem"
	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

var (
	ErrNotFound = errors.New("not found")
	// ErrNotRater reports a member an administrator has not chosen.
	ErrNotRater = errors.New("not a rater")
	// ErrInvalid wraps a reason a request cannot be done as asked.
	ErrInvalid = errors.New("invalid request")
	// ErrLocked reports a rating that can no longer be changed: only a
	// rater's latest rating in a batch can be revised.
	ErrLocked = errors.New("rating can no longer be changed")
	// ErrAlreadyRated reports a late submission for a photo the rater has
	// since rated through another claim.
	ErrAlreadyRated = errors.New("photo already rated")
	// ErrNotReady reports a batch that is still importing.
	ErrNotReady = errors.New("batch is still importing")
	// ErrInUse reports deleting a batch that already has ratings.
	ErrInUse = errors.New("batch already has ratings")
	// ErrNameTaken refuses a custom 打分项 named like another.
	ErrNameTaken = errors.New("a scoring dimension has this name")
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

const (
	// LeaseDuration is how long a handed-out photo is held for a rater.
	LeaseDuration = 10 * time.Minute
	// MaxHeld is how many photos a rater holds at once: the shown one and
	// two preloaded.
	MaxHeld = 3
	// A rater's consistency is checked on their own earlier photos once
	// they scored repeatAfter in a batch: about repeatShare of the photos
	// handed out are ones they scored at least repeatGap photos before.
	repeatAfter = 40
	repeatShare = 0.05
	repeatGap   = 20
	// PreviewEdge and previewQuality shape the JPEG raters see;
	// ThumbnailEdge the small one on the ruler of their recent scores.
	PreviewEdge      = 2048
	previewQuality   = 85
	ThumbnailEdge    = 320
	thumbnailQuality = 80
	// MaxScore is 5.00 in hundredths: scores and reference scores are
	// kept as whole hundredths, 0 to MaxScore, so that no float rounding
	// reaches the data.
	MaxScore = 500
	// Recent is how many of a rater's latest scores the ruler shows.
	Recent = 12
	// DefaultMinLongEdge is the shortest longer side, in pixels, of a photo
	// to score; smaller ones are skipped on import. Anchors are exempt: they
	// are shown, never scored.
	DefaultMinLongEdge = 2400
	// maxOriginalBytes skips files no camera writes; the decode budget
	// bounds the pixels separately.
	maxOriginalBytes = 1 << 30
	// AnchorFolder holds a batch's reference photos, named after their
	// score, as in "3.5-湖边日落.jpg".
	AnchorFolder = "_锚点"
	backupsKept  = 14
)

// Photos reads the Shared folder the module unit binds in read-only.
type Photos interface {
	Scan(ctx context.Context, path string, skip filesystem.ScanSkip) ([]filesystem.ScanEntry, error)
	Open(ctx context.Context, path string) (*os.File, error)
}

// Options are the store's surroundings; tests replace the clock and the
// random source.
type Options struct {
	// PhotosDir is the bound folder; Photos reads below it.
	PhotosDir string
	Photos    Photos
	Logger    *slog.Logger
	Now       func() time.Time
	// Random returns a uniform float in [0, 1).
	Random func() float64
	// MinLongEdge replaces DefaultMinLongEdge when it is not 0.
	MinLongEdge int
}

// Store is the module's state in dir.
type Store struct {
	db          *sql.DB
	dir         string
	photosDir   string
	photos      Photos
	logger      *slog.Logger
	now         func() time.Time
	random      func() float64
	minLongEdge int
	wake        chan struct{}
}

// Open opens or creates the store in dir, which must exist.
func Open(dir string, options Options) (*Store, error) {
	if options.Photos == nil || options.PhotosDir == "" {
		return nil, errors.New("scoring store needs the photos folder")
	}
	for _, sub := range []string{"previews", "thumbnails", "full", "backups", "staging"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return nil, err
		}
	}
	db, err := sqlitedb.Open(filepath.Join(dir, "scoring.db"), sqlitedb.Options{Durable: true})
	if err != nil {
		return nil, err
	}
	store := &Store{
		db: db, dir: dir, photosDir: filepath.Clean(options.PhotosDir), photos: options.Photos,
		logger: options.Logger, now: options.Now, random: options.Random, minLongEdge: options.MinLongEdge, wake: make(chan struct{}, 1),
	}
	if store.minLongEdge == 0 {
		store.minLongEdge = DefaultMinLongEdge
	}
	if store.logger == nil {
		store.logger = slog.Default()
	}
	if store.now == nil {
		store.now = time.Now
	}
	if store.random == nil {
		store.random = cryptoFloat
	}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) timestamp() string { return sqlitedb.FormatTime(s.now()) }

// schema takes the database from each version to the next; its version is
// how many steps have run (sqlitedb.Schema). The first four were released
// without a Down; every later step has one.
var schema = sqlitedb.Schema{
	Name: "scoring.db",
	Steps: []sqlitedb.Step{
		{Up: schemaV1}, {Up: schemaV2}, {Up: schemaV3}, {Up: schemaV4},
		{Up: schemaV5, Down: schemaV5Down},
		{Up: schemaV6, Down: schemaV6Down},
	},
	WithoutDown: 4,
}

const schemaV1 = `
CREATE TABLE raters (
	account_id TEXT PRIMARY KEY,
	added_by TEXT NOT NULL,
	added_at TEXT NOT NULL
);
CREATE TABLE batches (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	source TEXT NOT NULL,
	required INTEGER NOT NULL CHECK (required BETWEEN 1 AND 10),
	rubric TEXT NOT NULL,
	state TEXT NOT NULL CHECK (state IN ('importing', 'open', 'closed')),
	scanned INTEGER NOT NULL DEFAULT 0,
	created_by TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE TABLE import_files (
	batch_id TEXT NOT NULL REFERENCES batches(id) ON DELETE CASCADE,
	path TEXT NOT NULL,
	anchor_score INTEGER CHECK (anchor_score BETWEEN 0 AND 500),
	status TEXT NOT NULL CHECK (status IN ('pending', 'done', 'skipped')),
	reason TEXT,
	PRIMARY KEY (batch_id, path)
);
CREATE INDEX import_files_pending ON import_files(batch_id, path) WHERE status = 'pending';
CREATE TABLE contents (
	sha256 TEXT PRIMARY KEY,
	width INTEGER NOT NULL,
	height INTEGER NOT NULL,
	preview_width INTEGER NOT NULL,
	preview_height INTEGER NOT NULL
);
CREATE TABLE items (
	id INTEGER PRIMARY KEY,
	batch_id TEXT NOT NULL REFERENCES batches(id) ON DELETE CASCADE,
	sha256 TEXT NOT NULL REFERENCES contents(sha256),
	anchor_score INTEGER CHECK (anchor_score BETWEEN 0 AND 500),
	covered INTEGER NOT NULL DEFAULT 0,
	shuffle INTEGER NOT NULL,
	UNIQUE (batch_id, sha256)
);
CREATE INDEX items_queue ON items(batch_id, covered, shuffle) WHERE anchor_score IS NULL;
CREATE TABLE item_paths (
	item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
	path TEXT NOT NULL,
	PRIMARY KEY (item_id, path)
);
CREATE TABLE claims (
	token TEXT PRIMARY KEY,
	item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
	batch_id TEXT NOT NULL,
	rater TEXT NOT NULL,
	repeat_of INTEGER,
	issued_at TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	submitted INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX claims_item_open ON claims(item_id, expires_at) WHERE submitted = 0;
CREATE INDEX claims_rater_open ON claims(batch_id, rater, expires_at) WHERE submitted = 0;
CREATE TABLE ratings (
	id INTEGER PRIMARY KEY,
	claim_token TEXT NOT NULL REFERENCES claims(token),
	submission TEXT NOT NULL UNIQUE,
	item_id INTEGER NOT NULL REFERENCES items(id),
	batch_id TEXT NOT NULL,
	rater TEXT NOT NULL,
	kind TEXT NOT NULL CHECK (kind IN ('score', 'skip', 'flag')),
	score INTEGER CHECK (score BETWEEN 0 AND 500),
	repeat_of INTEGER REFERENCES ratings(id),
	replaces INTEGER REFERENCES ratings(id),
	view_ms INTEGER NOT NULL CHECK (view_ms >= 0),
	viewport_width INTEGER,
	viewport_height INTEGER,
	display_width INTEGER,
	display_height INTEGER,
	pixel_ratio REAL,
	created_at TEXT NOT NULL,
	superseded_at TEXT,
	CHECK ((kind = 'score') = (score IS NOT NULL))
);
CREATE UNIQUE INDEX ratings_one_per_rater ON ratings(item_id, rater) WHERE superseded_at IS NULL AND repeat_of IS NULL;
CREATE INDEX ratings_item_rater ON ratings(item_id, rater);
CREATE INDEX ratings_batch_rater ON ratings(batch_id, rater, id);
CREATE INDEX ratings_claim ON ratings(claim_token);
CREATE INDEX ratings_repeat_of ON ratings(repeat_of) WHERE repeat_of IS NOT NULL;
`

// schemaV2 adds 打分项: a batch's dimensions, and a rating's scores on those
// other than overall, which stays in ratings.score. Every batch from before
// scores overall alone.
const schemaV2 = `
ALTER TABLE batches ADD COLUMN dimensions TEXT NOT NULL DEFAULT 'overall';
CREATE TABLE rating_scores (
	rating_id INTEGER NOT NULL REFERENCES ratings(id),
	dimension TEXT NOT NULL CHECK (dimension <> 'overall'),
	score INTEGER NOT NULL CHECK (score BETWEEN 0 AND 500),
	PRIMARY KEY (rating_id, dimension)
) WITHOUT ROWID;
`

// schemaV3 lets a batch score any 打分项 in the order the administrator
// chose them, custom ones among them: ratings.score keeps a rating's first
// 打分项, which was overall in every batch before, and rating_scores the
// others, overall among them when it is not first.
const schemaV3 = `
CREATE TABLE custom_dimensions (
	id TEXT PRIMARY KEY,
	number INTEGER NOT NULL UNIQUE CHECK (number > 0),
	name TEXT NOT NULL UNIQUE,
	description TEXT NOT NULL,
	created_by TEXT NOT NULL,
	created_at TEXT NOT NULL,
	CHECK (id = 'custom_' || number)
);
CREATE TABLE rating_scores_v3 (
	rating_id INTEGER NOT NULL REFERENCES ratings(id),
	dimension TEXT NOT NULL,
	score INTEGER NOT NULL CHECK (score BETWEEN 0 AND 500),
	PRIMARY KEY (rating_id, dimension)
) WITHOUT ROWID;
INSERT INTO rating_scores_v3 (rating_id, dimension, score) SELECT rating_id, dimension, score FROM rating_scores;
DROP TABLE rating_scores;
ALTER TABLE rating_scores_v3 RENAME TO rating_scores;
`

// schemaV4 gives every batch a scale, which every batch before had on the
// number line with two decimals, and lets a scale with decimals keep them
// from raters.
const schemaV4 = `
ALTER TABLE batches ADD COLUMN scale TEXT NOT NULL DEFAULT 'hundredths'
	CHECK (scale IN ('hundredths', 'tenths', 'whole', 'three'));
ALTER TABLE batches ADD COLUMN hide_decimals INTEGER NOT NULL DEFAULT 0
	CHECK (hide_decimals = 0 OR hide_decimals = 1 AND scale IN ('hundredths', 'tenths'));
`

// schemaV5 lets a batch be scored on five levels. SQLite cannot change a
// column's CHECK, so batches is rebuilt with every row as it was; the step
// runs with foreign keys off, so that items and anchors keep their batch.
//
// The same version names overall 总体 and adds 画质 and 情绪 (BuiltIn). A
// custom 打分项 an administrator gave one of those names keeps its ID and
// gets 自定义 added to its name, or its ID when that name is taken too, so
// that names stay unique as CreateDimension keeps them.
const schemaV5 = `
UPDATE custom_dimensions SET name = CASE
	WHEN EXISTS (SELECT 1 FROM custom_dimensions AS other WHERE other.name = custom_dimensions.name || '（自定义）')
		THEN custom_dimensions.name || '（' || custom_dimensions.id || '）'
	ELSE custom_dimensions.name || '（自定义）'
END
WHERE name IN ('总体', '画质', '情绪');
CREATE TABLE batches_v5 (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	source TEXT NOT NULL,
	required INTEGER NOT NULL CHECK (required BETWEEN 1 AND 10),
	rubric TEXT NOT NULL,
	state TEXT NOT NULL CHECK (state IN ('importing', 'open', 'closed')),
	scanned INTEGER NOT NULL DEFAULT 0,
	created_by TEXT NOT NULL,
	created_at TEXT NOT NULL,
	dimensions TEXT NOT NULL DEFAULT 'overall',
	scale TEXT NOT NULL DEFAULT 'hundredths'
		CHECK (scale IN ('hundredths', 'tenths', 'whole', 'three', 'five')),
	hide_decimals INTEGER NOT NULL DEFAULT 0
		CHECK (hide_decimals = 0 OR hide_decimals = 1 AND scale IN ('hundredths', 'tenths'))
);
INSERT INTO batches_v5 (id, name, source, required, rubric, state, scanned, created_by, created_at, dimensions, scale, hide_decimals)
	SELECT id, name, source, required, rubric, state, scanned, created_by, created_at, dimensions, scale, hide_decimals FROM batches;
DROP TABLE batches;
ALTER TABLE batches_v5 RENAME TO batches;
`

// schemaV5Down takes batches back to version 4's scales. A five-level batch
// becomes whole points, whose scores include all of its own; renamed custom
// 打分项 keep their new names, which version 4 accepts as well. No build
// runs it yet: the builds of version 4 migrate without sqlitedb.Schema and
// refuse a newer database (migrate), but sqlitedb wants every step after
// the released ones reversible, and the stored Down is what a build with
// this schema and fewer steps would use.
const schemaV5Down = `
CREATE TABLE batches_v4 (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	source TEXT NOT NULL,
	required INTEGER NOT NULL CHECK (required BETWEEN 1 AND 10),
	rubric TEXT NOT NULL,
	state TEXT NOT NULL CHECK (state IN ('importing', 'open', 'closed')),
	scanned INTEGER NOT NULL DEFAULT 0,
	created_by TEXT NOT NULL,
	created_at TEXT NOT NULL,
	dimensions TEXT NOT NULL DEFAULT 'overall',
	scale TEXT NOT NULL DEFAULT 'hundredths'
		CHECK (scale IN ('hundredths', 'tenths', 'whole', 'three')),
	hide_decimals INTEGER NOT NULL DEFAULT 0
		CHECK (hide_decimals = 0 OR hide_decimals = 1 AND scale IN ('hundredths', 'tenths'))
);
INSERT INTO batches_v4 (id, name, source, required, rubric, state, scanned, created_by, created_at, dimensions, scale, hide_decimals)
	SELECT id, name, source, required, rubric, state, scanned, created_by, created_at, dimensions,
		CASE scale WHEN 'five' THEN 'whole' ELSE scale END, hide_decimals FROM batches;
DROP TABLE batches;
ALTER TABLE batches_v4 RENAME TO batches;
`

// schemaV6 goes with the handbook's 打分项 of 2026-11-26: it adds 主体 and
// names light_color 光影 and narrative 叙事性 (BuiltIn). A custom 打分项 an
// administrator gave one of those names keeps its ID and gets 自定义 added to
// its name, or its ID when that name is taken too, as in version 5.
const schemaV6 = `
UPDATE custom_dimensions SET name = CASE
	WHEN EXISTS (SELECT 1 FROM custom_dimensions AS other WHERE other.name = custom_dimensions.name || '（自定义）')
		THEN custom_dimensions.name || '（' || custom_dimensions.id || '）'
	ELSE custom_dimensions.name || '（自定义）'
END
WHERE name IN ('主体', '光影', '叙事性');
`

// schemaV6Down leaves the renamed custom 打分项 their new names, which
// version 5 accepts as well; it changes nothing.
const schemaV6Down = `
UPDATE custom_dimensions SET name = name WHERE 0;
`

// migrate brings the database to the schema's version, copying it first when
// it has to be upgraded. Builds of version 4 and before migrated by hand and
// refuse a database newer than theirs instead of taking it back, so going
// back to one of them means restoring that copy
// (docs/runbooks/install-and-remove-photo-scoring.md).
func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 0 && version < len(schema.Steps) {
		name := fmt.Sprintf("scoring-%s-v%d.db", s.now().UTC().Format("2006-01-02"), version)
		if err := s.copyTo(ctx, filepath.Join(s.dir, "backups", name)); err != nil {
			return fmt.Errorf("copy scoring.db before upgrading it from version %d: %w", version, err)
		}
	}
	return sqlitedb.Migrate(ctx, s.db, schema)
}

// each runs scan for every row of query and closes the rows.
func each(ctx context.Context, db querier, query string, args []any, scan func(*sql.Rows) error) error {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// within runs change in one transaction.
func (s *Store) within(ctx context.Context, change func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := change(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func randomToken(bytes int) string {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buffer)
}

func cryptoFloat() float64 {
	n, err := rand.Int(rand.Reader, big.NewInt(1<<53))
	if err != nil {
		panic(err)
	}
	return float64(n.Int64()) / float64(1<<53)
}

// randomShuffle orders a batch's photos; claims walk it from a random point.
func (s *Store) randomShuffle() int64 { return int64(s.random() * (1 << 53)) }
