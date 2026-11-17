package music_test

import (
	"path/filepath"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// A deleted account's music goes (docs/specs/account-enable-and-delete.md):
// what it marked, played and listed, and the tracks of its personal space.
// The shared tracks stay, with what everyone else marked and played.
func TestRemovingADeletedAccountForgetsItsMusic(t *testing.T) {
	f := newFixture(t)
	kid := filepath.Join(filepath.Dir(f.private), f.member.Username)
	write(t, filepath.Join(kid, "mine.mp3"), mp3File(5, id3Frame("TIT2", text("Mine"))))
	write(t, filepath.Join(f.shared, "ours.mp3"), mp3File(5, id3Frame("TIT2", text("Ours"))))
	f.scan(t, f.admin)
	f.scan(t, f.member)
	tracks := f.tracks(t, f.member)
	mine, ours := tracks["Mine"], tracks["Ours"]
	if mine.ID == "" || ours.ID == "" {
		t.Fatalf("the member's tracks = %+v", tracks)
	}
	for _, actor := range []accounts.User{f.admin, f.member} {
		if err := f.library.SetFavorite(f.ctx, actor, ours.ID, true); err != nil {
			t.Fatal(err)
		}
		if err := f.library.RecordPlay(f.ctx, actor, ours.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.library.CreatePlaylist(f.ctx, f.member, "Mine", []string{mine.ID, ours.ID}); err != nil {
		t.Fatal(err)
	}

	if err := f.library.RemoveAccount(f.ctx, f.member.ID, mine.SpaceID); err != nil {
		t.Fatalf("RemoveAccount() error = %v", err)
	}
	if favorites, err := f.library.Favorites(f.ctx, f.member); err != nil || len(favorites) != 0 {
		t.Errorf("the member's favorites = %+v, %v", favorites, err)
	}
	if recent, err := f.library.Recent(f.ctx, f.member); err != nil || len(recent) != 0 {
		t.Errorf("the member's plays = %+v, %v", recent, err)
	}
	if lists, err := f.library.Playlists(f.ctx, f.member); err != nil || len(lists) != 0 {
		t.Errorf("the member's playlists = %+v, %v", lists, err)
	}
	if left := f.tracks(t, f.member); len(left) != 1 || left["Ours"].ID != ours.ID {
		t.Errorf("tracks left = %+v, want only the shared one", left)
	}
	if favorites, err := f.library.Favorites(f.ctx, f.admin); err != nil || len(favorites) != 1 {
		t.Errorf("the administrator's favorites = %+v, %v", favorites, err)
	}
	if recent, err := f.library.Recent(f.ctx, f.admin); err != nil || len(recent) != 1 {
		t.Errorf("the administrator's plays = %+v, %v", recent, err)
	}
	if err := f.library.RemoveAccount(f.ctx, f.member.ID, mine.SpaceID); err != nil {
		t.Fatalf("RemoveAccount() again error = %v", err)
	}
}
