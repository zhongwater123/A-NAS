package photos_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/photos"
)

func writeFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func objectFile(root string, content []byte) string {
	sum := sha256.Sum256(content)
	id := hex.EncodeToString(sum[:])
	return filepath.Join(root, "objects", id[0:2], id[2:4], id)
}

func TestReconcileRemovesCrashLeftoversButNeverReferencedOriginals(t *testing.T) {
	ctx := context.Background()
	service, _, root := newService(t)
	private, _ := libraries(t, service, alice)
	kept := importPhoto(t, service, alice, private.ID, "", "kept.png", encodePNG(20))
	lost := importPhoto(t, service, alice, private.ID, "", "lost.png", encodePNG(21))

	// An import interrupted while streaming.
	writeFile(t, filepath.Join(root, "staging", "interrupted.part"), []byte("partial"))
	// An import that published its original but crashed before committing,
	// or a purge that committed but crashed before unlinking.
	orphan := encodePNG(22)
	writeFile(t, objectFile(root, orphan), orphan)
	// A file Reconcile does not own.
	writeFile(t, filepath.Join(root, "objects", "zz", "README"), []byte("not an object"))
	// A referenced original that went missing.
	if err := os.Remove(objectFile(root, encodePNG(21))); err != nil {
		t.Fatalf("remove original: %v", err)
	}

	report, err := service.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	want := photos.ReconcileReport{StagingRemoved: 1, OrphanObjectsRemoved: 1, MissingObjects: 1, UnexpectedEntries: 1}
	if report != want {
		t.Fatalf("Reconcile() = %+v, want %+v", report, want)
	}
	if _, err := os.Stat(objectFile(root, orphan)); !os.IsNotExist(err) {
		t.Fatalf("orphan original kept: %v", err)
	}
	content, err := service.Open(ctx, alice, kept.ID)
	if err != nil {
		t.Fatalf("Open(kept) error = %v", err)
	}
	stored, _ := io.ReadAll(content.Reader)
	_ = content.Reader.Close()
	if !bytes.Equal(stored, encodePNG(20)) {
		t.Fatalf("referenced original changed")
	}
	if _, err := service.Open(ctx, alice, lost.ID); err == nil {
		t.Fatalf("Open(lost) succeeded without its original")
	}

	// Importing the same bytes again heals the missing original.
	importPhoto(t, service, alice, private.ID, "", "lost again.png", encodePNG(21))
	report, err = service.Reconcile(ctx)
	if err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}
	if want := (photos.ReconcileReport{UnexpectedEntries: 1}); report != want {
		t.Fatalf("second Reconcile() = %+v, want %+v", report, want)
	}
}

func TestReconcileLeavesImportsInProgressAlone(t *testing.T) {
	ctx := context.Background()
	service, _, root := newService(t)
	private, _ := libraries(t, service, alice)
	original := encodePNG(23)
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		_, err := service.Import(ctx, alice, photos.ImportRequest{LibraryID: private.ID, Name: "slow.png", Content: reader})
		done <- err
	}()

	// Once the first write returns, the import has created its staging file
	// and is waiting for more bytes.
	if _, err := writer.Write(original[:10]); err != nil {
		t.Fatalf("write first chunk: %v", err)
	}
	report, err := service.Reconcile(ctx)
	if err != nil || report.StagingRemoved != 0 {
		t.Fatalf("Reconcile() during import = %+v, %v", report, err)
	}
	if _, err := writer.Write(original[10:]); err != nil {
		t.Fatalf("write rest: %v", err)
	}
	_ = writer.Close()
	if err := <-done; err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if _, err := os.Stat(objectFile(root, original)); err != nil {
		t.Fatalf("original not stored: %v", err)
	}
}
