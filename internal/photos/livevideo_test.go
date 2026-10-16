package photos_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/photos"
)

const liveID = "01234567-89AB-CDEF-0123-456789ABCDEF"

// liveService decodes every MOV as a short video carrying identifier, or
// none when identifier is empty.
func liveService(t *testing.T, identifier string) (*photos.Service, string) {
	t.Helper()
	probe := motionProbe()
	if identifier != "" {
		probe.Format.Tags = map[string]string{"com.apple.quicktime.content.identifier": identifier}
	}
	return motionService(t, probe)
}

func importStill(t *testing.T, s *photos.Service, p photos.Principal, libraryID, name string, content []byte) photos.Asset {
	t.Helper()
	asset, err := s.Import(context.Background(), p, photos.ImportRequest{LibraryID: libraryID, Name: name, Content: bytes.NewReader(content)})
	if err != nil {
		t.Fatalf("Import(%s) error = %v", name, err)
	}
	return asset
}

func attach(s *photos.Service, p photos.Principal, libraryID, name string) ([]photos.Asset, error) {
	return s.AttachLiveVideo(context.Background(), p, photos.LiveVideoRequest{LibraryID: libraryID, Name: name, Content: bytes.NewReader(movieBytes())})
}

func TestALiveStillWithoutItsVideoIsCompletedByTheVideoLater(t *testing.T) {
	ctx := context.Background()
	s, _ := liveService(t, liveID)
	private, _ := libraries(t, s, alice)
	still := importStill(t, s, alice, private.ID, "IMG_7.jpg", appleJPEG(t, liveID))
	plain := importStill(t, s, alice, private.ID, "plain.jpg", jpegBytes(t))
	if !still.LiveVideoMissing || still.Motion != nil {
		t.Fatalf("Live Photo still imported alone = %+v; want its video missing", still)
	}
	if plain.LiveVideoMissing {
		t.Fatalf("an ordinary photo says a Live Photo video is missing")
	}
	// A copy in the same library is the same Live Photo and waits too.
	duplicate := importStill(t, s, alice, private.ID, "IMG_7 copy.jpg", appleJPEG(t, liveID))

	completed, err := attach(s, alice, private.ID, "IMG_0007.MOV")
	if err != nil {
		t.Fatalf("AttachLiveVideo() error = %v", err)
	}
	if !sameIDs(completed, still.ID, duplicate.ID) {
		t.Fatalf("completed %+v; want the still and its duplicate, matched by identifier not name", completed)
	}
	for _, asset := range completed {
		if asset.Motion == nil || asset.Motion.Kind != "live-photo" || asset.LiveVideoMissing {
			t.Fatalf("completed asset = %+v", asset)
		}
	}
	video, err := s.OpenMotion(ctx, alice, still.ID)
	if err != nil {
		t.Fatal(err)
	}
	_ = video.Reader.Close()
	if video.Name != "IMG_7.MOV" {
		t.Fatalf("video named %q; want the still's name", video.Name)
	}
	// Nothing waits any more.
	if _, err := attach(s, alice, private.ID, "IMG_0007.MOV"); !errors.Is(err, photos.ErrNoLiveStill) {
		t.Fatalf("attaching again error = %v, want no waiting still", err)
	}
}

func TestALiveVideoNeverCompletesAPhotoItDoesNotBelongTo(t *testing.T) {
	s, _ := liveService(t, "another-identifier")
	private, _ := libraries(t, s, alice)
	importStill(t, s, alice, private.ID, "IMG_1.jpg", appleJPEG(t, liveID))
	importStill(t, s, alice, private.ID, "IMG_2.jpg", jpegBytes(t))
	if _, err := attach(s, alice, private.ID, "IMG_1.MOV"); !errors.Is(err, photos.ErrNoLiveStill) {
		t.Fatalf("mismatched identifier error = %v, want no waiting still", err)
	}
	if _, err := attach(s, alice, private.ID, "IMG_2.MOV"); !errors.Is(err, photos.ErrNoLiveStill) {
		t.Fatalf("a same-name photo without an identifier was completed: %v", err)
	}

	bare, _ := liveService(t, "")
	barePrivate, _ := libraries(t, bare, alice)
	importStill(t, bare, alice, barePrivate.ID, "IMG_1.jpg", appleJPEG(t, liveID))
	if _, err := attach(bare, alice, barePrivate.ID, "IMG_1.MOV"); !errors.Is(err, photos.ErrInvalidMotion) {
		t.Fatalf("a MOV without an identifier error = %v, want invalid motion", err)
	}
	if _, err := attach(bare, alice, barePrivate.ID, "IMG_1.mp4"); !errors.Is(err, photos.ErrInvalidMotion) {
		t.Fatalf("a non-MOV error = %v, want invalid motion", err)
	}
}

func TestALiveVideoCompletesOnlyPhotosTheCallerMayChangeInThatLibrary(t *testing.T) {
	s, _ := liveService(t, liveID)
	alicePrivate, shared := libraries(t, s, alice)
	bobPrivate, _ := libraries(t, s, bob)
	bobs := importStill(t, s, bob, bobPrivate.ID, "IMG_3.jpg", appleJPEG(t, liveID))
	bobsShared := importStill(t, s, bob, shared.ID, "IMG_3.jpg", appleJPEG(t, liveID))

	if _, err := attach(s, alice, alicePrivate.ID, "IMG_3.MOV"); !errors.Is(err, photos.ErrNoLiveStill) {
		t.Fatalf("another member's photo was completed from alice's library: %v", err)
	}
	if _, err := attach(s, alice, bobPrivate.ID, "IMG_3.MOV"); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("attaching into bob's private library error = %v, want not found", err)
	}
	if _, err := attach(s, alice, shared.ID, "IMG_3.MOV"); !errors.Is(err, photos.ErrNoLiveStill) {
		t.Fatalf("alice completed bob's shared photo: %v", err)
	}
	for _, library := range []string{bobPrivate.ID, shared.ID} {
		completed, err := attach(s, bob, library, "IMG_3.MOV")
		if err != nil || len(completed) != 1 || (completed[0].ID != bobs.ID && completed[0].ID != bobsShared.ID) {
			t.Fatalf("bob completing his own photo in %s = %+v, %v", library, completed, err)
		}
	}
}

func TestStillsImportedBeforeIdentifiersWereKeptAreReadByName(t *testing.T) {
	s, root := liveService(t, liveID)
	private, _ := libraries(t, s, alice)
	older := importStill(t, s, alice, private.ID, "IMG_5.HEIC", appleJPEG(t, liveID))
	renamed := importStill(t, s, alice, private.ID, "beach.jpg", appleJPEG(t, liveID))
	// What a catalog upgraded from version 8 holds: identifiers unread.
	if _, err := catalogDB(t, root).Exec("UPDATE objects SET live_id = NULL"); err != nil {
		t.Fatal(err)
	}
	completed, err := attach(s, alice, private.ID, "img_5.mov")
	if err != nil {
		t.Fatalf("AttachLiveVideo() error = %v", err)
	}
	// Both photos share the original, so reading one names both.
	if !sameIDs(completed, older.ID, renamed.ID) {
		t.Fatalf("completed %+v; want both photos of that original", completed)
	}
}

// sameIDs reports whether assets are exactly the photos with these IDs, in
// any order.
func sameIDs(assets []photos.Asset, ids ...string) bool {
	if len(assets) != len(ids) {
		return false
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	for _, asset := range assets {
		if !want[asset.ID] {
			return false
		}
		delete(want, asset.ID)
	}
	return true
}
