package files_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/files"
)

func TestCrossSpaceMoveKeepsChangesMadeAfterTheCopy(t *testing.T) {
	copies := &pausedCopies{started: make(chan string), resume: make(chan struct{})}
	var volume string
	f := newUploadFixtureOn(t, func(root string) files.FileSystem {
		volume = root
		copies.FileSystem = files.NewRootFileSystem(root, true)
		return copies
	})
	trip, err := f.service.CreateDirectory(f.ctx, f.alice, "space:shared", "", "Trip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Upload(f.ctx, f.alice, "space:shared", trip.ID, "notes.txt", bytes.NewBufferString("before copy")); err != nil {
		t.Fatal(err)
	}
	var moved files.Entry
	var moveErr error
	done := make(chan struct{})
	resume := sync.OnceFunc(func() { close(copies.resume) })
	go func() {
		defer close(done)
		moved, moveErr = f.service.Move(f.ctx, f.alice, trip.ID, f.space.ID, "", "")
	}()
	t.Cleanup(func() { resume(); <-done })
	select {
	case <-copies.started:
	case <-time.After(5 * time.Second):
		t.Fatal("copy did not reach the publish boundary")
	}
	// Direct writes model SMB, which does not acquire the Product Service's
	// locks. Both a new entry and a newer version arrived after copying.
	source := filepath.Join(volume, "spaces", "shared", "Trip")
	for name, content := range map[string]string{"late.txt": "new file", "notes.txt": "written during move"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	resume()
	<-done
	if moveErr != nil {
		t.Fatal(moveErr)
	}
	if moved.SpaceID != f.space.ID {
		t.Fatalf("moved = %+v", moved)
	}
	if data, err := os.ReadFile(filepath.Join(f.root, "Trip", "notes.txt")); err != nil || string(data) != "before copy" {
		t.Fatalf("copied content = %q, %v", data, err)
	}
	trash, err := f.service.ListTrash(f.ctx, f.alice)
	if err != nil {
		t.Fatal(err)
	}
	if len(trash) != 1 || trash[0].EntryID != trip.ID {
		t.Fatalf("source must remain recoverable: %+v", trash)
	}
	if _, err := f.service.Restore(f.ctx, f.alice, trash[0].ID, "", "", files.ConflictAsk); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"late.txt": "new file", "notes.txt": "written during move"} {
		data, err := os.ReadFile(filepath.Join(source, name))
		if err != nil || string(data) != want {
			t.Errorf("restored %s = %q, %v; want %q", name, data, err, want)
		}
	}
}

type refusingMoveToTrash struct{ files.FileSystem }

func (f refusingMoveToTrash) Rename(ctx context.Context, from, to string) error {
	if strings.Contains(to, string(filepath.Separator)+".a-nas-trash"+string(filepath.Separator)) {
		return os.ErrPermission
	}
	return f.FileSystem.Rename(ctx, from, to)
}

func TestCrossSpaceMoveKeepsBothCopiesWhenRecyclingFails(t *testing.T) {
	var volume string
	f := newUploadFixtureOn(t, func(root string) files.FileSystem {
		volume = root
		return refusingMoveToTrash{files.NewRootFileSystem(root, true)}
	})
	file := f.send(t, files.UploadRequest{Name: "notes.txt"}, "keep this")
	_, err := f.service.Move(f.ctx, f.alice, file.Entry.ID, "space:shared", "", "")
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("move error = %v, want recycling failure", err)
	}
	for _, name := range []string{filepath.Join(f.root, "notes.txt"), filepath.Join(volume, "spaces", "shared", "notes.txt")} {
		data, err := os.ReadFile(name)
		if err != nil || string(data) != "keep this" {
			t.Errorf("%s = %q, %v", name, data, err)
		}
	}
}

func TestCrossSpaceMovePreservesModificationTime(t *testing.T) {
	f := newUploadFixture(t)
	file := f.send(t, files.UploadRequest{Name: "dated.txt"}, "old content")
	want := time.Date(2020, 1, 2, 3, 4, 5, 123456789, time.UTC)
	if err := os.Chtimes(filepath.Join(f.root, "dated.txt"), want, want); err != nil {
		t.Fatal(err)
	}
	moved, err := f.service.Move(f.ctx, f.alice, file.Entry.ID, "space:shared", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !moved.ModifiedAt.Equal(want) {
		t.Fatalf("moved modification time = %v, want %v", moved.ModifiedAt, want)
	}
	content, err := f.service.OpenContent(f.ctx, f.alice, moved.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = content.Reader.Close() }()
	if !content.ModifiedAt.Equal(want) {
		t.Errorf("content modification time = %v, want %v", content.ModifiedAt, want)
	}
}
