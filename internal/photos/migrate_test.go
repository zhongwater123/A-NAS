package photos

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// A Catalog written by the first schema keeps its rows and gains thumbnail
// jobs for its existing originals.
func TestMigrationFromFirstSchemaQueuesThumbnails(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "photos")
	db, err := sql.Open("sqlite3", filepath.Join(mustMkdir(t, root), catalogFile)+"?_foreign_keys=on")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	for _, statement := range []string{
		migrations[0],
		"PRAGMA user_version = 1",
		"INSERT INTO libraries(id, kind, owner_user_id, created_at) VALUES('library:a', 'private', 'user:alice', '2026-10-01T00:00:00.000000000Z')",
		"INSERT INTO objects(id, size_bytes, media_type, created_at) VALUES('" + zeroObjectID + "', 1, 'image/png', '2026-10-01T00:00:00.000000000Z')",
		"INSERT INTO assets(id, library_id, object_id, name, uploaded_by, imported_at) VALUES('photo:a', 'library:a', '" + zeroObjectID + "', 'a.png', 'user:alice', '2026-10-01T00:00:00.000000000Z')",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("prepare first schema: %v", err)
		}
	}
	_ = db.Close()

	service, err := Open(root, Options{DisableCapacityReserve: true})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer service.Close()
	var version, jobs int
	if err := service.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Fatalf("user_version = %d, %v; want %d", version, err, len(migrations))
	}
	if err := service.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM jobs WHERE object_id = ?", zeroObjectID).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("thumbnail jobs = %d, %v; want 1", jobs, err)
	}
	asset, err := service.Get(ctx, Principal{UserID: "user:alice"}, "photo:a")
	if err != nil || asset.Thumbnail != ThumbnailPending || asset.Width != 0 {
		t.Fatalf("Get() = %+v, %v", asset, err)
	}
}

// Originals that already had thumbnails before vectors existed are queued
// for one.
func TestMigrationQueuesVectorsForExistingThumbnails(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "photos")
	db, err := sql.Open("sqlite3", filepath.Join(mustMkdir(t, root), catalogFile)+"?_foreign_keys=on")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	statements := append([]string{}, migrations[:3]...)
	statements = append(statements,
		"PRAGMA user_version = 3",
		"INSERT INTO objects(id, size_bytes, media_type, created_at) VALUES('"+zeroObjectID+"', 1, 'image/png', '2026-10-01T00:00:00.000000000Z')",
		"INSERT INTO derived_files(object_id, derivation, media_type, width, height, size_bytes, created_at) VALUES('"+zeroObjectID+"', 'thumbnail/v1', 'image/jpeg', 4, 3, 10, '2026-10-01T00:00:00.000000000Z')",
		"DELETE FROM jobs",
	)
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("prepare schema 3: %v", err)
		}
	}
	_ = db.Close()

	service, err := Open(root, Options{DisableCapacityReserve: true})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer service.Close()
	var derivation string
	if err := service.db.QueryRowContext(ctx, "SELECT derivation FROM jobs WHERE object_id = ?", zeroObjectID).Scan(&derivation); err != nil || derivation != embeddingDerivation {
		t.Fatalf("queued job = %q, %v; want %s", derivation, err, embeddingDerivation)
	}
}

func TestMigrationQueuesDetectionsAndFaceScansForExistingThumbnails(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "photos")
	db, err := sql.Open("sqlite3", filepath.Join(mustMkdir(t, root), catalogFile)+"?_foreign_keys=on")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	statements := append([]string{}, migrations[:6]...)
	statements = append(statements,
		"PRAGMA user_version = 6",
		"INSERT INTO objects(id, size_bytes, media_type, created_at) VALUES('"+zeroObjectID+"', 1, 'image/png', '2026-10-01T00:00:00.000000000Z')",
		"INSERT INTO derived_files(object_id, derivation, media_type, width, height, size_bytes, created_at) VALUES('"+zeroObjectID+"', 'thumbnail/v1', 'image/jpeg', 4, 3, 10, '2026-10-01T00:00:00.000000000Z')",
		"DELETE FROM jobs",
	)
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("prepare schema 6: %v", err)
		}
	}
	_ = db.Close()

	service, err := Open(root, Options{DisableCapacityReserve: true})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer service.Close()
	var queued string
	err = service.db.QueryRowContext(ctx,
		"SELECT group_concat(derivation, ' ') FROM (SELECT derivation FROM jobs WHERE object_id = ? ORDER BY derivation)",
		zeroObjectID).Scan(&queued)
	if want := facesDerivation + " " + detectionDerivation; err != nil || queued != want {
		t.Fatalf("queued jobs = %q, %v; want %s", queued, err, want)
	}
}

func TestStoredBoxesRoundTrip(t *testing.T) {
	boxes := []Box{{Class: 62, Score: 0.875, X0: 0.25, Y0: 0.5, X1: 0.75, Y1: 1}, {Class: 365, Score: 0.05, X1: 0.001, Y1: 0.002}}
	decoded, err := decodeBoxes(encodeBoxes(boxes))
	if err != nil || len(decoded) != 2 {
		t.Fatalf("decodeBoxes() = %v, %v", decoded, err)
	}
	for i, box := range decoded {
		want := boxes[i]
		if box.Class != want.Class || box.Score != want.Score {
			t.Fatalf("box %d = %+v, want %+v", i, box, want)
		}
		for _, pair := range [][2]float32{{box.X0, want.X0}, {box.Y0, want.Y0}, {box.X1, want.X1}, {box.Y1, want.Y1}} {
			if diff := pair[0] - pair[1]; diff > 1e-4 || diff < -1e-4 {
				t.Fatalf("box %d = %+v, want %+v", i, box, want)
			}
		}
	}
	if empty, err := decodeBoxes(encodeBoxes(nil)); err != nil || len(empty) != 0 {
		t.Fatalf("an empty detection = %v, %v", empty, err)
	}
	if _, err := decodeBoxes([]byte{2, 0}); err == nil {
		t.Fatal("decodeBoxes() accepted another version")
	}
}

const zeroObjectID = "0000000000000000000000000000000000000000000000000000000000000000"

func mustMkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return path
}
