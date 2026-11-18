package photos_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/photos"
)

// granting is a member's grant Principal, as the photo service builds it
// for the montage center (photosapi.NewGrantPrincipal).
func granting(p photos.Principal) photos.Principal {
	return photos.Principal{UserID: p.UserID, Username: p.Username, Grant: true}
}

func TestAProjectGrantReachesTheOwnersVideosAndNothingElse(t *testing.T) {
	ctx := context.Background()
	s, _, _ := videoService(t, phoneVideo())
	alice, bob := namedAlice, namedBob
	alicePrivate, shared := libraries(t, s, alice)
	video := importPhoto(t, s, alice, alicePrivate.ID, "", "beach.mp4", mp4Bytes(1))
	second := importPhoto(t, s, alice, alicePrivate.ID, "", "sunset.mp4", mp4Bytes(2))
	// Bob's video in the shared library: Alice sees it, so she may hand it over.
	bobsShared := importPhoto(t, s, bob, shared.ID, "", "party.mp4", mp4Bytes(3))

	grant, err := s.RegisterGrant(ctx, granting(alice), "project:1", []string{video.ID, bobsShared.ID})
	if err != nil || grant.ID == "" {
		t.Fatalf("RegisterGrant() = %+v, %v", grant, err)
	}
	// Registering the project again adds to the same grant.
	again, err := s.RegisterGrant(ctx, granting(alice), "project:1", []string{second.ID, video.ID})
	if err != nil || again.ID != grant.ID {
		t.Fatalf("second RegisterGrant() = %+v, %v; want grant %s", again, err, grant.ID)
	}
	if all, err := s.Grants(ctx, "", 10); err != nil || len(all) != 1 || all[0].ProjectID != "project:1" || all[0].OwnerUserID != alice.UserID {
		t.Fatalf("Grants() = %+v, %v", all, err)
	}

	looked, err := s.LookupGrant(ctx, granting(alice), grant.ID, []string{video.ID, bobsShared.ID, "asset:none"})
	if err != nil || len(looked) != 3 {
		t.Fatalf("LookupGrant() = %+v, %v", looked, err)
	}
	if looked[0].Status != photos.GrantAvailable || looked[0].ObjectID == "" || looked[0].Duration != 42.5 ||
		looked[1].Status != photos.GrantAvailable || looked[2].Status != photos.GrantMissing {
		t.Fatalf("LookupGrant() = %+v", looked)
	}
	file, objectID, err := s.OpenGrant(ctx, granting(alice), grant.ID, video.ID)
	if err != nil || objectID != looked[0].ObjectID {
		t.Fatalf("OpenGrant() = %v, %v", objectID, err)
	}
	content, _ := io.ReadAll(file)
	_ = file.Close()
	if !bytes.Equal(content, mp4Bytes(1)) {
		t.Fatal("OpenGrant() opened another original")
	}

	// In the trash, the video waits; restored, it is back.
	if _, err := s.Trash(ctx, alice, video.ID); err != nil {
		t.Fatal(err)
	}
	if looked, _ := s.LookupGrant(ctx, granting(alice), grant.ID, []string{video.ID}); looked[0].Status != photos.GrantTrashed {
		t.Fatalf("trashed video = %+v", looked)
	}
	if _, _, err := s.OpenGrant(ctx, granting(alice), grant.ID, video.ID); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("OpenGrant(trashed) = %v", err)
	}
	if _, err := s.Restore(ctx, alice, video.ID); err != nil {
		t.Fatal(err)
	}
	// Taken out of the grant, the montage center can no longer reach it.
	if err := s.RemoveFromGrant(ctx, granting(alice), grant.ID, []string{second.ID}); err != nil {
		t.Fatal(err)
	}
	if looked, _ := s.LookupGrant(ctx, granting(alice), grant.ID, []string{second.ID}); looked[0].Status != photos.GrantMissing {
		t.Fatalf("removed video = %+v", looked)
	}
	// Bob's video purged: missing.
	if _, err := s.Trash(ctx, bob, bobsShared.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Purge(ctx, bob, bobsShared.ID); err != nil {
		t.Fatal(err)
	}
	if looked, _ := s.LookupGrant(ctx, granting(alice), grant.ID, []string{bobsShared.ID}); looked[0].Status != photos.GrantMissing {
		t.Fatalf("purged video = %+v", looked)
	}

	// Another member cannot use the grant, nor can the member's own session.
	for _, p := range []photos.Principal{granting(bob), alice} {
		if _, err := s.LookupGrant(ctx, p, grant.ID, []string{video.ID}); err == nil {
			t.Fatalf("LookupGrant(%+v) used Alice's grant", p)
		}
	}

	// Revoked, the grant is gone: revoking again changes nothing.
	for range 2 {
		if err := s.RevokeGrant(ctx, grant.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.OpenGrant(ctx, granting(alice), grant.ID, video.ID); !errors.Is(err, photos.ErrGrantNotFound) {
		t.Fatalf("OpenGrant(revoked) = %v", err)
	}
	if _, err := s.RegisterGrant(ctx, granting(alice), "project:1", []string{video.ID}); !errors.Is(err, photos.ErrGrantNotFound) {
		t.Fatalf("RegisterGrant(revoked project) = %v", err)
	}
	if all, err := s.Grants(ctx, "", 10); err != nil || len(all) != 0 {
		t.Fatalf("Grants() after revoking = %+v, %v", all, err)
	}
}

// A revoked grant keeps no tie between the member's videos and a project
// that is gone: only its marked row stays.
func TestRevokingAGrantLetsGoOfItsAssets(t *testing.T) {
	ctx := context.Background()
	s, _, root := videoService(t, phoneVideo())
	alicePrivate, _ := libraries(t, s, namedAlice)
	video := importPhoto(t, s, namedAlice, alicePrivate.ID, "", "beach.mp4", mp4Bytes(1))
	grant, err := s.RegisterGrant(ctx, granting(namedAlice), "project:1", []string{video.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeGrant(ctx, grant.ID); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(root, "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var assets, revoked int
	if err := db.QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM montage_grant_assets), (SELECT COUNT(*) FROM montage_grants WHERE revoked_at IS NOT NULL)").
		Scan(&assets, &revoked); err != nil || assets != 0 || revoked != 1 {
		t.Fatalf("after revoking: %d asset rows, %d revoked grants, %v; want 0 and 1", assets, revoked, err)
	}
}

// The montage center pages through the grants, in ID order, and a revoked
// grant is not among them.
func TestGrantsArePagedAndLeaveRevokedOnesOut(t *testing.T) {
	ctx := context.Background()
	s, _, _ := videoService(t, phoneVideo())
	alicePrivate, _ := libraries(t, s, namedAlice)
	video := importPhoto(t, s, namedAlice, alicePrivate.ID, "", "beach.mp4", mp4Bytes(1))
	registered := map[string]bool{}
	var revoked string
	for i := range 5 {
		grant, err := s.RegisterGrant(ctx, granting(namedAlice), "project:"+string(rune('a'+i)), []string{video.ID})
		if err != nil {
			t.Fatal(err)
		}
		registered[grant.ID] = true
		revoked = grant.ID
	}
	if err := s.RevokeGrant(ctx, revoked); err != nil {
		t.Fatal(err)
	}
	delete(registered, revoked)
	var listed []string
	after := ""
	for range 10 {
		page, err := s.Grants(ctx, after, 2)
		if err != nil || len(page) > 2 {
			t.Fatalf("Grants(%q, 2) = %+v, %v", after, page, err)
		}
		if len(page) == 0 {
			break
		}
		for _, grant := range page {
			listed = append(listed, grant.ID)
		}
		after = page[len(page)-1].ID
	}
	if len(listed) != len(registered) || !slices.IsSorted(listed) || slices.ContainsFunc(listed, func(id string) bool { return !registered[id] }) {
		t.Fatalf("paged grants = %v; want the %d not revoked, in ID order", listed, len(registered))
	}
}

func TestAGrantChangesNothingAndImportsOnlyIntoTheOwnersPrivateLibrary(t *testing.T) {
	ctx := context.Background()
	s, _, _ := videoService(t, phoneVideo())
	alicePrivate, shared := libraries(t, s, namedAlice)
	video := importPhoto(t, s, namedAlice, alicePrivate.ID, "", "beach.mp4", mp4Bytes(1))
	bobsShared := importPhoto(t, s, namedBob, shared.ID, "", "party.mp4", mp4Bytes(3))
	p := granting(namedAlice)
	if _, err := s.Trash(ctx, p, video.ID); err == nil {
		t.Fatal("a grant put a video in the trash")
	}
	if _, err := s.Update(ctx, p, video.ID, photos.AssetUpdate{Name: new("x.mp4")}); err == nil {
		t.Fatal("a grant renamed a video")
	}
	if err := importInto(ctx, s, p, shared.ID, ""); err == nil {
		t.Fatal("a grant added to the shared library")
	}
	// Adding is more than importing: none of it, even in the owner's own
	// library.
	if _, err := s.CreateAlbum(ctx, p, alicePrivate.ID, "Films"); !errors.Is(err, photos.ErrForbidden) {
		t.Fatalf("a grant created an album: %v", err)
	}
	if _, err := s.CreateDirectory(ctx, p, alicePrivate.ID, "", "Films"); !errors.Is(err, photos.ErrForbidden) {
		t.Fatalf("a grant created a directory: %v", err)
	}
	if _, err := s.Copy(ctx, p, bobsShared.ID, alicePrivate.ID, ""); !errors.Is(err, photos.ErrForbidden) {
		t.Fatalf("a grant copied a video: %v", err)
	}
	if _, err := s.Import(ctx, p, photos.ImportRequest{LibraryID: alicePrivate.ID, Name: "film.mp4", Content: bytes.NewReader(mp4Bytes(9))}); err != nil {
		t.Fatalf("a grant could not import into the owner's private library: %v", err)
	}
}

func TestAGrantNamesAtMostFiveHundredAssets(t *testing.T) {
	s, _, _ := videoService(t, phoneVideo())
	ids := make([]string, photos.MaxGrantAssets+1)
	for i := range ids {
		ids[i] = "asset:" + string(rune('a'+i%26))
	}
	if _, err := s.RegisterGrant(context.Background(), granting(namedAlice), "project:1", ids); !errors.Is(err, photos.ErrTooManyIDs) {
		t.Fatalf("RegisterGrant(501) = %v", err)
	}
	if _, err := s.RegisterGrant(context.Background(), granting(namedAlice), "project:1", nil); !errors.Is(err, photos.ErrGrantAssets) {
		t.Fatalf("RegisterGrant(none) = %v", err)
	}
}

func TestRemovingAnAccountRemovesItsGrants(t *testing.T) {
	ctx := context.Background()
	s, _, _ := videoService(t, phoneVideo())
	_, shared := libraries(t, s, namedAlice)
	libraries(t, s, namedBob)
	video := importPhoto(t, s, namedBob, shared.ID, "", "party.mp4", mp4Bytes(3))
	if _, err := s.RegisterGrant(ctx, granting(namedAlice), "project:1", []string{video.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RemoveAccount(ctx, photos.AccountRemoval{UserID: namedAlice.UserID}); err != nil {
		t.Fatal(err)
	}
	if all, err := s.Grants(ctx, "", 10); err != nil || len(all) != 0 {
		t.Fatalf("Grants() after removing the owner = %+v, %v", all, err)
	}
}

// TestGrantRegistrationIsIndistinguishableForEverythingOutOfReach: a
// registration naming another member's private video, one that does not
// exist, one in the trash or a photo fails alike, and an administrator in
// viewing mode cannot grant a member's private video.
func TestGrantRegistrationIsIndistinguishableForEverythingOutOfReach(t *testing.T) {
	ctx := context.Background()
	s, _, _ := videoService(t, phoneVideo())
	alicePrivate, _ := libraries(t, s, namedAlice)
	bobPrivate, _ := libraries(t, s, namedBob)
	aliceVideo := importPhoto(t, s, namedAlice, alicePrivate.ID, "", "secret.mp4", mp4Bytes(1))
	bobTrashed := importPhoto(t, s, namedBob, bobPrivate.ID, "", "old.mp4", mp4Bytes(2))
	if _, err := s.Trash(ctx, namedBob, bobTrashed.ID); err != nil {
		t.Fatal(err)
	}
	bobPhoto := importPhoto(t, s, namedBob, bobPrivate.ID, "", "photo.png", encodePNG(5))
	var answers []string
	for _, id := range []string{aliceVideo.ID, "asset:none", bobTrashed.ID, bobPhoto.ID} {
		_, err := s.RegisterGrant(ctx, granting(namedBob), "project:b", []string{id})
		answers = append(answers, errorString(err))
	}
	if want := photos.ErrGrantAssets.Error(); slices.ContainsFunc(answers, func(answer string) bool { return answer != want }) {
		t.Fatalf("registration answers = %q; want %q for all four", answers, want)
	}
	// An administrator viewing Alice's library, even marked as such, grants
	// nothing of it.
	admin := photos.Principal{UserID: "user:admin", Username: "admin", Admin: true, Grant: true,
		Viewing: []photos.ViewingGrant{{GrantID: "viewing:1", OwnerUserID: namedAlice.UserID, ExpiresAt: time.Now().Add(time.Hour)}}}
	if _, err := s.RegisterGrant(ctx, admin, "project:a", []string{aliceVideo.ID}); !errors.Is(err, photos.ErrGrantAssets) {
		t.Fatalf("an administrator in viewing mode granted a member's video: %v", err)
	}
	if all, _ := s.Grants(ctx, "", 10); len(all) != 0 {
		t.Fatalf("refused registrations left grants: %+v", all)
	}
}

func errorString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

func TestAFilmIsSavedOncePerVersionIntoTheOwnersLibrary(t *testing.T) {
	ctx := context.Background()
	s, _, _ := videoService(t, phoneVideo())
	private, _ := libraries(t, s, namedAlice)
	video := importPhoto(t, s, namedAlice, private.ID, "", "beach.mp4", mp4Bytes(1))
	grant, err := s.RegisterGrant(ctx, granting(namedAlice), "project:1", []string{video.ID})
	if err != nil {
		t.Fatal(err)
	}
	film, err := s.SaveFilm(ctx, granting(namedAlice), grant.ID, "job:1/render:abc", "海边 v1.mp4", bytes.NewReader(mp4Bytes(7)))
	if err != nil {
		t.Fatal(err)
	}
	// A save tried again after a cut finds the first.
	again, err := s.SaveFilm(ctx, granting(namedAlice), grant.ID, "job:1/render:abc", "海边 v1.mp4", bytes.NewReader(mp4Bytes(8)))
	if err != nil || again != film {
		t.Fatalf("SaveFilm() again = %s, %v; want %s", again, err, film)
	}
	page, err := s.Timeline(ctx, namedAlice, private.ID, "", 10)
	if err != nil || len(page.Assets) != 2 {
		t.Fatalf("timeline = %d assets, %v; want the video and one film", len(page.Assets), err)
	}
	if looked, _ := s.LookupGrant(ctx, granting(namedAlice), grant.ID, []string{film}); looked[0].Status != photos.GrantAvailable {
		t.Fatalf("the film in the grant = %+v", looked[0])
	}
	// Bob cannot save into Alice's grant.
	if _, err := s.SaveFilm(ctx, granting(namedBob), grant.ID, "job:2", "x.mp4", bytes.NewReader(mp4Bytes(9))); err == nil {
		t.Fatal("Bob saved a film into Alice's grant")
	}
	// The owner deletes the film: the next save of that version makes a new one.
	if _, err := s.Trash(ctx, namedAlice, film); err != nil {
		t.Fatal(err)
	}
	if err := s.Purge(ctx, namedAlice, film); err != nil {
		t.Fatal(err)
	}
	if saved, err := s.SaveFilm(ctx, granting(namedAlice), grant.ID, "job:1/render:abc", "海边 v1.mp4", bytes.NewReader(mp4Bytes(7))); err != nil || saved == film {
		t.Fatalf("saving a deleted film again = %s, %v", saved, err)
	}
}

// The import checks the live grant in the transaction that publishes the film.
func TestRevokingAGrantWhileStagingAFilmPreventsPublication(t *testing.T) {
	ctx := t.Context()
	s, _, _ := videoService(t, phoneVideo())
	private, _ := libraries(t, s, namedAlice)
	video := importPhoto(t, s, namedAlice, private.ID, "", "beach.mp4", mp4Bytes(1))
	grant, err := s.RegisterGrant(ctx, granting(namedAlice), "project:1", []string{video.ID})
	if err != nil {
		t.Fatal(err)
	}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	saved := make(chan error, 1)
	go func() {
		_, err := s.SaveFilm(ctx, granting(namedAlice), grant.ID, "job:1/render", "film.mp4", reader)
		saved <- err
	}()
	film := mp4Bytes(7)
	if _, err := writer.Write(film[:1]); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeGrant(ctx, grant.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(film[1:]); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	if err := <-saved; !errors.Is(err, photos.ErrGrantNotFound) {
		t.Fatalf("save after revocation: %v", err)
	}
	page, err := s.Timeline(ctx, namedAlice, private.ID, "", 10)
	if err != nil || len(page.Assets) != 1 {
		t.Fatalf("timeline after revocation: %d assets, %v", len(page.Assets), err)
	}
}
