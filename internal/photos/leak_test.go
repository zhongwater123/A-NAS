package photos_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/photos"
)

// members are named, as the photo service learns from the session lookup.
var (
	namedAlice = photos.Principal{UserID: "user:alice", Username: "alice"}
	namedBob   = photos.Principal{UserID: "user:bob", Username: "bob"}
	namedCarol = photos.Principal{UserID: "user:carol", Username: "carol"}
)

// TestAnotherMembersPrivateLibraryIsIndistinguishableFromNothing checks every
// read and write path a member has into another member's private library:
// each fails exactly as it would for an ID that does not exist.
func TestAnotherMembersPrivateLibraryIsIndistinguishableFromNothing(t *testing.T) {
	ctx := context.Background()
	service, _, _ := newService(t)
	alicePrivate, shared := libraries(t, service, namedAlice)
	bobPrivate, _ := libraries(t, service, namedBob)
	aliceDir, err := service.CreateDirectory(ctx, namedAlice, alicePrivate.ID, "", "Secret")
	if err != nil {
		t.Fatal(err)
	}
	aliceAsset := importPhoto(t, service, namedAlice, alicePrivate.ID, aliceDir.ID, "secret.png", encodePNG(40))
	aliceTrashed := importPhoto(t, service, namedAlice, alicePrivate.ID, "", "trashed.png", encodePNG(41))
	if _, err := service.Trash(ctx, namedAlice, aliceTrashed.ID); err != nil {
		t.Fatal(err)
	}
	bobAsset := importPhoto(t, service, namedBob, bobPrivate.ID, "", "mine.png", encodePNG(42))
	importPhoto(t, service, namedAlice, shared.ID, "", "for everyone.png", encodePNG(43))

	type probe func(library, directory, asset string) error
	probes := map[string]probe{
		"get":           func(_, _, a string) error { _, err := service.Get(ctx, namedBob, a); return err },
		"open":          func(_, _, a string) error { _, err := service.Open(ctx, namedBob, a); return err },
		"thumbnail":     func(_, _, a string) error { _, err := service.Thumbnail(ctx, namedBob, a); return err },
		"timeline":      func(l, _, _ string) error { _, err := service.Timeline(ctx, namedBob, l, "", 10); return err },
		"entries":       func(l, d, _ string) error { _, err := service.ListDirectory(ctx, namedBob, l, d); return err },
		"trash":         func(l, _, _ string) error { _, err := service.ListTrash(ctx, namedBob, l); return err },
		"empty trash":   func(l, _, _ string) error { _, err := service.EmptyTrash(ctx, namedBob, l); return err },
		"rename":        func(_, _, a string) error { _, err := service.Rename(ctx, namedBob, a, "x.png"); return err },
		"trash asset":   func(_, _, a string) error { _, err := service.Trash(ctx, namedBob, a); return err },
		"restore":       func(_, _, a string) error { _, err := service.Restore(ctx, namedBob, a); return err },
		"purge":         func(_, _, a string) error { return service.Purge(ctx, namedBob, a) },
		"copy out":      func(_, _, a string) error { _, err := service.Copy(ctx, namedBob, a, bobPrivate.ID, ""); return err },
		"copy in":       func(l, _, _ string) error { _, err := service.Copy(ctx, namedBob, bobAsset.ID, l, ""); return err },
		"import":        func(l, _, _ string) error { return importInto(ctx, service, namedBob, l, "") },
		"import to dir": func(_, d, _ string) error { return importInto(ctx, service, namedBob, bobPrivate.ID, d) },
		"move to dir":   func(_, d, _ string) error { _, err := service.Move(ctx, namedBob, bobAsset.ID, d); return err },
		"create dir":    func(l, _, _ string) error { _, err := service.CreateDirectory(ctx, namedBob, l, "", "x"); return err },
		"rename dir":    func(_, d, _ string) error { _, err := service.RenameDirectory(ctx, namedBob, d, "x"); return err },
		"delete dir":    func(_, d, _ string) error { return service.DeleteDirectory(ctx, namedBob, d) },
		"move dir under": func(_, d, _ string) error {
			_, err := service.CreateDirectory(ctx, namedBob, bobPrivate.ID, d, "x")
			return err
		},
	}
	for name, probe := range probes {
		hidden := probe(alicePrivate.ID, aliceDir.ID, aliceAsset.ID)
		missing := probe("library:missing", "photo-directory:missing", "photo:missing")
		if !errors.Is(hidden, photos.ErrNotFound) || hidden.Error() != missing.Error() {
			t.Errorf("%s: hidden error %v, missing error %v; want the same not-found", name, hidden, missing)
		}
	}
	if err := service.Purge(ctx, namedBob, aliceTrashed.ID); !errors.Is(err, photos.ErrNotFound) {
		t.Errorf("purging another member's trashed photo error = %v", err)
	}

	// Bob's view of what everyone shares counts only the shared photo.
	page, err := service.Timeline(ctx, namedBob, shared.ID, "", 100)
	if err != nil || len(page.Assets) != 1 || page.Assets[0].Name != "for everyone.png" {
		t.Fatalf("shared timeline for bob = %+v, %v", page.Assets, err)
	}
	all, err := service.Libraries(ctx, namedBob)
	if err != nil {
		t.Fatal(err)
	}
	for _, lib := range all {
		if lib.ID == alicePrivate.ID {
			t.Fatalf("bob's libraries include alice's private library")
		}
	}
	// Nothing Bob did changed Alice's library.
	if got, err := service.Get(ctx, namedAlice, aliceAsset.ID); err != nil || got.Name != "secret.png" || got.Trash != nil {
		t.Fatalf("alice's photo changed: %+v, %v", got, err)
	}
}

func importInto(ctx context.Context, service *photos.Service, p photos.Principal, libraryID, directoryID string) error {
	_, err := service.Import(ctx, p, photos.ImportRequest{
		LibraryID: libraryID, DirectoryID: directoryID, Name: "probe.png", Content: bytes.NewReader(encodePNG(99)),
	})
	return err
}

func TestCrossMemberDuplicateHintRevealsOnlyTheOtherMembersName(t *testing.T) {
	ctx := context.Background()
	service, c, _ := newService(t)
	alicePrivate, shared := libraries(t, service, namedAlice)
	bobPrivate, _ := libraries(t, service, namedBob)
	carolPrivate, _ := libraries(t, service, namedCarol)
	same := encodePNG(50)

	aliceCopy := importPhoto(t, service, namedAlice, alicePrivate.ID, "", "beach.png", same)
	if aliceCopy.AlsoKeptBy != nil {
		t.Fatalf("first import hints %v", aliceCopy.AlsoKeptBy)
	}
	bobCopy := importPhoto(t, service, namedBob, bobPrivate.ID, "", "IMG_1.png", same)
	if !reflect.DeepEqual(bobCopy.AlsoKeptBy, []string{"alice"}) {
		t.Fatalf("bob's import hint = %v, want alice", bobCopy.AlsoKeptBy)
	}
	other := importPhoto(t, service, namedCarol, carolPrivate.ID, "", "other.png", encodePNG(51))
	if other.AlsoKeptBy != nil {
		t.Fatalf("an unrelated photo hints %v", other.AlsoKeptBy)
	}
	inShared := importPhoto(t, service, namedBob, shared.ID, "", "beach.png", same)
	if inShared.AlsoKeptBy != nil {
		t.Fatalf("shared library photo hints %v", inShared.AlsoKeptBy)
	}

	for name, read := range map[string]func() ([]photos.Asset, error){
		"get": func() ([]photos.Asset, error) {
			asset, err := service.Get(ctx, namedAlice, aliceCopy.ID)
			return []photos.Asset{asset}, err
		},
		"timeline": func() ([]photos.Asset, error) {
			page, err := service.Timeline(ctx, namedAlice, alicePrivate.ID, "", 10)
			return page.Assets, err
		},
		"entries": func() ([]photos.Asset, error) {
			listing, err := service.ListDirectory(ctx, namedAlice, alicePrivate.ID, "")
			return listing.Assets, err
		},
	} {
		assets, err := read()
		if err != nil || len(assets) != 1 || !reflect.DeepEqual(assets[0].AlsoKeptBy, []string{"bob"}) {
			t.Errorf("%s for alice = %+v, %v; want a hint naming bob only", name, assets, err)
		}
	}

	// An administrator viewing Alice's library learns nothing about Bob.
	viewer := photos.Principal{UserID: "user:admin", Username: "admin", Admin: true, Viewing: []photos.ViewingGrant{
		{GrantID: "viewing:1", OwnerUserID: namedAlice.UserID, ExpiresAt: c.Now().Add(time.Hour)},
	}}
	if viewed, err := service.Get(ctx, viewer, aliceCopy.ID); err != nil || viewed.AlsoKeptBy != nil {
		t.Fatalf("viewing administrator sees hint %v, %v", viewed.AlsoKeptBy, err)
	}

	// Once Bob trashes his copy the hint goes away.
	if _, err := service.Trash(ctx, namedBob, bobCopy.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := service.Get(ctx, namedAlice, aliceCopy.ID); got.AlsoKeptBy != nil {
		t.Fatalf("hint survives the other member trashing the photo: %v", got.AlsoKeptBy)
	}
}

func TestViewedLibraryCarriesItsGrant(t *testing.T) {
	ctx := context.Background()
	service, c, _ := newService(t)
	alicePrivate, _ := libraries(t, service, namedAlice)
	expires := c.Now().Add(24 * time.Hour)
	viewer := photos.Principal{UserID: "user:admin", Username: "admin", Admin: true, Viewing: []photos.ViewingGrant{
		{GrantID: "viewing:short", OwnerUserID: namedAlice.UserID, ExpiresAt: c.Now().Add(time.Hour)},
		{GrantID: "viewing:long", OwnerUserID: namedAlice.UserID, ExpiresAt: expires},
	}}
	all, err := service.Libraries(ctx, viewer)
	if err != nil {
		t.Fatal(err)
	}
	var viewed *photos.Library
	for i := range all {
		if all[i].ID == alicePrivate.ID {
			viewed = &all[i]
		}
	}
	if viewed == nil || viewed.OwnerName != "alice" || viewed.Viewing == nil ||
		viewed.Viewing.GrantID != "viewing:long" || !viewed.Viewing.ExpiresAt.Equal(expires) {
		t.Fatalf("viewed library = %+v", viewed)
	}
	for _, lib := range all {
		if lib.ID != alicePrivate.ID && lib.Viewing != nil {
			t.Fatalf("library %s carries a grant it does not need: %+v", lib.ID, lib.Viewing)
		}
	}
}
