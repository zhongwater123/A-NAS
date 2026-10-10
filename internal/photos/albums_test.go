package photos_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/photos"
)

func albumNames(t *testing.T, service *photos.Service, p photos.Principal, libraryID string) []string {
	t.Helper()
	albums, err := service.Albums(context.Background(), p, libraryID)
	if err != nil {
		t.Fatalf("Albums() error = %v", err)
	}
	var names []string
	for _, album := range albums {
		names = append(names, album.Name)
	}
	return names
}

func albumPhotos(t *testing.T, service *photos.Service, p photos.Principal, albumID string) []string {
	t.Helper()
	page, err := service.AlbumPhotos(context.Background(), p, albumID, "", 0)
	if err != nil {
		t.Fatalf("AlbumPhotos() error = %v", err)
	}
	var ids []string
	for _, asset := range page.Assets {
		ids = append(ids, asset.ID)
	}
	return ids
}

func details(t *testing.T, service *photos.Service, p photos.Principal, assetID string) photos.Asset {
	t.Helper()
	asset, err := service.Get(context.Background(), p, assetID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	return asset
}

func TestAlbumsGroupPhotosOfTheirOwnLibrary(t *testing.T) {
	service, c, _ := newService(t)
	ctx := context.Background()
	private, shared := libraries(t, service, namedAlice)
	mine := importPhoto(t, service, namedAlice, private.ID, "", "mine.png", encodePNG(10))
	c.Advance(time.Minute)
	theirs := importPhoto(t, service, namedBob, shared.ID, "", "theirs.png", encodePNG(11))
	if _, err := service.AddTag(ctx, namedBob, theirs.ID, "海边"); err != nil {
		t.Fatal(err)
	}
	trip, err := service.CreateAlbum(ctx, namedAlice, private.ID, "旅行")
	if err != nil {
		t.Fatalf("CreateAlbum() error = %v", err)
	}

	// A photo of the album's library joins as itself, once.
	for range 2 {
		added, err := service.AddToAlbum(ctx, namedAlice, trip.ID, mine.ID)
		if err != nil || added.ID != mine.ID {
			t.Fatalf("AddToAlbum(same library) = %s, %v; want the photo itself", added.ID, err)
		}
	}
	// A photo of another library is copied in, tags and all.
	copied, err := service.AddToAlbum(ctx, namedAlice, trip.ID, theirs.ID)
	if err != nil || copied.ID == theirs.ID || copied.LibraryID != private.ID || !slices.Equal(copied.Tags, []string{"海边"}) {
		t.Fatalf("AddToAlbum(other library) = %+v, %v; want a tagged copy in Alice's library", copied, err)
	}
	if got := albumPhotos(t, service, namedAlice, trip.ID); !slices.Equal(got, []string{copied.ID, mine.ID}) {
		t.Fatalf("album photos = %v", got)
	}
	if got := details(t, service, namedAlice, mine.ID).Albums; len(got) != 1 || got[0].Name != "旅行" {
		t.Fatalf("photo albums = %+v", got)
	}
	if page, _ := service.Timeline(ctx, namedAlice, private.ID, "", 0); len(page.Assets) != 2 {
		t.Fatalf("timeline holds %d photos, want each photo once", len(page.Assets))
	}

	// Trashed photos leave the album's listing until restored.
	if _, err := service.Trash(ctx, namedAlice, copied.ID); err != nil {
		t.Fatal(err)
	}
	albums, _ := service.Albums(ctx, namedAlice, private.ID)
	if got := albumPhotos(t, service, namedAlice, trip.ID); !slices.Equal(got, []string{mine.ID}) || albums[0].Photos != 1 || albums[0].CoverID != mine.ID {
		t.Fatalf("album with a trashed photo = %v, %+v", got, albums[0])
	}
	if _, err := service.Restore(ctx, namedAlice, copied.ID); err != nil {
		t.Fatal(err)
	}
	if got := albumPhotos(t, service, namedAlice, trip.ID); len(got) != 2 {
		t.Fatalf("album after a restore = %v", got)
	}
	if _, err := service.AddToAlbum(ctx, namedAlice, trip.ID, "photo:missing"); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("AddToAlbum(missing) error = %v", err)
	}

	// Removing a photo or deleting the album leaves the photos in place.
	if err := service.RemoveFromAlbum(ctx, namedAlice, trip.ID, mine.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.RemoveFromAlbum(ctx, namedAlice, trip.ID, mine.ID); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("RemoveFromAlbum() twice error = %v", err)
	}
	if _, err := service.CreateAlbum(ctx, namedAlice, private.ID, "旅行"); !errors.Is(err, photos.ErrConflict) {
		t.Fatalf("CreateAlbum(same name) error = %v", err)
	}
	for _, name := range []string{"", "a/b", strings.Repeat("长", 300)} {
		if _, err := service.CreateAlbum(ctx, namedAlice, private.ID, name); !errors.Is(err, photos.ErrInvalidName) {
			t.Fatalf("CreateAlbum(%q) error = %v", name, err)
		}
	}
	if renamed, err := service.RenameAlbum(ctx, namedAlice, trip.ID, "2026 旅行"); err != nil || renamed.Name != "2026 旅行" {
		t.Fatalf("RenameAlbum() = %+v, %v", renamed, err)
	}
	if err := service.DeleteAlbum(ctx, namedAlice, trip.ID); err != nil {
		t.Fatal(err)
	}
	if got := albumNames(t, service, namedAlice, private.ID); len(got) != 0 {
		t.Fatalf("albums after deleting = %v", got)
	}
	if page, _ := service.Timeline(ctx, namedAlice, private.ID, "", 0); len(page.Assets) != 2 {
		t.Fatalf("deleting the album removed photos: %d left", len(page.Assets))
	}
}

func TestAlbumsFollowTheLibraryPolicy(t *testing.T) {
	service, c, _ := newService(t)
	ctx := context.Background()
	alicePrivate, shared := libraries(t, service, namedAlice)
	bobPrivate, _ := libraries(t, service, namedBob)
	secret := importPhoto(t, service, namedAlice, alicePrivate.ID, "", "secret.png", encodePNG(20))
	common := importPhoto(t, service, namedBob, shared.ID, "", "common.png", encodePNG(21))
	private, err := service.CreateAlbum(ctx, namedAlice, alicePrivate.ID, "私人")
	if err != nil {
		t.Fatal(err)
	}
	family, err := service.CreateAlbum(ctx, namedAlice, shared.ID, "家庭")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddToAlbum(ctx, namedAlice, private.ID, secret.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddToAlbum(ctx, namedAlice, family.ID, common.ID); err != nil {
		t.Fatal(err)
	}

	// Another member sees shared albums, but only their creator or an
	// administrator changes them.
	if got := albumPhotos(t, service, namedBob, family.ID); !slices.Equal(got, []string{common.ID}) {
		t.Fatalf("Bob's view of the shared album = %v", got)
	}
	bobsAlbum, err := service.CreateAlbum(ctx, namedBob, bobPrivate.ID, "Bob")
	if err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"rename": func() error { _, err := service.RenameAlbum(ctx, namedBob, family.ID, "x"); return err }(),
		"delete": service.DeleteAlbum(ctx, namedBob, family.ID),
		"add":    func() error { _, err := service.AddToAlbum(ctx, namedBob, family.ID, common.ID); return err }(),
		"remove": service.RemoveFromAlbum(ctx, namedBob, family.ID, common.ID),
	} {
		if !errors.Is(err, photos.ErrForbidden) {
			t.Errorf("Bob may %s Alice's shared album: %v", name, err)
		}
	}
	admin := photos.Principal{UserID: "user:admin", Admin: true}
	if _, err := service.RenameAlbum(ctx, admin, family.ID, "全家"); err != nil {
		t.Fatalf("an administrator renaming a shared album: %v", err)
	}

	// Alice's private albums and photos are indistinguishable from nothing.
	for name, err := range map[string]error{
		"list":        func() error { _, err := service.Albums(ctx, namedBob, alicePrivate.ID); return err }(),
		"photos":      func() error { _, err := service.AlbumPhotos(ctx, namedBob, private.ID, "", 0); return err }(),
		"add to":      func() error { _, err := service.AddToAlbum(ctx, namedBob, private.ID, common.ID); return err }(),
		"add from":    func() error { _, err := service.AddToAlbum(ctx, namedBob, bobsAlbum.ID, secret.ID); return err }(),
		"create in":   func() error { _, err := service.CreateAlbum(ctx, namedBob, alicePrivate.ID, "x"); return err }(),
		"remove from": service.RemoveFromAlbum(ctx, namedBob, private.ID, secret.ID),
		"delete":      service.DeleteAlbum(ctx, namedBob, private.ID),
	} {
		if !errors.Is(err, photos.ErrNotFound) {
			t.Errorf("Bob could %s Alice's private album: %v", name, err)
		}
	}

	// Viewing Alice's library shows her albums read-only and copies nothing out.
	viewer := admin
	viewer.Viewing = []photos.ViewingGrant{{GrantID: "grant:1", OwnerUserID: namedAlice.UserID, ExpiresAt: c.Now().Add(time.Hour)}}
	if got := albumPhotos(t, service, viewer, private.ID); !slices.Equal(got, []string{secret.ID}) {
		t.Fatalf("viewing Alice's album = %v", got)
	}
	adminLibrary, _ := libraries(t, service, admin)
	adminAlbum, err := service.CreateAlbum(ctx, admin, adminLibrary.ID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteAlbum(ctx, viewer, private.ID); !errors.Is(err, photos.ErrForbidden) {
		t.Fatalf("deleting a viewed member's album error = %v", err)
	}
	if _, err := service.AddToAlbum(ctx, viewer, adminAlbum.ID, secret.ID); !errors.Is(err, photos.ErrForbidden) {
		t.Fatalf("copying a viewed member's photo into an album error = %v", err)
	}
}

func TestUserTagsAreTheCallersAndFindPhotos(t *testing.T) {
	service, _, _ := newService(t)
	ctx := context.Background()
	private, shared := libraries(t, service, namedAlice)
	party := importPhoto(t, service, namedAlice, private.ID, "", "IMG_1.png", encodePNG(30))
	sharedPhoto := importPhoto(t, service, namedAlice, shared.ID, "", "IMG_2.png", encodePNG(31))

	tagged, err := service.AddTag(ctx, namedAlice, party.ID, " 生日 ")
	if err != nil || !slices.Equal(tagged.Tags, []string{"生日"}) {
		t.Fatalf("AddTag() = %v, %v", tagged.Tags, err)
	}
	if again, err := service.AddTag(ctx, namedAlice, party.ID, "生日"); err != nil || len(again.Tags) != 1 {
		t.Fatalf("AddTag() twice = %v, %v", again.Tags, err)
	}
	for _, name := range []string{"", "a/b", strings.Repeat("长", photos.MaxTagRunes+1)} {
		if _, err := service.AddTag(ctx, namedAlice, party.ID, name); !errors.Is(err, photos.ErrInvalidName) {
			t.Fatalf("AddTag(%q) error = %v", name, err)
		}
	}
	// Without local AI, search still finds the tag.
	if page := search(t, service, namedAlice, "生日"); !slices.Equal(names(page.Assets), []string{"IMG_1.png"}) {
		t.Fatalf("search by tag = %v", names(page.Assets))
	}

	if _, err := service.AddTag(ctx, namedBob, party.ID, "x"); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("tagging another member's private photo error = %v", err)
	}
	if _, err := service.AddTag(ctx, namedBob, sharedPhoto.ID, "x"); !errors.Is(err, photos.ErrForbidden) {
		t.Fatalf("tagging a shared photo someone else uploaded error = %v", err)
	}
	if untagged, err := service.RemoveTag(ctx, namedAlice, party.ID, "生日"); err != nil || len(untagged.Tags) != 0 {
		t.Fatalf("RemoveTag() = %v, %v", untagged.Tags, err)
	}
	if _, err := service.RemoveTag(ctx, namedAlice, party.ID, "生日"); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("RemoveTag() twice error = %v", err)
	}
}

// AI labels are off, but the corrections users made while they were shown are
// user data: they stay with the photo and travel with its copies.
func TestAICorrectionsStayWithThePhoto(t *testing.T) {
	root := filepath.Join(t.TempDir(), "photos")
	service, err := photos.Open(root, photos.Options{Now: newClock().Now, DisableCapacityReserve: true})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	ctx := context.Background()
	private, shared := libraries(t, service, namedAlice)
	photo := importPhoto(t, service, namedAlice, private.ID, "", "a.png", red)
	db := catalogDB(t, root)
	if _, err := db.Exec(`INSERT INTO ai_tag_corrections(asset_id, label_id, verdict, created_by, created_at)
VALUES(?, 'frisbee', 'hidden', ?, '2026-10-09T00:00:00Z')`, photo.ID, namedAlice.UserID); err != nil {
		t.Fatal(err)
	}
	copied, err := service.Copy(ctx, namedAlice, photo.ID, shared.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{photo.ID, copied.ID} {
		var label string
		if err := db.QueryRow("SELECT label_id FROM ai_tag_corrections WHERE asset_id = ?", id).Scan(&label); err != nil || label != "frisbee" {
			t.Fatalf("correction of %s = %q, %v", id, label, err)
		}
	}
}
