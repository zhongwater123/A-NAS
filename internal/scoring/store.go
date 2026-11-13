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
}

// Store is the module's state in dir.
type Store struct {
	db        *sql.DB
	dir       string
	photosDir string
	photos    Photos
	logger    *slog.Logger
	now       func() time.Time
	random    func() float64
	wake      chan struct{}
}

// Open opens or creates the store in dir, which must exist.
func Open(dir string, options Options) (*Store, error) {
	if options.Photos == nil || options.PhotosDir == "" {
		return nil, errors.New("scoring store needs the photos folder")
	}
	for _, sub := range []string{"previews", "thumbnails", "backups", "staging"} {
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
		logger: options.Logger, now: options.Now, random: options.Random, wake: make(chan struct{}, 1),
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

const schemaVersion = 1

const schema = `
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

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	switch {
	case version == schemaVersion:
		return nil
	case version > schemaVersion:
		return fmt.Errorf("scoring database version %d is newer than this module (%d)", version, schemaVersion)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
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
