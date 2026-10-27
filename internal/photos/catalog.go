package photos

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"
)

// migrations are applied in order and recorded in PRAGMA user_version. The
// Catalog lives on the data volume and is restored from backups taken by older
// builds, so an applied migration is never edited; add a new one instead.
var migrations = []string{
	`
CREATE TABLE libraries (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('private', 'shared')),
    owner_user_id TEXT,
    created_at TEXT NOT NULL,
    CHECK ((kind = 'private') = (owner_user_id IS NOT NULL))
);
CREATE UNIQUE INDEX libraries_private_owner ON libraries(owner_user_id) WHERE kind = 'private';
CREATE UNIQUE INDEX libraries_single_shared ON libraries(kind) WHERE kind = 'shared';

CREATE TABLE directories (
    id TEXT PRIMARY KEY,
    library_id TEXT NOT NULL REFERENCES libraries(id),
    parent_id TEXT REFERENCES directories(id),
    name TEXT NOT NULL,
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE UNIQUE INDEX directories_sibling_name ON directories(library_id, IFNULL(parent_id, ''), name);

-- One row per immutable original; id is the lowercase hex SHA-256 of its bytes.
CREATE TABLE objects (
    id TEXT PRIMARY KEY,
    size_bytes INTEGER NOT NULL,
    media_type TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE assets (
    id TEXT PRIMARY KEY,
    library_id TEXT NOT NULL REFERENCES libraries(id),
    directory_id TEXT REFERENCES directories(id) ON DELETE SET NULL,
    object_id TEXT NOT NULL REFERENCES objects(id),
    name TEXT NOT NULL,
    uploaded_by TEXT NOT NULL,
    imported_at TEXT NOT NULL,
    trashed_at TEXT,
    trashed_by TEXT,
    purge_after TEXT,
    CHECK ((trashed_at IS NULL) = (purge_after IS NULL)),
    CHECK ((trashed_at IS NULL) = (trashed_by IS NULL))
);
CREATE INDEX assets_timeline ON assets(library_id, imported_at, id) WHERE trashed_at IS NULL;
CREATE INDEX assets_directory ON assets(library_id, directory_id);
CREATE INDEX assets_object ON assets(object_id, library_id);
CREATE INDEX assets_purge ON assets(purge_after) WHERE purge_after IS NOT NULL;

CREATE TABLE audit_events (
    id TEXT PRIMARY KEY,
    actor_user_id TEXT NOT NULL,
    action TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    occurred_at TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT ''
);
`,
	// 2: header metadata, capture-time ordering, derived files and the
	// durable media job queue. Objects imported before this migration keep
	// unknown metadata but still get thumbnails.
	`
ALTER TABLE objects ADD COLUMN width INTEGER NOT NULL DEFAULT 0;
ALTER TABLE objects ADD COLUMN height INTEGER NOT NULL DEFAULT 0;
ALTER TABLE objects ADD COLUMN orientation INTEGER NOT NULL DEFAULT 1;
ALTER TABLE assets ADD COLUMN taken_at TEXT;
DROP INDEX assets_timeline;
CREATE INDEX assets_timeline ON assets(library_id, IFNULL(taken_at, imported_at), id) WHERE trashed_at IS NULL;

CREATE TABLE derived_files (
    object_id TEXT NOT NULL REFERENCES objects(id) ON DELETE CASCADE,
    derivation TEXT NOT NULL,
    media_type TEXT NOT NULL,
    width INTEGER NOT NULL,
    height INTEGER NOT NULL,
    size_bytes INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (object_id, derivation)
);

CREATE TABLE jobs (
    object_id TEXT NOT NULL REFERENCES objects(id) ON DELETE CASCADE,
    derivation TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('pending', 'running', 'failed')),
    attempts INTEGER NOT NULL DEFAULT 0,
    not_before TEXT NOT NULL,
    lease_until TEXT,
    error_class TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    PRIMARY KEY (object_id, derivation)
);
CREATE INDEX jobs_ready ON jobs(state, not_before);

INSERT INTO jobs(object_id, derivation, state, not_before, created_at)
SELECT id, 'thumbnail/v1', 'pending', created_at, created_at FROM objects;
`,
	// 3: the owner's username, which cross-member duplicate hints show.
	`
ALTER TABLE libraries ADD COLUMN owner_name TEXT NOT NULL DEFAULT '';
`,
	// 4: image vectors from the AI Worker, one per content object and
	// derivation, with the model that produced them. Originals that already
	// have thumbnails are queued for a vector.
	`
CREATE TABLE embeddings (
    object_id TEXT NOT NULL REFERENCES objects(id) ON DELETE CASCADE,
    derivation TEXT NOT NULL,
    model TEXT NOT NULL,
    vector BLOB NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (object_id, derivation)
);

INSERT INTO jobs(object_id, derivation, state, not_before, created_at)
SELECT object_id, 'embedding/v1', 'pending', created_at, created_at FROM derived_files WHERE derivation = 'thumbnail/v1'
ON CONFLICT(object_id, derivation) DO NOTHING;
`,
	// 5: text vectors the AI Worker made for label texts, per model, so
	// labels survive a restart without the Worker.
	`
CREATE TABLE query_vectors (
    model TEXT NOT NULL,
    text TEXT NOT NULL,
    vector BLOB NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (model, text)
);
`,
	// 6: user metadata. Albums group photo assets of their own library;
	// user tags and AI corrections belong to one photo asset. All of it is
	// user data, kept apart from derived results and never rebuilt.
	`
CREATE TABLE albums (
    id TEXT PRIMARY KEY,
    library_id TEXT NOT NULL REFERENCES libraries(id),
    name TEXT NOT NULL,
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (library_id, name)
);

CREATE TABLE album_assets (
    album_id TEXT NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
    asset_id TEXT NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    added_by TEXT NOT NULL,
    added_at TEXT NOT NULL,
    PRIMARY KEY (album_id, asset_id)
);
CREATE INDEX album_assets_by_asset ON album_assets(asset_id);

CREATE TABLE user_tags (
    asset_id TEXT NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (asset_id, name)
);

CREATE TABLE ai_tag_corrections (
    asset_id TEXT NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    label_id TEXT NOT NULL,
    verdict TEXT NOT NULL CHECK (verdict IN ('hidden')),
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (asset_id, label_id)
);
CREATE INDEX ai_tag_corrections_by_label ON ai_tag_corrections(label_id);
`,
	// 7: the objects the detector found in each content object
	// (docs/architecture/photo-objects.md), with the model that found them;
	// an empty list means the detector found nothing. Originals that already
	// have thumbnails are queued for detection.
	`
CREATE TABLE detections (
    object_id TEXT NOT NULL REFERENCES objects(id) ON DELETE CASCADE,
    derivation TEXT NOT NULL,
    model TEXT NOT NULL,
    boxes BLOB NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (object_id, derivation)
);

INSERT INTO jobs(object_id, derivation, state, not_before, created_at)
SELECT object_id, 'objects/v1', 'pending', created_at, created_at FROM derived_files WHERE derivation = 'thumbnail/v1'
ON CONFLICT(object_id, derivation) DO NOTHING;
`,
	`ALTER TABLE assets ADD COLUMN motion_object_id TEXT REFERENCES objects(id);
ALTER TABLE assets ADD COLUMN motion_name TEXT NOT NULL DEFAULT '';
ALTER TABLE assets ADD COLUMN motion_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE assets ADD COLUMN motion_duration REAL NOT NULL DEFAULT 0;
CREATE INDEX assets_by_motion ON assets(motion_object_id);`,
	// A still's Apple Live Photo identifier, so that its MOV can follow in a
	// later upload: NULL while unread (objects imported before this
	// migration), '' when the still has none.
	`ALTER TABLE objects ADD COLUMN live_id TEXT;
CREATE INDEX objects_by_live_id ON objects(live_id) WHERE live_id IS NOT NULL AND live_id <> '';`,
	// 10: people and pets (docs/architecture/photo-people.md). A scan
	// records that the face models, or the animal model, looked at a content
	// object; each face or pet they found is an occurrence, derived data
	// keyed by the object. People
	// belong to a library: who is who there, and what the user named,
	// confirmed, merged or took out, is user data. An occurrence a library
	// has considered has a row in person_occurrences, with no person while
	// it is nobody yet; its source says whether clustering put it there, a
	// member did, or it moved over from a face found by earlier models and
	// waits to be checked against the new ones. Originals that already have
	// thumbnails are queued.
	`
CREATE TABLE scans (
    object_id TEXT NOT NULL REFERENCES objects(id) ON DELETE CASCADE,
    derivation TEXT NOT NULL,
    model TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (object_id, derivation)
);

CREATE TABLE occurrences (
    id TEXT PRIMARY KEY,
    object_id TEXT NOT NULL REFERENCES objects(id) ON DELETE CASCADE,
    derivation TEXT NOT NULL,
    ordinal INTEGER NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('face', 'cat', 'dog')),
    model TEXT NOT NULL,
    box BLOB NOT NULL,
    landmarks BLOB,
    score REAL NOT NULL,
    vector BLOB NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (object_id, derivation, ordinal)
);

CREATE TABLE people (
    id TEXT PRIMARY KEY,
    library_id TEXT NOT NULL REFERENCES libraries(id),
    kind TEXT NOT NULL CHECK (kind IN ('person', 'cat', 'dog')),
    name TEXT NOT NULL DEFAULT '',
    hidden INTEGER NOT NULL DEFAULT 0,
    cover_occurrence_id TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX people_by_library ON people(library_id, kind);

CREATE TABLE person_occurrences (
    library_id TEXT NOT NULL REFERENCES libraries(id),
    occurrence_id TEXT NOT NULL REFERENCES occurrences(id) ON DELETE CASCADE,
    person_id TEXT REFERENCES people(id) ON DELETE SET NULL,
    source TEXT NOT NULL CHECK (source IN ('auto', 'user', 'moved')),
    created_at TEXT NOT NULL,
    PRIMARY KEY (library_id, occurrence_id)
);
CREATE INDEX person_occurrences_by_person ON person_occurrences(person_id);
CREATE INDEX person_occurrences_by_occurrence ON person_occurrences(occurrence_id);

CREATE TABLE occurrence_rejections (
    library_id TEXT NOT NULL REFERENCES libraries(id),
    occurrence_id TEXT NOT NULL REFERENCES occurrences(id) ON DELETE CASCADE,
    person_id TEXT NOT NULL REFERENCES people(id) ON DELETE CASCADE,
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (library_id, occurrence_id, person_id)
);
CREATE INDEX occurrence_rejections_by_occurrence ON occurrence_rejections(occurrence_id);

INSERT INTO jobs(object_id, derivation, state, not_before, created_at)
SELECT object_id, 'faces/v1', 'pending', created_at, created_at FROM derived_files WHERE derivation = 'thumbnail/v1'
ON CONFLICT(object_id, derivation) DO NOTHING;
`,
	// 11: whether a library recognises people and pets; on by default
	// (docs/architecture/photo-people.md#关闭与清除).
	`ALTER TABLE libraries ADD COLUMN people INTEGER NOT NULL DEFAULT 1;`,
	// 12: pet scans told only dogs apart; cats are pets too now. Without
	// their scans, every photo with a cat or a dog is scanned again when the
	// service first sees the animal model; the dogs found keep what members
	// decided, by the place they were found in.
	`DELETE FROM scans WHERE derivation = 'pets/v1';`,
	// 13: videos (docs/specs/photo-video.md). A content object that is a
	// video original keeps how long it plays and what the Media Worker read
	// from it, so the timeline and the decision how a browser plays it need
	// no decoder. Videos take no part in local AI.
	`
CREATE TABLE videos (
    object_id TEXT PRIMARY KEY REFERENCES objects(id) ON DELETE CASCADE,
    duration REAL NOT NULL,
    info TEXT NOT NULL
);
`,
	// 14: the vector index's outbox (docs/architecture/vector-index.md). Every
	// change to an object's image vector, or to which libraries hold it,
	// queues the object in the same transaction; the sync loop brings the
	// index in line and drops the row unless it changed again meanwhile.
	// The outbox has no foreign key: when an object goes, its row must stay
	// for the index to drop the point. A cascade from objects deletes the
	// vector through the embeddings trigger as well. The triggers upsert
	// rather than INSERT OR REPLACE: an upsert that fires them, as storing a
	// vector does, makes their own conflict handling abort.
	`
CREATE TABLE vector_outbox (
    object_id TEXT PRIMARY KEY,
    version INTEGER NOT NULL DEFAULT 1
);

-- The sync loop finds the newest vector's model before every batch.
CREATE INDEX embeddings_by_age ON embeddings(derivation, created_at, object_id);

-- Which collection holds the index, once the sync loop has made one.
CREATE TABLE vector_index_state (
    name TEXT PRIMARY KEY,
    collection TEXT NOT NULL,
    model TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TRIGGER embeddings_vector_outbox_insert AFTER INSERT ON embeddings BEGIN
    INSERT INTO vector_outbox(object_id) VALUES (NEW.object_id)
    ON CONFLICT(object_id) DO UPDATE SET version = version + 1;
END;
CREATE TRIGGER embeddings_vector_outbox_update AFTER UPDATE ON embeddings BEGIN
    INSERT INTO vector_outbox(object_id) VALUES (NEW.object_id)
    ON CONFLICT(object_id) DO UPDATE SET version = version + 1;
END;
CREATE TRIGGER embeddings_vector_outbox_delete AFTER DELETE ON embeddings BEGIN
    INSERT INTO vector_outbox(object_id) VALUES (OLD.object_id)
    ON CONFLICT(object_id) DO UPDATE SET version = version + 1;
END;
CREATE TRIGGER assets_vector_outbox_insert AFTER INSERT ON assets BEGIN
    INSERT INTO vector_outbox(object_id) VALUES (NEW.object_id)
    ON CONFLICT(object_id) DO UPDATE SET version = version + 1;
END;
CREATE TRIGGER assets_vector_outbox_delete AFTER DELETE ON assets BEGIN
    INSERT INTO vector_outbox(object_id) VALUES (OLD.object_id)
    ON CONFLICT(object_id) DO UPDATE SET version = version + 1;
END;
CREATE TRIGGER assets_vector_outbox_update AFTER UPDATE OF trashed_at, library_id, object_id ON assets
WHEN OLD.trashed_at IS NOT NEW.trashed_at OR OLD.library_id IS NOT NEW.library_id OR OLD.object_id IS NOT NEW.object_id
BEGIN
    INSERT INTO vector_outbox(object_id) VALUES (OLD.object_id)
    ON CONFLICT(object_id) DO UPDATE SET version = version + 1;
    INSERT INTO vector_outbox(object_id) VALUES (NEW.object_id)
    ON CONFLICT(object_id) DO UPDATE SET version = version + 1;
END;
`,
}

func openCatalog(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_busy_timeout=5000&_journal_mode=WAL&_synchronous=FULL")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := migrate(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read photo catalog version: %w", err)
	}
	if version > len(migrations) {
		return fmt.Errorf("photo catalog version %d is newer than this build supports (%d)", version, len(migrations))
	}
	for next := version; next < len(migrations); next++ {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[next]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply photo catalog migration %d: %w", next+1, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", next+1)); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// timeLayout has a fixed width so that stored timestamps sort correctly as
// text; time.RFC3339Nano trims trailing zeros and would not.
const timeLayout = "2006-01-02T15:04:05.000000000Z"

func formatTime(value time.Time) string { return value.UTC().Format(timeLayout) }

func parseTime(value string) (time.Time, error) { return time.Parse(timeLayout, value) }

func (s *Service) randomID(prefix string) string { return prefix + ":" + s.randomHex() }

func (s *Service) randomHex() string {
	value := make([]byte, 16)
	if _, err := io.ReadFull(s.random, value); err != nil {
		panic(err)
	}
	return hex.EncodeToString(value)
}

type queryer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type rowScanner interface {
	Scan(...any) error
}

func (s *Service) audit(ctx context.Context, q queryer, actorID, action, resourceID, detail string) error {
	_, err := q.ExecContext(ctx,
		"INSERT INTO audit_events(id, actor_user_id, action, resource_type, resource_id, occurred_at, detail) VALUES(?,?,?,?,?,?,?)",
		s.randomID("audit"), actorID, action, "photo", resourceID, formatTime(s.now()), detail)
	return err
}

func (s *Service) ensureSharedLibrary() error {
	ctx := context.Background()
	err := s.db.QueryRowContext(ctx, "SELECT id FROM libraries WHERE kind = 'shared'").Scan(&s.sharedLibraryID)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	s.sharedLibraryID = s.randomID("library")
	_, err = s.db.ExecContext(ctx, "INSERT INTO libraries(id, kind, created_at) VALUES(?, 'shared', ?)",
		s.sharedLibraryID, formatTime(s.now()))
	return err
}

// ensurePrivateLibrary returns the caller's private library, creating it on
// first use and keeping the owner's name current.
func (s *Service) ensurePrivateLibrary(ctx context.Context, p Principal) (library, error) {
	lib, err := s.privateLibrary(ctx, s.db, p.UserID)
	if errors.Is(err, ErrNotFound) {
		lib = library{id: s.randomID("library"), kind: LibraryKindPrivate, ownerUserID: p.UserID, ownerName: p.Username, createdAt: s.now().UTC()}
		if _, err := s.db.ExecContext(ctx,
			"INSERT INTO libraries(id, kind, owner_user_id, owner_name, created_at) VALUES(?, 'private', ?, ?, ?) ON CONFLICT DO NOTHING",
			lib.id, p.UserID, p.Username, formatTime(lib.createdAt)); err != nil {
			return library{}, err
		}
		return s.privateLibrary(ctx, s.db, p.UserID)
	}
	if err != nil || p.Username == "" || lib.ownerName == p.Username {
		return lib, err
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE libraries SET owner_name = ? WHERE id = ?", p.Username, lib.id); err != nil {
		return library{}, err
	}
	lib.ownerName = p.Username
	return lib, nil
}

// ownLibrary returns the caller's private library and whether it has one. A
// member's own request creates it on first use; the member's assistant only
// reads, so until the member has opened the photo library it has none.
func (s *Service) ownLibrary(ctx context.Context, p Principal) (library, bool, error) {
	if !p.Assistant {
		lib, err := s.ensurePrivateLibrary(ctx, p)
		return lib, err == nil, err
	}
	lib, err := s.privateLibrary(ctx, s.db, p.UserID)
	if errors.Is(err, ErrNotFound) {
		return library{}, false, nil
	}
	return lib, err == nil, err
}

type library struct {
	id          string
	kind        LibraryKind
	ownerUserID string
	ownerName   string
	createdAt   time.Time
}

const libraryColumns = "id, kind, IFNULL(owner_user_id, ''), owner_name, created_at"

func scanLibrary(row rowScanner) (library, error) {
	var lib library
	var createdAt string
	if err := row.Scan(&lib.id, &lib.kind, &lib.ownerUserID, &lib.ownerName, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return library{}, ErrNotFound
		}
		return library{}, err
	}
	parsed, err := parseTime(createdAt)
	if err != nil {
		return library{}, err
	}
	lib.createdAt = parsed
	return lib, nil
}

func (s *Service) privateLibrary(ctx context.Context, q queryer, userID string) (library, error) {
	return scanLibrary(q.QueryRowContext(ctx,
		"SELECT "+libraryColumns+" FROM libraries WHERE kind = 'private' AND owner_user_id = ?", userID))
}

func (s *Service) libraryByID(ctx context.Context, q queryer, libraryID string) (library, error) {
	return scanLibrary(q.QueryRowContext(ctx, "SELECT "+libraryColumns+" FROM libraries WHERE id = ?", libraryID))
}

// assetSelect reads an asset with its library. The duplicate role is derived
// from the non-trashed assets of the same library that share its original.
const assetSelect = `
SELECT a.id, a.library_id, IFNULL(a.directory_id, ''), a.name, o.media_type, o.size_bytes,
       a.uploaded_by, a.imported_at, IFNULL(a.trashed_at, ''), IFNULL(a.trashed_by, ''),
       IFNULL(a.purge_after, ''), IFNULL(a.taken_at, ''), o.width, o.height, o.orientation,
       CASE
         WHEN EXISTS (SELECT 1 FROM derived_files f WHERE f.object_id = a.object_id AND f.derivation = '` + thumbnailDerivation + `') THEN 'ready'
         WHEN EXISTS (SELECT 1 FROM jobs j WHERE j.object_id = a.object_id AND j.derivation = '` + thumbnailDerivation + `'
                      AND j.state = 'failed') THEN 'failed'
         ELSE 'pending'
       END,
       CASE
         WHEN a.trashed_at IS NOT NULL THEN ''
         WHEN NOT EXISTS (SELECT 1 FROM assets d WHERE d.object_id = a.object_id AND IFNULL(d.motion_object_id, '') = IFNULL(a.motion_object_id, '') AND d.library_id = a.library_id
                          AND d.id <> a.id AND d.trashed_at IS NULL) THEN ''
         WHEN EXISTS (SELECT 1 FROM assets d WHERE d.object_id = a.object_id AND IFNULL(d.motion_object_id, '') = IFNULL(a.motion_object_id, '') AND d.library_id = a.library_id
                      AND d.trashed_at IS NULL
                      AND (d.imported_at < a.imported_at OR (d.imported_at = a.imported_at AND d.id < a.id))) THEN 'duplicate'
         ELSE 'first'
       END,
       a.object_id, IFNULL(a.motion_object_id, ''), a.motion_name, a.motion_kind, a.motion_duration,
       a.motion_object_id IS NULL AND IFNULL(o.live_id, '') <> '', IFNULL(v.duration, -1),
       l.kind, IFNULL(l.owner_user_id, ''), l.created_at
FROM assets a
JOIN objects o ON o.id = a.object_id
JOIN libraries l ON l.id = a.library_id
LEFT JOIN videos v ON v.object_id = a.object_id`

type assetRecord struct {
	Asset
	// orientation is the EXIF orientation of the original (1 when upright).
	orientation    int
	objectID       string
	motionObjectID string
	motionName     string
	library        library
}

func scanAsset(row rowScanner) (assetRecord, error) {
	var record assetRecord
	var importedAt, trashedAt, trashedBy, purgeAfter, takenAt, libraryCreatedAt string
	var duplicate, thumbnail string
	var metadata photoMetadata
	var motion Motion
	var video Video
	if err := row.Scan(&record.ID, &record.LibraryID, &record.DirectoryID, &record.Name, &record.MediaType,
		&record.SizeBytes, &record.UploadedBy, &importedAt, &trashedAt, &trashedBy, &purgeAfter,
		&takenAt, &metadata.width, &metadata.height, &metadata.orientation, &thumbnail, &duplicate,
		&record.objectID, &record.motionObjectID, &record.motionName, &motion.Kind, &motion.Duration,
		&record.LiveVideoMissing, &video.Duration, &record.library.kind, &record.library.ownerUserID, &libraryCreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return assetRecord{}, ErrNotFound
		}
		return assetRecord{}, err
	}
	if record.motionObjectID != "" {
		record.Motion = &motion
	}
	if video.Duration >= 0 {
		record.Video = &video
	}
	record.Duplicate = DuplicateRole(duplicate)
	record.Thumbnail = ThumbnailState(thumbnail)
	record.Width, record.Height = metadata.displaySize()
	record.orientation = metadata.orientation
	record.library.id = record.LibraryID
	var err error
	if record.ImportedAt, err = parseTime(importedAt); err != nil {
		return assetRecord{}, err
	}
	if takenAt != "" {
		parsed, err := parseTime(takenAt)
		if err != nil {
			return assetRecord{}, err
		}
		record.TakenAt = &parsed
	}
	if record.library.createdAt, err = parseTime(libraryCreatedAt); err != nil {
		return assetRecord{}, err
	}
	if trashedAt != "" {
		trash := &TrashState{TrashedBy: trashedBy}
		if trash.TrashedAt, err = parseTime(trashedAt); err != nil {
			return assetRecord{}, err
		}
		if trash.PurgeAfter, err = parseTime(purgeAfter); err != nil {
			return assetRecord{}, err
		}
		record.Trash = trash
	}
	return record, nil
}

func (s *Service) assetByID(ctx context.Context, q queryer, assetID string) (assetRecord, error) {
	return scanAsset(q.QueryRowContext(ctx, assetSelect+" WHERE a.id = ?", assetID))
}

func (s *Service) queryAssets(ctx context.Context, q queryer, where string, args ...any) ([]assetRecord, error) {
	rows, err := q.QueryContext(ctx, assetSelect+" "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []assetRecord
	for rows.Next() {
		record, err := scanAsset(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

type directoryRecord struct {
	Directory
	library library
}

const directorySelect = `
SELECT d.id, d.library_id, IFNULL(d.parent_id, ''), d.name, d.created_by, d.created_at,
       l.kind, IFNULL(l.owner_user_id, ''), l.created_at
FROM directories d
JOIN libraries l ON l.id = d.library_id`

func scanDirectory(row rowScanner) (directoryRecord, error) {
	var record directoryRecord
	var createdAt, libraryCreatedAt string
	if err := row.Scan(&record.ID, &record.LibraryID, &record.ParentID, &record.Name, &record.CreatedBy, &createdAt,
		&record.library.kind, &record.library.ownerUserID, &libraryCreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return directoryRecord{}, ErrNotFound
		}
		return directoryRecord{}, err
	}
	record.library.id = record.LibraryID
	var err error
	if record.CreatedAt, err = parseTime(createdAt); err != nil {
		return directoryRecord{}, err
	}
	if record.library.createdAt, err = parseTime(libraryCreatedAt); err != nil {
		return directoryRecord{}, err
	}
	return record, nil
}

func (s *Service) directoryByID(ctx context.Context, q queryer, directoryID string) (directoryRecord, error) {
	return scanDirectory(q.QueryRowContext(ctx, directorySelect+" WHERE d.id = ?", directoryID))
}

func (s *Service) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
