package photos

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
)

func TestImportIntoAFullVolumeLeavesNothingBehind(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "photos")
	service, err := Open(root, Options{DisableCapacityReserve: true})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	alice := Principal{UserID: "user:alice", Username: "alice"}
	libraries, err := service.Libraries(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	library := libraries[0].ID
	for _, lib := range libraries {
		if lib.Kind == LibraryKindPrivate {
			library = lib.ID
		}
	}

	// The volume reaches its reserve while the upload is streaming: the
	// first megabyte still fits, the second does not.
	var chunks int
	service.admits = func(string, int64) (bool, error) { chunks++; return chunks < 2, nil }
	original := append(syntheticPNG(t, 1), make([]byte, 3<<20)...)
	_, err = service.Import(ctx, alice, ImportRequest{LibraryID: library, Name: "big.png", Content: bytes.NewReader(original)})
	if !errors.Is(err, ErrInsufficientSpace) {
		t.Fatalf("Import() on a full volume error = %v, want ErrInsufficientSpace", err)
	}
	for _, dir := range []string{stagingDir, objectsDir} {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, err error) error {
			if err == nil && !entry.IsDir() {
				t.Errorf("full-volume import left %s behind", path)
			}
			return nil
		})
	}
	if page, err := service.Timeline(ctx, alice, library, "", 10); err != nil || len(page.Assets) != 0 {
		t.Fatalf("Timeline() after a refused import = %d assets, %v", len(page.Assets), err)
	}

	// Space is freed: the same upload now succeeds.
	service.admits = func(string, int64) (bool, error) { return true, nil }
	if _, err := service.Import(ctx, alice, ImportRequest{LibraryID: library, Name: "big.png", Content: bytes.NewReader(original)}); err != nil {
		t.Fatalf("Import() once space is free error = %v", err)
	}
}
