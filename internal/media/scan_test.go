package media

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"

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
