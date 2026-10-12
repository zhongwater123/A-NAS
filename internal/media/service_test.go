package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
)

type testEnv struct {
	ctx      context.Context
	accounts *accounts.Service
	files    *files.Service
	media    *Service
	volume   string
	admin    accounts.User
	alice    accounts.User
	bob      accounts.User
	shared   accounts.Space
	private  map[string]accounts.Space
}

type acceptingCredentials struct{}

func (acceptingCredentials) SetCredential(context.Context, accounts.CredentialRequest) error {
	return nil
}
func (acceptingCredentials) DisableCredential(context.Context, string) error { return nil }

// fakeProcessor reads the MediaInfo a test wrote into the "video" file.
type fakeProcessor struct {
	streams []StreamOptions
}

func (p *fakeProcessor) Probe(_ context.Context, file *os.File) (MediaInfo, error) {
	data, _ := io.ReadAll(file)
	var info MediaInfo
	if err := json.Unmarshal(data, &info); err != nil || info.Container == "" {
		return MediaInfo{}, ErrUnreadable
	}
	return info, nil
}

func (p *fakeProcessor) Frame(context.Context, *os.File, float64, int) ([]byte, error) {
	return []byte("jpeg"), nil
}

func (p *fakeProcessor) Subtitle(_ context.Context, file *os.File, source SubtitleSource) ([]byte, error) {
	data, _ := io.ReadAll(file)
	return []byte("WEBVTT\n\n" + source.Charset + ":" + string(data)), nil
}

func (p *fakeProcessor) Stream(_ context.Context, _ *os.File, options StreamOptions) (io.ReadCloser, error) {
	p.streams = append(p.streams, options)
	return io.NopCloser(strings.NewReader("fmp4")), nil
}

func newTestEnv(t *testing.T, processor Processor) *testEnv {
	t.Helper()
	ctx := context.Background()
	accountStore, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatalf("open account store: %v", err)
	}
	t.Cleanup(func() { _ = accountStore.Close() })
	accountService := accounts.NewService(accountStore, acceptingCredentials{}, accounts.Options{})
	admin, err := accountService.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatalf("setup administrator: %v", err)
	}
	alice, err := accountService.CreateMember(ctx, admin, "alice", "alice password for testing")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := accountService.CreateMember(ctx, admin, "bob", "bob password for testing")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	catalog, err := files.OpenSQLite(filepath.Join(t.TempDir(), "files.db"))
	if err != nil {
		t.Fatalf("open file catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	volume := filepath.Join(t.TempDir(), "volume")
	fileService := files.NewService(catalog, volume, accountService, files.Options{DisableCapacityReserve: true, AllowUnverifiedVolume: true})
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatalf("open media store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	clock := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	service, err := NewService(store, Options{
		Files: fileService, Spaces: accountService, Processor: processor, CacheDir: filepath.Join(t.TempDir(), "cache"),
		Now: func() time.Time { clock = clock.Add(time.Second); return clock },
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	t.Cleanup(service.Close)
	env := &testEnv{ctx: ctx, accounts: accountService, files: fileService, media: service, volume: volume, admin: admin, alice: alice, bob: bob, private: map[string]accounts.Space{}}
	for _, user := range []accounts.User{admin, alice, bob} {
		spaces, err := accountService.ListSpaces(ctx, user)
		if err != nil {
			t.Fatalf("list spaces: %v", err)
		}
		for _, space := range spaces {
			if space.Kind == accounts.SpaceKindShared {
				env.shared = space
			} else if space.OwnerUserID == user.ID {
				env.private[user.ID] = space
			}
		}
	}
	return env
}

func (e *testEnv) spaceRoot(space accounts.Space) string {
	if space.Kind == accounts.SpaceKindShared {
		return filepath.Join(e.volume, "spaces", "shared")
	}
	return filepath.Join(e.volume, "spaces", "private", space.Name)
}

// write creates a file below a space, as SMB would.
func (e *testEnv) write(t *testing.T, space accounts.Space, relative string, contents string) {
	t.Helper()
	target := filepath.Join(e.spaceRoot(space), filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func video(container, videoCodec, audioCodec string, height int, duration float64) string {
	info := MediaInfo{Container: container, Duration: duration, Video: &VideoStream{Codec: videoCodec, Width: height * 16 / 9, Height: height, PixelFormat: "yuv420p"}}
	if audioCodec != "" {
		info.Audio = []AudioStream{{Index: 1, Codec: audioCodec, Channels: 6, Language: "chi", Default: true}}
	}
	encoded, _ := json.Marshal(info)
	return string(encoded)
}

// folder returns the entry ID of a directory in a space.
func (e *testEnv) folder(t *testing.T, actor accounts.User, space accounts.Space, relative string) string {
	t.Helper()
	tree, err := e.files.Tree(e.ctx, actor, space.ID, "")
	if err != nil {
		t.Fatalf("Tree() error = %v", err)
	}
	for _, entry := range tree {
		if entry.Path == relative && entry.Kind == files.EntryKindDirectory {
			return entry.ID
		}
	}
	t.Fatalf("folder %s not found", relative)
	return ""
}

func (e *testEnv) library(t *testing.T, actor accounts.User, space accounts.Space, kind LibraryKind, folders ...string) Library {
	t.Helper()
	var ids []string
	for _, folder := range folders {
		ids = append(ids, e.folder(t, actor, space, folder))
	}
	library, err := e.media.CreateLibrary(e.ctx, actor, LibraryInput{Name: "影视", Kind: kind, SpaceID: space.ID, FolderIDs: ids})
	if err != nil {
		t.Fatalf("CreateLibrary() error = %v", err)
	}
	if err := e.media.Scan(e.ctx, actor, library.ID); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	return library
}

func titlesOf(page TitleResults) map[string]Title {
	byTitle := map[string]Title{}
	for _, title := range page.Items {
		byTitle[title.Title] = title
	}
	return byTitle
}

func TestMixedLibraryRecognisesFilmsShowsAndOtherVideos(t *testing.T) {
	env := newTestEnv(t, nil)
	shared := env.shared
	env.write(t, shared, "影视/电影/流浪地球2 (2023)/流浪地球2.2023.2160p.WEB-DL.mkv", "video")
	env.write(t, shared, "影视/电影/流浪地球2 (2023)/poster.jpg", "poster")
	env.write(t, shared, "影视/电影/流浪地球2 (2023)/movie.nfo", `<movie><title>流浪地球2</title><plot>太阳即将毁灭。</plot><genre>科幻</genre><set><name>流浪地球系列</name></set></movie>`)
	env.write(t, shared, "影视/电影/流浪地球 (2019)/The.Wandering.Earth.2019.1080p.mkv", "video")
	env.write(t, shared, "影视/电影/流浪地球 (2019)/The.Wandering.Earth.2019.1080p.chs.srt", "1\n00:00:01,000 --> 00:00:02,000\n你好\n")
	env.write(t, shared, "影视/电影/流浪地球 (2019)/The.Wandering.Earth.2019.1080p.en.srt", "1\n")
	env.write(t, shared, "影视/电影/流浪地球 (2019)/movie.nfo", `<movie><title>流浪地球</title><set>流浪地球系列</set><genre>科幻</genre></movie>`)
	env.write(t, shared, "影视/剧集/漫长的季节/Season 1/S01E02.mp4", "video")
	env.write(t, shared, "影视/剧集/漫长的季节/Season 1/S01E01.mp4", "video")
	env.write(t, shared, "影视/剧集/漫长的季节/poster.jpg", "poster")
	env.write(t, shared, "影视/剧集/漫长的季节/tvshow.nfo", `<tvshow><title>漫长的季节</title><year>2023</year><plot>东北小城</plot></tvshow>`)
	env.write(t, shared, "影视/剧集/繁花/[字幕组] 繁花 第06集.mkv", "video")
	env.write(t, shared, "影视/剧集/繁花/[字幕组] 繁花 第05集.mkv", "video")
	env.write(t, shared, "影视/家庭/VID_20240501_123456.mp4", "video")
	env.write(t, shared, "影视/家庭/.hidden/skip.mp4", "video")
	env.write(t, shared, "影视/家庭/notes.txt", "not a video")
	env.library(t, env.admin, shared, LibraryMixed, "影视")

	movies, err := env.media.Titles(env.ctx, env.alice, TitleQuery{Category: "movie", Sort: "name"})
	if err != nil {
		t.Fatalf("Titles(movie) error = %v", err)
	}
	if movies.Total != 2 {
		t.Fatalf("movies = %+v", movies.Items)
	}
	byTitle := titlesOf(movies)
	sequel, original := byTitle["流浪地球2"], byTitle["流浪地球"]
	if sequel.Year != 2023 || !sequel.Artwork.Poster || original.Year != 2019 || original.Artwork.Poster {
		t.Fatalf("films = %+v / %+v", sequel, original)
	}
	shows, err := env.media.Titles(env.ctx, env.alice, TitleQuery{Category: "show"})
	if err != nil {
		t.Fatalf("Titles(show) error = %v", err)
	}
	byShow := titlesOf(shows)
	season, ok := byShow["漫长的季节"]
	if !ok || season.Episodes != 2 || season.Year != 2023 || !season.Artwork.Poster {
		t.Fatalf("shows = %+v", shows.Items)
	}
	blossoms, ok := byShow["繁花"]
	if !ok || blossoms.Episodes != 2 {
		t.Fatalf("shows = %+v", shows.Items)
	}
	detail, err := env.media.Show(env.ctx, env.alice, blossoms.ID)
	if err != nil {
		t.Fatalf("Show() error = %v", err)
	}
	if len(detail.SeasonList) != 1 || detail.SeasonList[0].Number != 1 || detail.SeasonList[0].Episodes[0].Episode != 5 || detail.Next == nil || detail.Next.Episode != 5 {
		t.Fatalf("show detail = %+v", detail)
	}
	others, err := env.media.Titles(env.ctx, env.alice, TitleQuery{Category: "other"})
	if err != nil || others.Total != 1 || others.Items[0].Title != "VID_20240501_123456" {
		t.Fatalf("others = %+v, %v", others, err)
	}
	search, err := env.media.Titles(env.ctx, env.alice, TitleQuery{Search: "季节"})
	if err != nil || search.Total != 1 || search.Items[0].Type != TypeShow {
		t.Fatalf("search = %+v, %v", search, err)
	}
	if len(movies.Genres) != 1 || movies.Genres[0] != "科幻" {
		t.Fatalf("genres = %v", movies.Genres)
	}

	film, err := env.media.Video(env.ctx, env.alice, original.ID)
	if err != nil {
		t.Fatalf("Video() error = %v", err)
	}
	if len(film.Subtitles) != 2 || film.Subtitles[0].Label != "简体中文" || film.Subtitles[1].Language != "en" || film.Collection != "流浪地球系列" {
		t.Fatalf("film detail = %+v", film)
	}
	if film.File.Folder != "影视/电影/流浪地球 (2019)" {
		t.Fatalf("folder = %q", film.File.Folder)
	}
	collections, err := env.media.Collections(env.ctx, env.bob)
	if err != nil || len(collections) != 1 || !collections[0].Automatic || collections[0].Count != 2 {
		t.Fatalf("collections = %+v, %v", collections, err)
	}
	set, err := env.media.CollectionDetail(env.ctx, env.bob, collections[0].ID)
	if err != nil || set.Items[0].Title != "流浪地球" {
		t.Fatalf("set = %+v, %v", set, err)
	}
}

func TestLibrariesFollowSpaceVisibilityAndDoNotOverlap(t *testing.T) {
	env := newTestEnv(t, nil)
	alicePrivate := env.private[env.alice.ID]
	env.write(t, alicePrivate, "我的视频/旅行.mp4", "video")
	env.write(t, env.shared, "电影/Inception (2010).mkv", "video")
	env.write(t, env.shared, "电影/动画/Up (2009).mkv", "video")

	mine := env.library(t, env.alice, alicePrivate, LibraryOther, "我的视频")
	shared := env.library(t, env.admin, env.shared, LibraryMovies, "电影")

	for _, check := range []struct {
		user accounts.User
		want []string
	}{{env.alice, []string{mine.ID, shared.ID}}, {env.bob, []string{shared.ID}}, {env.admin, []string{shared.ID}}} {
		libraries, err := env.media.ListLibraries(env.ctx, check.user)
		if err != nil {
			t.Fatalf("ListLibraries() error = %v", err)
		}
		var got []string
		for _, library := range libraries {
			got = append(got, library.ID)
			if library.ID == shared.ID && library.CanManage != (check.user.ID == env.admin.ID) {
				t.Fatalf("%s CanManage = %v", check.user.Username, library.CanManage)
			}
		}
		sort.Strings(got)
		sort.Strings(check.want)
		if strings.Join(got, ",") != strings.Join(check.want, ",") {
			t.Fatalf("%s sees %v, want %v", check.user.Username, got, check.want)
		}
	}
	others, _ := env.media.Titles(env.ctx, env.bob, TitleQuery{Category: "other"})
	if others.Total != 0 {
		t.Fatalf("bob sees alice's videos: %+v", others.Items)
	}
	travel, _ := env.media.Titles(env.ctx, env.alice, TitleQuery{Category: "other"})
	if travel.Total != 1 {
		t.Fatalf("alice's videos = %+v", travel.Items)
	}
	if _, err := env.media.Video(env.ctx, env.admin, travel.Items[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("admin reads alice's video: %v", err)
	}
	if _, err := env.media.Artwork(env.ctx, env.bob, travel.Items[0].ID, "thumb"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bob reads alice's artwork: %v", err)
	}

	_, err := env.media.CreateLibrary(env.ctx, env.bob, LibraryInput{Name: "共享", Kind: LibraryMovies, SpaceID: env.shared.ID, FolderIDs: []string{env.folder(t, env.bob, env.shared, "电影")}})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("member created a shared library: %v", err)
	}
	if err := env.media.DeleteLibrary(env.ctx, env.bob, shared.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member deleted a shared library: %v", err)
	}
	_, err = env.media.CreateLibrary(env.ctx, env.admin, LibraryInput{Name: "动画", Kind: LibraryMovies, SpaceID: env.shared.ID, FolderIDs: []string{env.folder(t, env.admin, env.shared, "电影/动画")}})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("overlapping library: %v", err)
	}
	_, err = env.media.CreateLibrary(env.ctx, env.alice, LibraryInput{Name: "偷看", Kind: LibraryMovies, SpaceID: env.private[env.bob.ID].ID, FolderIDs: []string{"file:missing"}})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("library in another member's space: %v", err)
	}

	if err := env.media.DeleteLibrary(env.ctx, env.admin, shared.ID); err != nil {
		t.Fatalf("DeleteLibrary() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.spaceRoot(env.shared), "电影", "Inception (2010).mkv")); err != nil {
		t.Fatalf("deleting the library touched the file: %v", err)
	}
	movies, _ := env.media.Titles(env.ctx, env.alice, TitleQuery{Category: "movie"})
	if movies.Total != 0 {
		t.Fatalf("films after delete = %+v", movies.Items)
	}
}

func TestProgressHistoryFavoritesAndCollectionsArePerUser(t *testing.T) {
	env := newTestEnv(t, &fakeProcessor{})
	env.write(t, env.shared, "剧集/漫长的季节/S01E01.mp4", video("mov,mp4,m4a,3gp,3g2,mj2", "h264", "aac", 1080, 3000))
	env.write(t, env.shared, "剧集/漫长的季节/S01E02.mp4", video("mov,mp4,m4a,3gp,3g2,mj2", "h264", "aac", 1080, 3000))
	env.write(t, env.shared, "电影/Inception (2010).mkv", video("matroska,webm", "h264", "ac3", 2160, 8880))
	env.library(t, env.admin, env.shared, LibraryMixed, "剧集", "电影")
	env.media.processPending(withSessions(t, env))

	movies, _ := env.media.Titles(env.ctx, env.alice, TitleQuery{Category: "movie"})
	if movies.Total != 1 || movies.Items[0].Duration != 8880 || movies.Items[0].Resolution != "4K" || !movies.Items[0].Artwork.Backdrop {
		t.Fatalf("film after processing = %+v", movies.Items)
	}
	film := movies.Items[0].ID
	if _, err := env.media.SaveProgress(env.ctx, env.alice, film, 1200, 0); err != nil {
		t.Fatalf("SaveProgress() error = %v", err)
	}
	shows, _ := env.media.Titles(env.ctx, env.alice, TitleQuery{Category: "show"})
	show, err := env.media.Show(env.ctx, env.alice, shows.Items[0].ID)
	if err != nil {
		t.Fatalf("Show() error = %v", err)
	}
	first, second := show.SeasonList[0].Episodes[0], show.SeasonList[0].Episodes[1]
	// Watching 95% of the first episode finishes it; the next one is up.
	if progress, err := env.media.SaveProgress(env.ctx, env.alice, first.ID, 2850, 3000); err != nil || !progress.Watched || progress.Position != 0 {
		t.Fatalf("SaveProgress(episode) = %+v, %v", progress, err)
	}
	home, err := env.media.Home(env.ctx, env.alice)
	if err != nil {
		t.Fatalf("Home() error = %v", err)
	}
	if len(home.Continue) != 2 || home.Continue[0].ID != second.ID || home.Continue[1].ID != film || len(home.Featured) == 0 {
		t.Fatalf("continue watching = %+v", home.Continue)
	}
	bobHome, _ := env.media.Home(env.ctx, env.bob)
	if len(bobHome.Continue) != 0 {
		t.Fatalf("bob continues alice's videos: %+v", bobHome.Continue)
	}

	history, _ := env.media.History(env.ctx, env.alice, 0, 0)
	if history.Total != 2 || history.Items[0].ID != first.ID || history.Items[0].ShowTitle != "漫长的季节" {
		t.Fatalf("history = %+v", history.Items)
	}
	if err := env.media.RemoveFromHistory(env.ctx, env.alice, first.ID); err != nil {
		t.Fatalf("RemoveFromHistory() error = %v", err)
	}
	if history, _ := env.media.History(env.ctx, env.alice, 0, 0); history.Total != 1 {
		t.Fatalf("history after removal = %+v", history.Items)
	}
	if detail, _ := env.media.Show(env.ctx, env.alice, show.ID); detail.Watched != 1 {
		t.Fatalf("removing from history forgot the watched mark: %+v", detail.Title)
	}

	if err := env.media.SetFavorite(env.ctx, env.alice, show.ID, true); err != nil {
		t.Fatalf("SetFavorite() error = %v", err)
	}
	if favorites, _ := env.media.Favorites(env.ctx, env.alice); favorites.Total != 1 || favorites.Items[0].Type != TypeShow || !favorites.Items[0].Favorite {
		t.Fatalf("favorites = %+v", favorites.Items)
	}
	if favorites, _ := env.media.Favorites(env.ctx, env.bob); favorites.Total != 0 {
		t.Fatalf("bob's favorites = %+v", favorites.Items)
	}

	collection, err := env.media.CreateCollection(env.ctx, env.alice, "周末片单", []string{film})
	if err != nil {
		t.Fatalf("CreateCollection() error = %v", err)
	}
	if err := env.media.AddToCollection(env.ctx, env.alice, collection.ID, []string{show.ID}); err != nil {
		t.Fatalf("AddToCollection() error = %v", err)
	}
	detail, err := env.media.CollectionDetail(env.ctx, env.alice, collection.ID)
	if err != nil || len(detail.Items) != 2 || detail.Items[0].ID != film {
		t.Fatalf("collection = %+v, %v", detail, err)
	}
	if _, err := env.media.CollectionDetail(env.ctx, env.bob, collection.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bob opened alice's collection: %v", err)
	}
	if err := env.media.RenameCollection(env.ctx, env.bob, collection.ID, "抢走"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bob renamed alice's collection: %v", err)
	}
	if err := env.media.RemoveFromCollection(env.ctx, env.alice, collection.ID, film); err != nil {
		t.Fatalf("RemoveFromCollection() error = %v", err)
	}
	if err := env.media.SetWatched(env.ctx, env.alice, show.ID, true); err != nil {
		t.Fatalf("SetWatched(show) error = %v", err)
	}
	if detail, _ := env.media.Show(env.ctx, env.alice, show.ID); detail.Watched != 2 {
		t.Fatalf("watched episodes = %d", detail.Watched)
	}
}

func withSessions(t *testing.T, env *testEnv) context.Context {
	t.Helper()
	session, err := env.accounts.Authenticate(env.ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	env.media.sessions = func(context.Context) []accounts.Session { return []accounts.Session{session} }
	return env.ctx
}

func TestRescanKeepsRenamedVideosAndDropsRemovedOnes(t *testing.T) {
	env := newTestEnv(t, nil)
	env.write(t, env.shared, "电影/Inception (2010).mkv", "video")
	env.write(t, env.shared, "电影/Up (2009).mkv", "video")
	library := env.library(t, env.admin, env.shared, LibraryMovies, "电影")
	movies, _ := env.media.Titles(env.ctx, env.alice, TitleQuery{Category: "movie", Sort: "name"})
	byTitle := titlesOf(movies)
	inception, up := byTitle["Inception"], byTitle["Up"]
	if _, err := env.media.SaveProgress(env.ctx, env.alice, inception.ID, 600, 0); err != nil {
		t.Fatal(err)
	}
	if err := env.media.SetFavorite(env.ctx, env.alice, up.ID, true); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(env.spaceRoot(env.shared), "电影")
	if err := os.Rename(filepath.Join(root, "Inception (2010).mkv"), filepath.Join(root, "Inception.2010.1080p.mkv")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "Up (2009).mkv")); err != nil {
		t.Fatal(err)
	}
	if err := env.media.Scan(env.ctx, env.admin, library.ID); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	movies, _ = env.media.Titles(env.ctx, env.alice, TitleQuery{Category: "movie"})
	if movies.Total != 1 || movies.Items[0].ID != inception.ID || movies.Items[0].Progress == nil || movies.Items[0].Progress.Position != 600 {
		t.Fatalf("films after rescan = %+v", movies.Items)
	}
	if favorites, _ := env.media.Favorites(env.ctx, env.alice); favorites.Total != 0 {
		t.Fatalf("favorite of a removed film survived: %+v", favorites.Items)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := env.media.Scan(env.ctx, env.admin, library.ID); err != nil {
		t.Fatalf("Scan() after removing the folder error = %v", err)
	}
	described, _ := env.media.GetLibrary(env.ctx, env.admin, library.ID)
	if !described.Folders[0].Missing || described.Counts.Movies != 0 {
		t.Fatalf("library after its folder went away = %+v", described)
	}
}

func TestPlaybackPicksDirectRemuxOrTranscode(t *testing.T) {
	mp4 := "mov,mp4,m4a,3gp,3g2,mj2"
	mkv := "matroska,webm"
	h264 := &VideoStream{Codec: "h264", Height: 1080, PixelFormat: "yuv420p"}
	hevc := &VideoStream{Codec: "hevc", Height: 2160, PixelFormat: "yuv420p10le"}
	aac := []AudioStream{{Codec: "aac", Default: true}}
	ac3 := []AudioStream{{Codec: "ac3", Default: true}, {Codec: "aac"}}
	cases := []struct {
		name    string
		info    MediaInfo
		file    string
		caps    Capabilities
		audio   int
		quality string
		mode    string
		check   func(StreamOptions) bool
	}{
		{name: "mp4", info: MediaInfo{Container: mp4, Video: h264, Audio: aac}, file: "a.mp4", mode: ModeDirect},
		{name: "mkv in Chromium", info: MediaInfo{Container: mkv, Video: h264, Audio: aac}, file: "a.mkv", caps: Capabilities{Matroska: true}, mode: ModeDirect},
		{name: "mkv elsewhere", info: MediaInfo{Container: mkv, Video: h264, Audio: aac}, file: "a.mkv", mode: ModeRemux,
			check: func(o StreamOptions) bool { return o.CopyVideo && o.CopyAudio }},
		{name: "AC3 audio", info: MediaInfo{Container: mkv, Video: h264, Audio: ac3}, file: "a.mkv", caps: Capabilities{Matroska: true}, mode: ModeRemux,
			check: func(o StreamOptions) bool { return o.CopyVideo && !o.CopyAudio && o.AudioTrack == 0 }},
		{name: "second audio track", info: MediaInfo{Container: mp4, Video: h264, Audio: ac3}, file: "a.mp4", caps: Capabilities{AC3: true}, audio: 1, mode: ModeRemux,
			check: func(o StreamOptions) bool { return o.AudioTrack == 1 && o.CopyAudio }},
		{name: "HEVC without support", info: MediaInfo{Container: mkv, Video: hevc, Audio: aac}, file: "a.mkv", mode: ModeTranscode,
			check: func(o StreamOptions) bool { return !o.CopyVideo && o.Height == 0 }},
		{name: "HEVC with support", info: MediaInfo{Container: mkv, Video: hevc, Audio: aac}, file: "a.mkv", caps: Capabilities{HEVC: true}, mode: ModeRemux,
			check: func(o StreamOptions) bool { return o.CopyVideo && o.HEVC }},
		{name: "AVI", info: MediaInfo{Container: "avi", Video: &VideoStream{Codec: "mpeg4", Height: 480}, Audio: []AudioStream{{Codec: "mp3"}}}, file: "a.avi", mode: ModeTranscode},
		{name: "lower quality", info: MediaInfo{Container: mp4, Video: h264, Audio: aac}, file: "a.mp4", quality: "720p", mode: ModeTranscode,
			check: func(o StreamOptions) bool { return o.Height == 720 && o.MaxBitrate == 4000 }},
		{name: "quality above the source", info: MediaInfo{Container: mp4, Video: &VideoStream{Codec: "h264", Height: 720}, Audio: aac}, file: "a.mp4", quality: "1080p", mode: ModeDirect},
		{name: "10-bit H.264", info: MediaInfo{Container: mp4, Video: &VideoStream{Codec: "h264", Height: 1080, PixelFormat: "yuv420p10le"}, Audio: aac}, file: "a.mp4", mode: ModeTranscode},
	}
	for _, test := range cases {
		if test.quality == "" {
			test.quality = QualityOriginal
		}
		audio := test.audio
		if audio == 0 && test.name != "AC3 audio" {
			audio = -1
		}
		got := decide(test.info, test.file, test.caps, audio, test.quality)
		if got.mode != test.mode || test.check != nil && !test.check(got.options) {
			t.Errorf("%s: decide() = %s %+v, want %s", test.name, got.mode, got.options, test.mode)
		}
	}
	if got := availableQualities(MediaInfo{Video: &VideoStream{Height: 1080}}); strings.Join(got, ",") != "original,720p,480p" {
		t.Errorf("qualities for 1080p = %v", got)
	}
}

func TestStreamsSubtitlesAndArtworkGoThroughTheProcessor(t *testing.T) {
	processor := &fakeProcessor{}
	env := newTestEnv(t, processor)
	env.write(t, env.shared, "电影/Heat (1995).mkv", video("matroska,webm", "h264", "ac3", 1080, 10200))
	gbk := string([]byte{'1', '\n', 0xc4, 0xe3, 0xba, 0xc3})
	env.write(t, env.shared, "电影/Heat (1995).chs.srt", gbk)
	env.library(t, env.admin, env.shared, LibraryMovies, "电影")
	env.media.processPending(withSessions(t, env))
	movies, _ := env.media.Titles(env.ctx, env.bob, TitleQuery{Category: "movie"})
	id := movies.Items[0].ID

	playback, err := env.media.Playback(env.ctx, env.bob, id, Capabilities{Matroska: true}, -1, "")
	if err != nil || playback.Mode != ModeRemux || !strings.Contains(playback.Reason, "AC3") {
		t.Fatalf("Playback() = %+v, %v", playback, err)
	}
	stream, err := env.media.Stream(env.ctx, env.bob, id, StreamRequest{Mode: ModeRemux, Start: 120, Audio: -1, Caps: Capabilities{Matroska: true}})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	if data, _ := io.ReadAll(stream); string(data) != "fmp4" {
		t.Fatalf("stream = %q", data)
	}
	_ = stream.Close()
	if options := processor.streams[0]; options.Start != 120 || !options.CopyVideo || options.CopyAudio {
		t.Fatalf("stream options = %+v", options)
	}
	if _, err := env.media.Stream(env.ctx, env.bob, id, StreamRequest{Start: 99999}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Stream() past the end: %v", err)
	}
	data, err := env.media.Subtitle(env.ctx, env.bob, id, "x0")
	if err != nil || !bytes.HasPrefix(data, []byte("WEBVTT\n\nGB18030:")) {
		t.Fatalf("Subtitle() = %q, %v", data, err)
	}
	if _, err := env.media.Subtitle(env.ctx, env.bob, id, "x9"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing subtitle: %v", err)
	}
	image, err := env.media.Artwork(env.ctx, env.bob, id, "backdrop")
	if err != nil {
		t.Fatalf("Artwork() error = %v", err)
	}
	contents, _ := io.ReadAll(image.File)
	_ = image.File.Close()
	if string(contents) != "jpeg" {
		t.Fatalf("frame = %q", contents)
	}
	if _, err := env.media.Artwork(env.ctx, env.bob, id, "poster"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("poster without one: %v", err)
	}
}
