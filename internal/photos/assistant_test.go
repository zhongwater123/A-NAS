package photos_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/photos"
)

// A member's AI assistant (ADR 0022) reads what the member owns and what the
// shared library holds. It never writes, never sees what the member could see
// only as an administrator through a viewing grant, and creates nothing.

func TestAssistantSeesOnlyTheMembersOwnAndSharedLibraries(t *testing.T) {
	ctx := context.Background()
	service, c, _ := newService(t)
	alicePrivate, shared := libraries(t, service, alice)
	adminPrivate, _ := libraries(t, service, admin)
	importPhoto(t, service, alice, alicePrivate.ID, "", "beach-alice.png", pngBytes(t, 1))
	importPhoto(t, service, admin, adminPrivate.ID, "", "beach-admin.png", pngBytes(t, 2))
	importPhoto(t, service, bob, shared.ID, "", "beach-shared.png", pngBytes(t, 3))
	// The administrator is viewing alice's private library right now.
	assistant := photos.Principal{UserID: admin.UserID, Admin: true, Assistant: true, Viewing: []photos.ViewingGrant{
		{GrantID: "grant:1", OwnerUserID: alice.UserID, ExpiresAt: c.Now().Add(time.Hour)},
	}}

	all, err := service.Libraries(ctx, assistant)
	if err != nil {
		t.Fatalf("Libraries() error = %v", err)
	}
	var ids []string
	for _, lib := range all {
		if lib.Viewing != nil {
			t.Fatalf("assistant lists %+v through a viewing grant", lib)
		}
		ids = append(ids, lib.ID)
	}
	if want := []string{adminPrivate.ID, shared.ID}; !slices.Equal(ids, want) {
		t.Fatalf("Libraries() = %v, want %v", ids, want)
	}

	if got := names(search(t, service, assistant, "beach").Assets); !slices.Equal(got, []string{"beach-admin.png", "beach-shared.png"}) &&
		!slices.Equal(got, []string{"beach-shared.png", "beach-admin.png"}) {
		t.Fatalf("Search() = %v, want the administrator's own and the shared photo", got)
	}
	if _, err := service.Search(ctx, assistant, photos.SearchRequest{Query: "beach", Viewing: alicePrivate.ID}); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("Search() in the viewed library error = %v, want ErrNotFound", err)
	}
	if _, err := service.Timeline(ctx, assistant, alicePrivate.ID, "", 10); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("Timeline() of the viewed library error = %v, want ErrNotFound", err)
	}
	if _, err := service.TagCounts(ctx, assistant, alicePrivate.ID); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("TagCounts() of the viewed library error = %v, want ErrNotFound", err)
	}
}

func TestAssistantCreatesNothingForAMemberWhoNeverOpenedPhotos(t *testing.T) {
	ctx := context.Background()
	service, _, _ := newService(t)
	_, shared := libraries(t, service, alice)
	importPhoto(t, service, alice, shared.ID, "", "family.png", pngBytes(t, 4))
	bobsAssistant := photos.Principal{UserID: bob.UserID, Username: "bob", Assistant: true}

	for range 2 {
		all, err := service.Libraries(ctx, bobsAssistant)
		if err != nil {
			t.Fatalf("Libraries() error = %v", err)
		}
		if len(all) != 1 || all[0].ID != shared.ID {
			t.Fatalf("Libraries() = %+v, want only the shared library", all)
		}
		if got := names(search(t, service, bobsAssistant, "family").Assets); !slices.Equal(got, []string{"family.png"}) {
			t.Fatalf("Search() = %v, want the shared photo", got)
		}
		if _, err := service.TagCounts(ctx, bobsAssistant, ""); err != nil {
			t.Fatalf("TagCounts() error = %v", err)
		}
	}
}

func TestAssistantChangesNothing(t *testing.T) {
	ctx := context.Background()
	service := labelService(t, filepath.Join(t.TempDir(), "photos"), &colorEmbedder{}, colourLabels(t, "colour-1"))
	member := photos.Principal{UserID: alice.UserID, Username: "alice"}
	alicePrivate, shared := libraries(t, service, member)
	directory, err := service.CreateDirectory(ctx, member, alicePrivate.ID, "", "Trip")
	if err != nil {
		t.Fatalf("CreateDirectory() error = %v", err)
	}
	own := importPhoto(t, service, member, alicePrivate.ID, "", "own.png", pngBytes(t, 5))
	tagged := importPhoto(t, service, member, alicePrivate.ID, directory.ID, "tagged.png", pngBytes(t, 7))
	if _, err := service.AddTag(ctx, member, tagged.ID, "青岛"); err != nil {
		t.Fatalf("AddTag() error = %v", err)
	}
	album, err := service.CreateAlbum(ctx, member, alicePrivate.ID, "Trip")
	if err != nil {
		t.Fatalf("CreateAlbum() error = %v", err)
	}
	if _, err := service.AddToAlbum(ctx, member, album.ID, tagged.ID); err != nil {
		t.Fatalf("AddToAlbum() error = %v", err)
	}
	trashed := importPhoto(t, service, member, alicePrivate.ID, "", "old.png", pngBytes(t, 6))
	if _, err := service.Trash(ctx, member, trashed.ID); err != nil {
		t.Fatalf("Trash() error = %v", err)
	}
	// The assistant acts for alice; the name it carries must not rename her library.
	assistant := photos.Principal{UserID: alice.UserID, Username: "someone-else", Assistant: true}

	writes := map[string]func() error{
		"tag":           func() error { _, err := service.AddTag(ctx, assistant, own.ID, "海边"); return err },
		"untag":         func() error { _, err := service.RemoveTag(ctx, assistant, tagged.ID, "青岛"); return err },
		"hide-ai-label": func() error { _, err := service.HideAILabel(ctx, assistant, own.ID, "red"); return err },
		"rename": func() error {
			_, err := service.Update(ctx, assistant, own.ID, photos.AssetUpdate{Name: new("renamed.png")})
			return err
		},
		"move": func() error {
			_, err := service.Update(ctx, assistant, own.ID, photos.AssetUpdate{DirectoryID: &directory.ID})
			return err
		},
		"trash": func() error { _, err := service.Trash(ctx, assistant, own.ID); return err },
		"album": func() error {
			_, err := service.CreateAlbum(ctx, assistant, alicePrivate.ID, "Beach")
			return err
		},
		"rename-album":      func() error { _, err := service.RenameAlbum(ctx, assistant, album.ID, "Beach"); return err },
		"delete-album":      func() error { return service.DeleteAlbum(ctx, assistant, album.ID) },
		"add-to-album":      func() error { _, err := service.AddToAlbum(ctx, assistant, album.ID, own.ID); return err },
		"remove-from-album": func() error { return service.RemoveFromAlbum(ctx, assistant, album.ID, tagged.ID) },
		"directory": func() error {
			_, err := service.CreateDirectory(ctx, assistant, shared.ID, "", "Trip")
			return err
		},
		"rename-directory": func() error {
			_, err := service.UpdateDirectory(ctx, assistant, directory.ID, photos.DirectoryUpdate{Name: new("Beach")})
			return err
		},
		"delete-directory": func() error { return service.DeleteDirectory(ctx, assistant, directory.ID) },
		"empty-trash":      func() error { _, err := service.EmptyTrash(ctx, assistant, alicePrivate.ID); return err },
		"open":             func() error { _, err := service.Open(ctx, assistant, own.ID); return err },
		"thumbnail":        func() error { _, err := service.Thumbnail(ctx, assistant, own.ID); return err },
	}
	for name, write := range writes {
		if err := write(); !errors.Is(err, photos.ErrForbidden) {
			t.Errorf("%s error = %v, want ErrForbidden", name, err)
		}
	}
	// Restoring needs the right to change, so the trash stays out of sight.
	if _, err := service.Get(ctx, assistant, trashed.ID); !errors.Is(err, photos.ErrNotFound) {
		t.Errorf("Get() of a trashed photo error = %v, want ErrNotFound", err)
	}
	for _, check := range []struct {
		asset      photos.Asset
		name       string
		directory  string
		tags, albs int
	}{{own, "own.png", "", 0, 0}, {tagged, "tagged.png", directory.ID, 1, 1}} {
		asset, err := service.Get(ctx, member, check.asset.ID)
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if asset.Name != check.name || asset.DirectoryID != check.directory || asset.Trash != nil ||
			len(asset.Tags) != check.tags || len(asset.Albums) != check.albs {
			t.Fatalf("photo after the assistant's attempts = %+v", asset)
		}
	}
	all, err := service.Libraries(ctx, assistant)
	if err != nil {
		t.Fatalf("Libraries() error = %v", err)
	}
	if all[0].ID != alicePrivate.ID || all[0].OwnerName != "alice" {
		t.Fatalf("own library = %+v, want alice's, still named alice", all[0])
	}
}

func TestAssistantSeesNoOtherMembersNames(t *testing.T) {
	ctx := context.Background()
	service, _, _ := newService(t)
	aliceNamed := photos.Principal{UserID: alice.UserID, Username: "alice"}
	bobNamed := photos.Principal{UserID: bob.UserID, Username: "bob"}
	alicePrivate, _ := libraries(t, service, aliceNamed)
	bobPrivate, _ := libraries(t, service, bobNamed)
	same := pngBytes(t, 8)
	photo := importPhoto(t, service, aliceNamed, alicePrivate.ID, "", "same.png", same)
	importPhoto(t, service, bobNamed, bobPrivate.ID, "", "same.png", same)

	asset, err := service.Get(ctx, aliceNamed, photo.ID)
	if err != nil || !slices.Equal(asset.AlsoKeptBy, []string{"bob"}) {
		t.Fatalf("member's view = %+v, %v; want bob in AlsoKeptBy", asset.AlsoKeptBy, err)
	}
	asset, err = service.Get(ctx, photos.Principal{UserID: alice.UserID, Username: "alice", Assistant: true}, photo.ID)
	if err != nil || len(asset.AlsoKeptBy) != 0 {
		t.Fatalf("assistant's view = %+v, %v; want no other member's name", asset.AlsoKeptBy, err)
	}
}
