package photos_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/vectorindex"
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
		"get":       func(_, _, a string) error { _, err := service.Get(ctx, namedBob, a); return err },
		"open":      func(_, _, a string) error { _, err := service.Open(ctx, namedBob, a); return err },
		"thumbnail": func(_, _, a string) error { _, err := service.Thumbnail(ctx, namedBob, a); return err },
		"similar": func(_, _, a string) error {
			_, err := service.Similar(ctx, namedBob, a, photos.SimilarRequest{})
			return err
		},
		"timeline":    func(l, _, _ string) error { _, err := service.Timeline(ctx, namedBob, l, "", 10); return err },
		"entries":     func(l, d, _ string) error { _, err := service.ListDirectory(ctx, namedBob, l, d, "", 0); return err },
		"trash":       func(l, _, _ string) error { _, err := service.ListTrash(ctx, namedBob, l); return err },
		"empty trash": func(l, _, _ string) error { _, err := service.EmptyTrash(ctx, namedBob, l); return err },
		"rename": func(_, _, a string) error {
			_, err := service.Update(ctx, namedBob, a, photos.AssetUpdate{Name: new("x.png")})
			return err
		},
		"trash asset":   func(_, _, a string) error { _, err := service.Trash(ctx, namedBob, a); return err },
		"restore":       func(_, _, a string) error { _, err := service.Restore(ctx, namedBob, a); return err },
		"purge":         func(_, _, a string) error { return service.Purge(ctx, namedBob, a) },
		"copy out":      func(_, _, a string) error { _, err := service.Copy(ctx, namedBob, a, bobPrivate.ID, ""); return err },
		"copy in":       func(l, _, _ string) error { _, err := service.Copy(ctx, namedBob, bobAsset.ID, l, ""); return err },
		"import":        func(l, _, _ string) error { return importInto(ctx, service, namedBob, l, "") },
		"import to dir": func(_, d, _ string) error { return importInto(ctx, service, namedBob, bobPrivate.ID, d) },
		"move to dir": func(_, d, _ string) error {
			_, err := service.Update(ctx, namedBob, bobAsset.ID, photos.AssetUpdate{DirectoryID: new(d)})
			return err
		},
		"create dir": func(l, _, _ string) error { _, err := service.CreateDirectory(ctx, namedBob, l, "", "x"); return err },
		"rename dir": func(_, d, _ string) error {
			_, err := service.UpdateDirectory(ctx, namedBob, d, photos.DirectoryUpdate{Name: new("x")})
			return err
		},
		"delete dir": func(_, d, _ string) error { return service.DeleteDirectory(ctx, namedBob, d) },
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
			listing, err := service.ListDirectory(ctx, namedAlice, alicePrivate.ID, "", "", 0)
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

func TestSharedCopyNamesTheEarliestSharedAssetWithTheSameOriginal(t *testing.T) {
	ctx := context.Background()
	service, c, _ := newService(t)
	alicePrivate, shared := libraries(t, service, namedAlice)
	same := encodePNG(60)

	mine := importPhoto(t, service, namedAlice, alicePrivate.ID, "", "baby.png", same)
	other := importPhoto(t, service, namedAlice, alicePrivate.ID, "", "other.png", encodePNG(61))
	if mine.SharedCopy != "" {
		t.Fatalf("a photo only in the private library names shared copy %q", mine.SharedCopy)
	}
	c.Advance(time.Minute)
	copied, err := service.Copy(ctx, namedAlice, mine.ID, shared.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	c.Advance(time.Minute)
	later := importPhoto(t, service, namedBob, shared.ID, "", "IMG_2.png", same)

	sharedCopyOf := func(p photos.Principal) map[string]string {
		t.Helper()
		page, err := service.Timeline(ctx, p, alicePrivate.ID, "", 10)
		if err != nil {
			t.Fatal(err)
		}
		found := map[string]string{}
		for _, asset := range page.Assets {
			found[asset.ID] = asset.SharedCopy
		}
		return found
	}
	if got := sharedCopyOf(namedAlice); got[mine.ID] != copied.ID || got[other.ID] != "" {
		t.Fatalf("shared copies = %v; want %s for the copied photo only", got, copied.ID)
	}
	if got, err := service.Get(ctx, namedAlice, mine.ID); err != nil || got.SharedCopy != copied.ID {
		t.Fatalf("Get shared copy = %q, %v; want %s", got.SharedCopy, err, copied.ID)
	}
	if got, err := service.Get(ctx, namedAlice, copied.ID); err != nil || got.SharedCopy != "" {
		t.Fatalf("a shared photo names shared copy %q, %v", got.SharedCopy, err)
	}
	viewer := photos.Principal{UserID: "user:admin", Username: "admin", Admin: true, Viewing: []photos.ViewingGrant{
		{GrantID: "viewing:1", OwnerUserID: namedAlice.UserID, ExpiresAt: c.Now().Add(time.Hour)},
	}}
	if got := sharedCopyOf(viewer); got[mine.ID] != "" {
		t.Fatalf("viewing administrator sees shared copy %q", got[mine.ID])
	}

	// A trashed copy no longer counts; the next one does.
	if _, err := service.Trash(ctx, namedAlice, copied.ID); err != nil {
		t.Fatal(err)
	}
	if got := sharedCopyOf(namedAlice); got[mine.ID] != later.ID {
		t.Fatalf("after trashing the copy, shared copy = %q; want %s", got[mine.ID], later.ID)
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

// An index hit grants nothing: whatever a search ranks, its page shows only
// assets of the libraries the search covers.
func TestARankedHitOutsideTheSearchedLibrariesIsDropped(t *testing.T) {
	ctx := context.Background()
	service, _, _ := newService(t)
	alicePrivate, shared := libraries(t, service, alice)
	bobPrivate, _ := libraries(t, service, bob)
	mine := importPhoto(t, service, alice, alicePrivate.ID, "", "mine.png", pngBytes(t, 1))
	theirs := importPhoto(t, service, bob, bobPrivate.ID, "", "theirs.png", pngBytes(t, 2))
	shown, err := photos.RecheckHits(ctx, service, alice, []string{alicePrivate.ID, shared.ID}, theirs.ID, mine.ID)
	if err != nil || !slices.Equal(shown, []string{mine.ID}) {
		t.Fatalf("recheck of bob's and alice's photo = %v, %v; want alice's alone", shown, err)
	}
}

// A window search answers only from the grant's own videos: neither another
// member's nor the same member's other project's, however like the text
// they are, nor a point the index holds under the grant's video but keyed
// to another. Another member's grant, a revoked one and none at all refuse
// alike.
func TestAWindowSearchFindsOnlyTheGrantsVideos(t *testing.T) {
	ctx := context.Background()
	f := newSearchFixture(t)
	db := catalog(t, f.root)
	const text = "sunset over the sea"
	for _, object := range []string{f.hikeObject, f.gardenObject} {
		if _, err := db.Exec("UPDATE video_windows SET vector = ? WHERE object_id = ?", queryVector(text), object); err != nil {
			t.Fatal(err)
		}
	}
	w := newWindowIndex(t, &f.montageFixture)
	w.sync(t)
	collection, err := w.index.Alias(ctx, "video_windows_v1")
	if err != nil || collection == "" {
		t.Fatalf("Alias() = %q, %v", collection, err)
	}
	forged := decodeFloats(queryVector(text))
	if err := w.index.Upsert(ctx, collection, []vectorindex.Point{{Key: f.gardenObject + "#0", Vector: forged,
		Keywords: map[string][]string{"object": {f.tripObject}}}}); err != nil {
		t.Fatal(err)
	}
	for _, index := range []photos.VectorIndex{w.index, nil} {
		photos.UseVectorIndex(f.service, index)
		for _, search := range []struct {
			owner photos.Principal
			grant photos.Grant
			sees  []string
		}{
			{namedAlice, f.tripGrant, []string{f.trip.ID, f.beach.ID}},
			{namedAlice, f.hikeGrant, []string{f.hike.ID}},
			{namedBob, f.gardenGrant, []string{f.garden.ID}},
		} {
			matches, err := f.service.SearchWindows(ctx, granting(search.owner), search.grant.ID, []string{text}, photos.MaxWindowMatches)
			if err != nil || len(matches[0]) == 0 {
				t.Fatalf("SearchWindows(%s) = %+v, %v", search.grant.ProjectID, matches, err)
			}
			for _, match := range matches[0] {
				if !slices.Contains(search.sees, match.AssetID) {
					t.Fatalf("index %v: %s found %+v outside its videos", index != nil, search.grant.ProjectID, match)
				}
			}
		}
	}
	missing := func(p photos.Principal, grantID string) error {
		_, err := f.service.SearchWindows(ctx, granting(p), grantID, []string{text}, 5)
		return err
	}
	if err := f.service.RevokeGrant(ctx, f.hikeGrant.ID); err != nil {
		t.Fatal(err)
	}
	nothing := missing(namedBob, "grant:missing")
	for name, err := range map[string]error{
		"bob with alice's":   missing(namedBob, f.tripGrant.ID),
		"alice with bob's":   missing(namedAlice, f.gardenGrant.ID),
		"alice with revoked": missing(namedAlice, f.hikeGrant.ID),
	} {
		if !errors.Is(err, photos.ErrGrantNotFound) || err.Error() != nothing.Error() {
			t.Errorf("%s: %v; want as for no grant, %v", name, err, nothing)
		}
	}
	// Only the montage center's grant Principal searches.
	if _, err := f.service.SearchWindows(ctx, namedAlice, f.tripGrant.ID, []string{text}, 5); !errors.Is(err, photos.ErrForbidden) {
		t.Errorf("a member's own Principal searching a grant = %v", err)
	}
}

// A video that left the grant's reach is searched no more, and the answer
// is the one of a grant whose video does not exist: its uploader put it in
// the shared library's trash, where the grant's owner saw it; it was taken
// out of the grant; or it is gone. The index still holds its points each
// time, and they find nothing. Once the owner's account is removed, the
// grant is as one that never was.
func TestAWindowSearchForgetsWhatLeftTheGrant(t *testing.T) {
	ctx := context.Background()
	f := newSearchFixture(t)
	// Bob's video in the library everyone shares: Alice sees it, and hands
	// it to her project. It is what the text is most like.
	_, shared := libraries(t, f.service, namedBob)
	party := importPhoto(t, f.service, namedBob, shared.ID, "", "party.mp4", mp4Bytes(5))
	if _, err := f.service.RegisterGrant(ctx, granting(namedAlice), "project:trip", []string{party.ID}); err != nil {
		t.Fatal(err)
	}
	if err := f.service.AnalyzeGrant(ctx, granting(namedAlice), f.tripGrant.ID, []string{party.ID}, false); err != nil {
		t.Fatal(err)
	}
	processAI(t, f.service, f.embedder, f.gate)
	looked, err := f.service.LookupGrant(ctx, granting(namedAlice), f.tripGrant.ID, []string{party.ID})
	if err != nil || len(looked) != 1 || looked[0].Windows != photos.AnalysisDone {
		t.Fatalf("the party in Alice's grant = %+v, %v", looked, err)
	}
	partyObject := looked[0].ObjectID
	const text = "sunset over the sea"
	if _, err := catalog(t, f.root).Exec("UPDATE video_windows SET vector = ? WHERE object_id = ?", queryVector(text), partyObject); err != nil {
		t.Fatal(err)
	}
	w := newWindowIndex(t, &f.montageFixture)
	w.sync(t)
	// search answers through the index and from the Catalog, alike.
	search := func(stage string) []photos.WindowMatch {
		t.Helper()
		var answers [2][][]photos.WindowMatch
		for i, index := range []photos.VectorIndex{w.index, nil} {
			photos.UseVectorIndex(f.service, index)
			matches, err := f.service.SearchWindows(ctx, granting(namedAlice), f.tripGrant.ID, []string{text}, photos.MaxWindowMatches)
			if err != nil {
				t.Fatalf("%s: SearchWindows() = %v", stage, err)
			}
			answers[i] = matches
		}
		if !reflect.DeepEqual(answers[0], answers[1]) {
			t.Fatalf("%s: the index found\n%+v\nthe Catalog\n%+v", stage, answers[0], answers[1])
		}
		return answers[0][0]
	}
	// indexed checks that the index still holds the party's points.
	indexed := func(stage string) {
		t.Helper()
		if points, _ := w.points(t); points[partyObject] != 35 {
			t.Fatalf("%s: the index holds %d points of the party, want its 35", stage, points[partyObject])
		}
	}
	without := func(stage string, found []photos.WindowMatch) {
		t.Helper()
		if len(found) != 70 || slices.ContainsFunc(found, func(match photos.WindowMatch) bool {
			return match.AssetID != f.trip.ID && match.AssetID != f.beach.ID
		}) {
			t.Fatalf("%s: found %d windows, want the 70 of the trip and the beach alone: %+v", stage, len(found), found)
		}
	}
	all := search("granted")
	if len(all) != 105 || all[0].AssetID != party.ID {
		t.Fatalf("granted: found %d windows, first %+v; want the party's first of 105", len(all), all[0])
	}

	// Bob puts his video in the trash: Alice no longer sees it.
	if _, err := f.service.Trash(ctx, namedBob, party.ID); err != nil {
		t.Fatal(err)
	}
	indexed("in the trash")
	trashed := search("in the trash")
	without("in the trash", trashed)
	if _, err := f.service.Restore(ctx, namedBob, party.ID); err != nil {
		t.Fatal(err)
	}
	w.sync(t)
	if again := search("restored"); !reflect.DeepEqual(again, all) {
		t.Fatalf("restored: found %+v, want as before", again)
	}

	// Taken out of the grant: nothing about the video itself changed.
	if err := f.service.RemoveFromGrant(ctx, granting(namedAlice), f.tripGrant.ID, []string{party.ID}); err != nil {
		t.Fatal(err)
	}
	indexed("out of the grant")
	if removed := search("out of the grant"); !reflect.DeepEqual(removed, trashed) {
		t.Fatalf("out of the grant: found %+v, want as with the party in the trash", removed)
	}

	// Gone altogether, in the grant again or not: the same answer, so
	// neither of the above says more than that there is no such video.
	if _, err := f.service.Trash(ctx, namedBob, party.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Purge(ctx, namedBob, party.ID); err != nil {
		t.Fatal(err)
	}
	if gone := search("gone"); !reflect.DeepEqual(gone, trashed) {
		t.Fatalf("gone: found %+v, want as with the party in the trash", gone)
	}

	// Alice's own video in the trash, its points still in the index.
	if _, err := f.service.Trash(ctx, namedAlice, f.beach.ID); err != nil {
		t.Fatal(err)
	}
	if points, _ := w.points(t); points[f.beachObject] != 35 {
		t.Fatalf("the index holds %d points of the beach, want its 35", points[f.beachObject])
	}
	if found := search("her own in the trash"); len(found) != 35 || slices.ContainsFunc(found, func(match photos.WindowMatch) bool {
		return match.AssetID != f.trip.ID
	}) {
		t.Fatalf("her own in the trash: found %+v, want the trip's 35 windows alone", found)
	}

	// The owner's account removed: her grant refuses as no grant does.
	if _, err := f.service.RemoveAccount(ctx, photos.AccountRemoval{UserID: namedAlice.UserID}); err != nil {
		t.Fatal(err)
	}
	_, nothing := f.service.SearchWindows(ctx, granting(namedAlice), "grant:missing", []string{text}, 5)
	for _, index := range []photos.VectorIndex{w.index, nil} {
		photos.UseVectorIndex(f.service, index)
		_, err := f.service.SearchWindows(ctx, granting(namedAlice), f.tripGrant.ID, []string{text}, 5)
		if !errors.Is(err, photos.ErrGrantNotFound) || err.Error() != nothing.Error() {
			t.Errorf("index %v: after the owner's account went: %v; want as for no grant, %v", index != nil, err, nothing)
		}
	}
}

func decodeFloats(encoded []byte) []float32 {
	vector := make([]float32, len(encoded)/4)
	for i := range vector {
		vector[i] = math.Float32frombits(binary.LittleEndian.Uint32(encoded[4*i:]))
	}
	return vector
}
