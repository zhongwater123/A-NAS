package media

import (
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// A deleted account's media goes (docs/specs/account-enable-and-delete.md):
// the libraries of its personal space with their videos, and what it
// watched, marked and collected. A shared library stays, and so does what
// everyone else watched in it.
func TestRemovingADeletedAccountForgetsItsMedia(t *testing.T) {
	env := newTestEnv(t, nil)
	alicePrivate := env.private[env.alice.ID]
	env.write(t, alicePrivate, "影视/家庭/VID_20240501_123456.mp4", "video")
	env.write(t, env.shared, "影视/家庭/VID_20080808_200000.mp4", "video")
	env.library(t, env.alice, alicePrivate, LibraryMixed, "影视")
	env.library(t, env.admin, env.shared, LibraryMixed, "影视")
	var shared string
	if err := env.media.store.db.QueryRowContext(env.ctx, `SELECT v.id FROM videos v JOIN libraries l ON l.id = v.library_id
WHERE l.space_id = ?`, env.shared.ID).Scan(&shared); err != nil {
		t.Fatalf("the shared video: %v", err)
	}
	for _, user := range []accounts.User{env.alice, env.admin} {
		if _, err := env.media.SaveProgress(env.ctx, user, shared, 600, 0); err != nil {
			t.Fatalf("SaveProgress() error = %v", err)
		}
		if err := env.media.SetFavorite(env.ctx, user, shared, true); err != nil {
			t.Fatalf("SetFavorite() error = %v", err)
		}
	}
	if _, err := env.media.CreateCollection(env.ctx, env.alice, "周末", []string{shared}); err != nil {
		t.Fatalf("CreateCollection() error = %v", err)
	}

	if err := env.media.RemoveAccount(env.ctx, env.alice.ID, alicePrivate.ID); err != nil {
		t.Fatalf("RemoveAccount() error = %v", err)
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := env.media.store.db.QueryRowContext(env.ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for what, got := range map[string]int{
		"alice's libraries":   count("SELECT COUNT(*) FROM libraries WHERE space_id = ?", alicePrivate.ID),
		"alice's progress":    count("SELECT COUNT(*) FROM progress WHERE user_id = ?", env.alice.ID),
		"alice's favorites":   count("SELECT COUNT(*) FROM favorites WHERE user_id = ?", env.alice.ID),
		"alice's collections": count("SELECT COUNT(*) FROM collections WHERE owner_user_id = ?", env.alice.ID),
	} {
		if got != 0 {
			t.Errorf("%s: %d left", what, got)
		}
	}
	if videos := count("SELECT COUNT(*) FROM videos"); videos != 1 {
		t.Errorf("videos = %d, want the shared one", videos)
	}
	if kept := count("SELECT COUNT(*) FROM progress WHERE user_id = ?", env.admin.ID) +
		count("SELECT COUNT(*) FROM favorites WHERE user_id = ?", env.admin.ID); kept != 2 {
		t.Errorf("the administrator's progress and favorite: %d left, want 2", kept)
	}
	if err := env.media.RemoveAccount(env.ctx, env.alice.ID, alicePrivate.ID); err != nil {
		t.Fatalf("RemoveAccount() again error = %v", err)
	}
}
