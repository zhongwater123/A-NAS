package photos_test

import (
	"context"
	"errors"
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
	service, _, _ := newService(t)
	member := photos.Principal{UserID: alice.UserID, Username: "alice"}
	alicePrivate, shared := libraries(t, service, member)
	own := importPhoto(t, service, member, alicePrivate.ID, "", "own.png", pngBytes(t, 5))
	album, err := service.CreateAlbum(ctx, member, alicePrivate.ID, "Trip")
	if err != nil {
		t.Fatalf("CreateAlbum() error = %v", err)
	}
	trashed := importPhoto(t, service, member, alicePrivate.ID, "", "old.png", pngBytes(t, 6))
	if _, err := service.Trash(ctx, member, trashed.ID); err != nil {
		t.Fatalf("Trash() error = %v", err)
	}
	// The assistant acts for alice; the name it carries must not rename her library.
	assistant := photos.Principal{UserID: alice.UserID, Username: "someone-else", Assistant: true}

	writes := map[string]func() error{
		"tag": func() error { _, err := service.AddTag(ctx, assistant, own.ID, "海边"); return err },
		"album": func() error {
			_, err := service.CreateAlbum(ctx, assistant, alicePrivate.ID, "Beach")
			return err
		},
		"add-to-album": func() error { _, err := service.AddToAlbum(ctx, assistant, album.ID, own.ID); return err },
		"directory": func() error {
			_, err := service.CreateDirectory(ctx, assistant, shared.ID, "", "Trip")
			return err
		},
		"empty-trash": func() error { _, err := service.EmptyTrash(ctx, assistant, alicePrivate.ID); return err },
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
	asset, err := service.Get(ctx, member, own.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if len(asset.Tags) != 0 || len(asset.Albums) != 0 {
		t.Fatalf("photo after the assistant's attempts = %+v, want no tags or albums", asset)
	}
	all, err := service.Libraries(ctx, assistant)
	if err != nil {
		t.Fatalf("Libraries() error = %v", err)
	}
	if all[0].ID != alicePrivate.ID || all[0].OwnerName != "alice" {
		t.Fatalf("own library = %+v, want alice's, still named alice", all[0])
	}
}
