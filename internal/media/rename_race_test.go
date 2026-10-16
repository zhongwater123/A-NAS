package media

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
)

// Pause one scan after it has read the old file names, before it can commit.
type pausedTreeFiles struct {
	Files
	armed    atomic.Bool
	captured chan struct{}
	release  chan struct{}
}

func (f *pausedTreeFiles) Tree(ctx context.Context, actor accounts.User, spaceID, directoryID string) ([]files.TreeEntry, error) {
	entries, err := f.Files.Tree(ctx, actor, spaceID, directoryID)
	if f.armed.CompareAndSwap(true, false) {
		close(f.captured)
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return entries, err
}

func TestRenameShowKeepsIdentityWhenAnOlderScanIsInFlight(t *testing.T) {
	env := newTestEnv(t, nil)
	env.write(t, env.shared, "影视/繁花 第01集.mkv", "video")
	library := env.library(t, env.admin, env.shared, LibraryMixed, "影视")
	shows, err := env.media.Titles(env.ctx, env.admin, TitleQuery{Category: "show"})
	if err != nil || len(shows.Items) != 1 {
		t.Fatalf("initial shows = %+v, %v", shows, err)
	}
	original := shows.Items[0]
	if err := env.media.SetFavorite(env.ctx, env.admin, original.ID, true); err != nil {
		t.Fatal(err)
	}
	paused := &pausedTreeFiles{Files: env.files, captured: make(chan struct{}), release: make(chan struct{})}
	paused.armed.Store(true)
	env.media.files = paused
	ctx, cancel := context.WithTimeout(env.ctx, 5*time.Second)
	defer cancel()
	scanDone := make(chan error, 1)
	go func() { scanDone <- env.media.Scan(ctx, env.admin, library.ID) }()
	select {
	case <-paused.captured:
	case <-ctx.Done():
		t.Fatal("scan did not reach the barrier")
	}
	renameDone := make(chan error, 1)
	go func() { _, err := env.media.RenameShow(ctx, env.admin, original.ID, "Blossoms"); renameDone <- err }()
	// Before the fix, rename finishes while the old scan is paused. With the
	// fix it waits; release the scan so both operations can finish. The assertion
	// below checks the user-visible identity, not the duration of this wait.
	returned := false
	select {
	case err := <-renameDone:
		returned = true
		if err != nil {
			t.Errorf("rename: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
	}
	close(paused.release)
	if err := <-scanDone; err != nil {
		t.Fatal(err)
	}
	if !returned {
		if err := <-renameDone; err != nil {
			t.Fatal(err)
		}
	}
	env.media.background.Wait()
	if err := env.media.Scan(ctx, env.admin, library.ID); err != nil {
		t.Fatal(err)
	}
	result, err := env.media.Show(ctx, env.admin, original.ID)
	if err != nil || result.Title.Title != "Blossoms" || !result.Title.Favorite {
		t.Fatalf("rename lost show identity or favorite after old scan: %+v, %v", result.Title, err)
	}
}
