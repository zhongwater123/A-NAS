package photos_test

import (
	"context"
	"errors"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/photos"
)

func TestRemovingAnAccountHandsItsPhotosToTheAdministrator(t *testing.T) {
	service, _, root := newService(t)
	ctx := context.Background()
	alicePrivate, shared := libraries(t, service, alice)
	adminPrivate, _ := libraries(t, service, admin)
	kept := importPhoto(t, service, alice, alicePrivate.ID, "", "kept.png", pngBytes(t, 1))
	trashed := importPhoto(t, service, alice, alicePrivate.ID, "", "trashed.png", pngBytes(t, 2))
	if _, err := service.Trash(ctx, alice, trashed.ID); err != nil {
		t.Fatal(err)
	}
	inShared := importPhoto(t, service, alice, shared.ID, "", "shared.png", pngBytes(t, 3))
	if _, err := service.CreateAlbum(ctx, admin, adminPrivate.ID, "alice 的照片"); err != nil {
		t.Fatal(err)
	}
	before := len(objectFiles(t, root))

	removal := photos.AccountRemoval{UserID: alice.UserID, Transfer: true, AdminID: admin.UserID, AdminUsername: "admin", Album: "alice 的照片"}
	if err := service.RemoveAccount(ctx, removal); err != nil {
		t.Fatalf("RemoveAccount() error = %v", err)
	}
	// The administrator had an album of that name; the new one gets a number.
	albums, err := service.Albums(ctx, admin, adminPrivate.ID)
	if err != nil {
		t.Fatal(err)
	}
	var handed photos.Album
	for _, album := range albums {
		if album.Name == "alice 的照片 (2)" {
			handed = album
		}
	}
	if handed.ID == "" || handed.Photos != 1 {
		t.Fatalf("albums = %+v, want alice 的照片 (2) with the one kept photo", albums)
	}
	page, err := service.AlbumPhotos(ctx, admin, handed.ID, "", 10)
	if err != nil || len(page.Assets) != 1 || page.Assets[0].Name != kept.Name || page.Assets[0].LibraryID != adminPrivate.ID {
		t.Fatalf("handed-over photos = %+v, %v", page.Assets, err)
	}
	if _, err := service.Get(ctx, alice, kept.ID); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("alice's photo is still there: %v", err)
	}
	if _, err := service.Get(ctx, admin, inShared.ID); err != nil {
		t.Fatalf("the shared photo alice uploaded is gone: %v", err)
	}
	// The trashed photo's original went; the kept one's is shared with the copy.
	if after := len(objectFiles(t, root)); after != before-1 {
		t.Fatalf("object files %d → %d, want one fewer", before, after)
	}
	// Sent again, it finds nothing left to do.
	if err := service.RemoveAccount(ctx, removal); err != nil {
		t.Fatalf("RemoveAccount() again error = %v", err)
	}
	if albums, _ := service.Albums(ctx, admin, adminPrivate.ID); len(albums) != 2 {
		t.Fatalf("a second removal made another album: %+v", albums)
	}
}

func TestRemovingAnAccountWithoutTransferDeletesItsLibrary(t *testing.T) {
	service, _, root := newService(t)
	ctx := context.Background()
	alicePrivate, _ := libraries(t, service, alice)
	bobPrivate, _ := libraries(t, service, bob)
	only := importPhoto(t, service, alice, alicePrivate.ID, "", "only.png", pngBytes(t, 4))
	both := pngBytes(t, 5)
	importPhoto(t, service, alice, alicePrivate.ID, "", "both.png", both)
	bobs := importPhoto(t, service, bob, bobPrivate.ID, "", "both.png", both)
	before := len(objectFiles(t, root))

	if err := service.RemoveAccount(ctx, photos.AccountRemoval{UserID: alice.UserID}); err != nil {
		t.Fatalf("RemoveAccount() error = %v", err)
	}
	if _, err := service.Get(ctx, admin, only.ID); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("alice's photo is still there: %v", err)
	}
	if _, err := service.Get(ctx, bob, bobs.ID); err != nil {
		t.Fatalf("bob's copy of the same original is gone: %v", err)
	}
	if after := len(objectFiles(t, root)); after != before-1 {
		t.Fatalf("object files %d → %d, want only alice's own original gone", before, after)
	}
	// A new account of that name starts with an empty library.
	again := photos.Principal{UserID: "user:alice-2"}
	fresh, _ := libraries(t, service, again)
	if page, err := service.Timeline(ctx, again, fresh.ID, "", 10); err != nil || len(page.Assets) != 0 {
		t.Fatalf("the new account's library = %+v, %v", page.Assets, err)
	}
}

func TestRemovingAnAccountRefusesToHandItsPhotosToItself(t *testing.T) {
	service, _, _ := newService(t)
	err := service.RemoveAccount(context.Background(), photos.AccountRemoval{UserID: alice.UserID, Transfer: true, AdminID: alice.UserID})
	if err == nil {
		t.Fatal("an account's photos were handed to itself")
	}
}
