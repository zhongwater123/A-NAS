package files_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/files"
)

// New names must fit the file system and work over SMB on Windows and
// macOS; the errors say which rule a name broke.
func TestNewNamesFitTheFileSystemAndSMBClients(t *testing.T) {
	f := newUploadFixture(t)
	refused := map[string]error{
		strings.Repeat("长", 86): files.ErrNameTooLong, // 258 bytes
		"a\tb":                  files.ErrNameNotPortable,
		"line\nbreak":           files.ErrNameNotPortable,
		"a:b":                   files.ErrNameNotPortable,
		"a*b":                   files.ErrNameNotPortable,
		"what?":                 files.ErrNameNotPortable,
		`say "hi"`:              files.ErrNameNotPortable,
		"<x>":                   files.ErrNameNotPortable,
		"a|b":                   files.ErrNameNotPortable,
		"CON":                   files.ErrNameNotPortable,
		"con.txt":               files.ErrNameNotPortable,
		"Lpt1":                  files.ErrNameNotPortable,
		"ends with a dot.":      files.ErrNameNotPortable,
		"ends with a space ":    files.ErrNameNotPortable,
		".a-nas-trash":          files.ErrInvalidName,
		"..":                    files.ErrInvalidName,
		"a/b":                   files.ErrInvalidName,
	}
	for name, want := range refused {
		_, err := f.service.CreateDirectory(f.ctx, f.alice, f.space.ID, "", name)
		if !errors.Is(err, want) || !errors.Is(err, files.ErrInvalidName) {
			t.Errorf("CreateDirectory(%q) error = %v, want %v", name, err, want)
		}
		if _, err := f.service.Upload(f.ctx, f.alice, f.space.ID, "", name, bytes.NewBufferString("x")); !errors.Is(err, want) {
			t.Errorf("Upload(%q) error = %v, want %v", name, err, want)
		}
		if _, err := f.service.BeginUpload(f.ctx, f.alice, files.UploadRequest{SpaceID: f.space.ID, Name: name, Size: 1, Conflict: files.ConflictAsk}); !errors.Is(err, want) {
			t.Errorf("BeginUpload(%q) error = %v, want %v", name, err, want)
		}
	}
	for _, name := range []string{strings.Repeat("长", 85), "CONSOLE", "con-notes", "a.b.c", " leading space", "résumé", "照片 😀", "COM0"} {
		if _, err := f.service.CreateDirectory(f.ctx, f.alice, f.space.ID, "", name); err != nil {
			t.Errorf("CreateDirectory(%q) error = %v", name, err)
		}
	}
}

// Names that differ only in letter case are one name to SMB clients.
func TestNamesThatDifferOnlyInCaseAreTaken(t *testing.T) {
	f := newUploadFixture(t)
	folder, err := f.service.CreateDirectory(f.ctx, f.alice, f.space.ID, "", "Dup")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.CreateDirectory(f.ctx, f.alice, f.space.ID, "", "dup"); !errors.Is(err, files.ErrConflict) {
		t.Fatalf("CreateDirectory(dup) beside Dup error = %v, want ErrConflict", err)
	}
	if _, err := f.service.Upload(f.ctx, f.alice, f.space.ID, "", "DUP", bytes.NewBufferString("x")); !errors.Is(err, files.ErrConflict) {
		t.Fatalf("Upload(DUP) beside Dup error = %v, want ErrConflict", err)
	}
	if _, err := f.service.BeginUpload(f.ctx, f.alice, files.UploadRequest{SpaceID: f.space.ID, Name: "dUP", Size: 1, Conflict: files.ConflictAsk}); !errors.Is(err, files.ErrConflict) {
		t.Fatalf("BeginUpload(dUP) beside Dup error = %v, want ErrConflict", err)
	}
	// An item may change its own case.
	renamed, err := f.service.Move(f.ctx, f.alice, folder.ID, "", "", "DUP")
	if err != nil || renamed.Name != "DUP" {
		t.Fatalf("renaming Dup to DUP = %+v, %v", renamed, err)
	}
	if _, err := f.service.Copy(f.ctx, f.alice, folder.ID, "", "", "dup"); !errors.Is(err, files.ErrConflict) {
		t.Fatalf("copying DUP beside itself as dup error = %v, want ErrConflict", err)
	}
	// A folder upload goes into the folder of the other case.
	committed := f.send(t, files.UploadRequest{Directories: []string{"dup"}, Name: "inside.txt"}, "x")
	if committed.Entry.ParentID != folder.ID {
		t.Fatalf("folder upload went to %q, want the existing DUP folder", committed.Entry.ParentID)
	}
	// Keeping both skips numbered names taken in another case.
	f.send(t, files.UploadRequest{Name: "Report.txt"}, "1")
	f.send(t, files.UploadRequest{Name: "report (1).TXT"}, "2")
	kept := f.send(t, files.UploadRequest{Name: "report.txt", Conflict: files.ConflictRename}, "3")
	if kept.Entry.Name != "report (2).txt" {
		t.Fatalf("kept both as %q, want report (2).txt", kept.Entry.Name)
	}
}

// An item that already has a name the rules refuse, for example one made
// over SMB on Linux, keeps it when it moves or comes back from the trash.
func TestExistingNamesAreKept(t *testing.T) {
	f := newUploadFixture(t)
	target, err := f.service.CreateDirectory(f.ctx, f.alice, f.space.ID, "", "archive")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "notes:old.txt"), []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := f.service.List(f.ctx, f.alice, f.space.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	var legacy files.Entry
	for _, entry := range entries {
		if entry.Name == "notes:old.txt" {
			legacy = entry
		}
	}
	if legacy.ID == "" {
		t.Fatalf("legacy file not listed in %+v", entries)
	}
	if _, err := f.service.Move(f.ctx, f.alice, legacy.ID, "", "", "notes:new.txt"); !errors.Is(err, files.ErrNameNotPortable) {
		t.Fatalf("renaming to another refused name error = %v, want ErrNameNotPortable", err)
	}
	moved, err := f.service.Move(f.ctx, f.alice, legacy.ID, "", target.ID, "")
	if err != nil || moved.Name != "notes:old.txt" {
		t.Fatalf("moving under its own name = %+v, %v", moved, err)
	}
	item, err := f.service.Delete(f.ctx, f.alice, f.space.ID, legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored, err := f.service.Restore(f.ctx, f.alice, item.ID, "", ""); err != nil || restored.Name != "notes:old.txt" {
		t.Fatalf("restoring under its own name = %+v, %v", restored, err)
	}
}

// Keeping both trims a long name so the number still fits.
func TestNumberedCopiesOfLongNamesFit(t *testing.T) {
	f := newUploadFixture(t)
	name := strings.Repeat("长", 83) + ".txt" // 253 bytes
	f.send(t, files.UploadRequest{Name: name}, "1")
	kept := f.send(t, files.UploadRequest{Name: name, Conflict: files.ConflictRename}, "2")
	if len(kept.Entry.Name) > 255 || !strings.HasSuffix(kept.Entry.Name, " (1).txt") {
		t.Fatalf("kept both as %q (%d bytes)", kept.Entry.Name, len(kept.Entry.Name))
	}
}
