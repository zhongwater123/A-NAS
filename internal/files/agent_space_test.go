package files_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/files"
)

func TestAgentPolicyNamesCannotBeCreatedThroughFileOperations(t *testing.T) {
	for _, operation := range []string{"mkdir", "upload", "begin upload", "copy", "move", "restore"} {
		t.Run(operation, func(t *testing.T) {
			f := newUploadFixture(t)
			source, err := f.service.Upload(f.ctx, f.alice, "space:shared", "", "ordinary.txt", bytes.NewBufferString("keep"))
			if err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "mkdir":
				_, err = f.service.CreateDirectory(f.ctx, f.alice, "space:shared", "", "Agent空间")
			case "upload":
				_, err = f.service.Upload(f.ctx, f.alice, "space:shared", "", "Agent空间", bytes.NewBufferString("impostor"))
			case "begin upload":
				var pending *files.PendingUpload
				pending, err = f.service.BeginUpload(f.ctx, f.alice, files.UploadRequest{SpaceID: "space:shared", Name: "Agent空间", Size: 1, Conflict: files.ConflictReplace})
				if pending != nil {
					_ = f.service.AbortUpload(f.ctx, f.alice, pending)
				}
			case "copy":
				_, err = f.service.Copy(f.ctx, f.alice, source.ID, "space:shared", "", "Agent空间")
			case "move":
				_, err = f.service.Move(f.ctx, f.alice, source.ID, "space:shared", "", "Agent空间")
			case "restore":
				item, deleteErr := f.service.Delete(f.ctx, f.alice, "space:shared", source.ID)
				if deleteErr != nil {
					t.Fatal(deleteErr)
				}
				_, err = f.service.Restore(f.ctx, f.alice, item.ID, "", "Agent空间", files.ConflictAsk)
			}
			if !errors.Is(err, files.ErrForbidden) {
				t.Fatalf("%s at reserved name = %v, want permission denied", operation, err)
			}
			// The restriction belongs to the managed location, not the word.
			if _, err := f.service.CreateDirectory(f.ctx, f.alice, f.space.ID, "", "Agent空间"); err != nil {
				t.Fatalf("ordinary directory in personal space: %v", err)
			}
		})
	}
}

func TestAgentSpacePolicyDirectoriesCannotBeMovedOrDeleted(t *testing.T) {
	for _, relative := range []string{"Agent空间", "Agent空间/alice"} {
		for _, operation := range []string{"rename", "move", "delete"} {
			t.Run(relative+"/"+operation, func(t *testing.T) {
				var volume string
				f := newUploadFixtureOn(t, func(root string) files.FileSystem {
					volume = root
					return files.NewRootFileSystem(root, true)
				})
				if _, err := f.service.List(f.ctx, f.alice, "space:shared", ""); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(volume, "spaces/shared/Agent空间/alice"), 0o700); err != nil {
					t.Fatal(err)
				}
				entries, err := f.service.List(f.ctx, f.alice, "space:shared", "")
				if err != nil || len(entries) != 1 {
					t.Fatalf("shared entries = %+v, %v", entries, err)
				}
				entry := entries[0]
				if relative != "Agent空间" {
					entries, err = f.service.List(f.ctx, f.alice, "space:shared", entry.ID)
					if err != nil || len(entries) != 1 {
						t.Fatalf("agent entries = %+v, %v", entries, err)
					}
					entry = entries[0]
				}
				switch operation {
				case "rename":
					_, err = f.service.Move(f.ctx, f.alice, entry.ID, "", entry.ParentID, "renamed")
				case "move":
					_, err = f.service.Move(f.ctx, f.alice, entry.ID, f.space.ID, "", "")
				case "delete":
					_, err = f.service.Delete(f.ctx, f.alice, "space:shared", entry.ID)
				}
				if !errors.Is(err, files.ErrForbidden) {
					t.Errorf("%s %s = %v, want permission denied", operation, relative, err)
				}
			})
		}
	}
}
