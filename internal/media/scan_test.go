package media

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/sqlitetest"
)

// A read of a library's folders that fails part way, as a failing disk
// would, fails the scan: the videos in the folders it did not read are not
// gone, and neither is what was watched of them (#269).
func TestScanStopsWhenReadingTheFoldersFails(t *testing.T) {
	for _, after := range []int{0, 1} {
		t.Run(fmt.Sprintf("after %d rows", after), func(t *testing.T) {
			env := newTestEnv(t, nil)
			env.write(t, env.shared, "电影/Inception (2010).mkv", "video")
			env.write(t, env.shared, "纪录片/Planet Earth (2006).mkv", "video")
			library := env.library(t, env.admin, env.shared, LibraryMovies, "电影", "纪录片")
			movies, _ := env.media.Titles(env.ctx, env.alice, TitleQuery{Category: "movie"})
			planet, ok := titlesOf(movies)["Planet Earth"]
			if movies.Total != 2 || !ok {
				t.Fatalf("films = %+v, want both", movies.Items)
			}
			if _, err := env.media.SaveProgress(env.ctx, env.alice, planet.ID, 600, 0); err != nil {
				t.Fatal(err)
			}

			store, err := openSQLite(sqlitetest.Register("SELECT entry_id FROM library_folders", after), env.catalog)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			cut, err := NewService(store, Options{Files: env.files, Spaces: env.accounts, CacheDir: filepath.Join(t.TempDir(), "cache"), Now: env.clock.Now})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cut.Close)
			if err := cut.Scan(env.ctx, env.admin, library.ID); !errors.Is(err, sqlitetest.ErrCut) {
				t.Fatalf("Scan() error = %v, want ErrCut", err)
			}

			movies, _ = env.media.Titles(env.ctx, env.alice, TitleQuery{Category: "movie"})
			kept, ok := titlesOf(movies)["Planet Earth"]
			if movies.Total != 2 || !ok || kept.Progress == nil || kept.Progress.Position != 600 {
				t.Fatalf("films after the failed scan = %+v, want both and the progress", movies.Items)
			}
		})
	}
}

// A frame or subtitle written by work already under way when its video went
// (its library or account deleted) is not left in the cache: the next prune
// removes it. Those of videos still there, and a file being written, stay.
func TestPruneCacheDropsFramesAndSubtitlesOfVideosThatAreGone(t *testing.T) {
	env := newTestEnv(t, nil)
	env.write(t, env.shared, "影视/家庭/VID_20080808_200000.mp4", "video")
	env.library(t, env.admin, env.shared, LibraryMixed, "影视")
	var kept string
	if err := env.media.store.db.QueryRowContext(env.ctx, "SELECT id FROM videos").Scan(&kept); err != nil {
		t.Fatal(err)
	}
	subtitle := func(id string) string {
		return filepath.Join(env.media.cacheDir, "subtitles", cacheName(id)+"-s0-20240501123456.vtt")
	}
	gone := "video:GONE"
	stay := []string{env.media.framePath(kept), subtitle(kept), filepath.Join(env.media.cacheDir, "frames", ".tmp-123")}
	crashed := filepath.Join(env.media.cacheDir, "subtitles", ".tmp-456")
	removed := []string{env.media.framePath(gone), subtitle(gone), crashed}
	for _, name := range append(stay, removed...) {
		if err := os.WriteFile(name, []byte("cached"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A temporary file this old was left by a crash.
	if old := time.Now().Add(-2 * time.Hour); os.Chtimes(crashed, old, old) != nil {
		t.Fatal("could not age the temporary file")
	}
	env.media.pruneCache(env.ctx)
	for _, name := range stay {
		if _, err := os.Stat(name); err != nil {
			t.Errorf("%s was pruned: %v", filepath.Base(name), err)
		}
	}
	for _, name := range removed {
		if _, err := os.Stat(name); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s of a video that is gone is still cached (%v)", filepath.Base(name), err)
		}
	}
}

// A read of which artwork the titles use that fails part way leaves the
// resized artwork alone: what was not read could still be in use (#269).
func TestPruneCacheKeepsArtworkWhenReadingTheTitlesFails(t *testing.T) {
	env := newTestEnv(t, nil)
	artwork := filepath.Join(env.media.cacheDir, "artwork")
	if err := os.MkdirAll(artwork, 0o755); err != nil {
		t.Fatal(err)
	}
	resized := filepath.Join(artwork, "entry-unread--300-1.jpg")
	if err := os.WriteFile(resized, []byte("jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := openSQLite(sqlitetest.Register("SELECT poster_entry FROM videos", 0), env.catalog)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cut, err := NewService(store, Options{Files: env.files, Spaces: env.accounts, CacheDir: env.media.cacheDir, Now: env.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cut.Close)
	cut.pruneCache(env.ctx)
	if _, err := os.Stat(resized); err != nil {
		t.Fatalf("artwork after a failed read: %v", err)
	}
	// Read in full, the titles do not use it.
	env.media.pruneCache(env.ctx)
	if _, err := os.Stat(resized); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unused artwork after a full read: %v", err)
	}
}
