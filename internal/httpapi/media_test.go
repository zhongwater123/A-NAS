package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
	"github.com/zhongwater123/A-NAS/internal/media"
	"github.com/zhongwater123/A-NAS/internal/mediaapi"
)

type mediaFixture struct {
	accounts *accounts.Service
	files    *files.Service
	media    *media.Service
	admin    accounts.User
	shared   accounts.Space
	library  string
	film     string
	show     string
	episode  string
	folder   string
}

// newMediaFixture scans a shared library holding a film with a poster and
// a WebVTT subtitle, and a show with one episode.
func newMediaFixture(t *testing.T) *mediaFixture {
	t.Helper()
	ctx := context.Background()
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatalf("open account store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	accountService := accounts.NewService(store, apiCredentials{}, accounts.Options{})
	admin, err := accountService.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatalf("setup administrator: %v", err)
	}
	fixture := &mediaFixture{accounts: accountService, admin: admin}
	spaces, err := accountService.ListSpaces(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	for _, space := range spaces {
		if space.Kind == accounts.SpaceKindShared {
			fixture.shared = space
		}
	}
	catalog, err := files.OpenSQLite(filepath.Join(t.TempDir(), "files.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	volume := filepath.Join(t.TempDir(), "volume")
	fixture.files = files.NewService(catalog, volume, accountService, files.Options{DisableCapacityReserve: true, AllowUnverifiedVolume: true})
	shared := filepath.Join(volume, "spaces", "shared", "影视")
	var poster bytes.Buffer
	if err := jpeg.Encode(&poster, image.NewGray(image.Rect(0, 0, 4, 6)), nil); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{
		"Heat (1995)/Heat (1995).mkv":    "0123456789abcdefghij",
		"Heat (1995)/poster.jpg":         poster.String(),
		"Heat (1995)/Heat (1995).zh.vtt": "WEBVTT\n\n00:01.000 --> 00:02.000\n你好\n",
		"繁花/繁花 第01集.mp4":                 "episode",
	} {
		target := filepath.Join(shared, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mediaStore, err := media.OpenSQLite(filepath.Join(t.TempDir(), "media.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mediaStore.Close() })
	fixture.media, err = media.NewService(mediaStore, media.Options{Files: fixture.files, Spaces: accountService, CacheDir: filepath.Join(t.TempDir(), "cache")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fixture.media.Close)
	tree, err := fixture.files.Tree(ctx, admin, fixture.shared.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range tree {
		if entry.Path == "影视" {
			fixture.folder = entry.ID
		}
	}
	library, err := fixture.media.CreateLibrary(ctx, admin, media.LibraryInput{Name: "影视", Kind: media.LibraryMixed, SpaceID: fixture.shared.ID, FolderIDs: []string{fixture.folder}})
	if err != nil {
		t.Fatalf("CreateLibrary() error = %v", err)
	}
	fixture.library = library.ID
	if err := fixture.media.Scan(ctx, admin, library.ID); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	titles, err := fixture.media.Titles(ctx, admin, media.TitleQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range titles.Items {
		switch title.Type {
		case media.TypeMovie:
			fixture.film = title.ID
		case media.TypeShow:
			fixture.show = title.ID
		}
	}
	show, err := fixture.media.Show(ctx, admin, fixture.show)
	if err != nil {
		t.Fatalf("Show() error = %v", err)
	}
	fixture.episode = show.SeasonList[0].Episodes[0].ID
	if fixture.film == "" || fixture.episode == "" {
		t.Fatalf("fixture titles = %+v", titles.Items)
	}
	return fixture
}

// mediaAPIFor returns the media API acting for user, as the Product Service
// mounts it after confirming the session.
func mediaAPIFor(service *media.Service, user accounts.User) http.Handler {
	api := mediaapi.New(service, nil)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.ServeHTTP(w, r.WithContext(mediaapi.WithUser(r.Context(), user)))
	})
}

func TestMediaAPIUsesTheSessionUserAndRequiresCSRFForWrites(t *testing.T) {
	fixture := newMediaFixture(t)
	handler := httpapi.NewProduct(httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated, Accounts: fixture.accounts, Files: fixture.files,
		Media: mediaapi.New(fixture.media, nil), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	anonymous := httptest.NewRecorder()
	handler.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/api/v1/media/home", nil))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", anonymous.Code)
	}
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/v1/session", strings.NewReader(`{"username":"owner","password":"correct horse battery staple"}`))
	loginRequest.Header.Set("Content-Type", "application/json")
	login := httptest.NewRecorder()
	handler.ServeHTTP(login, loginRequest)
	var session struct {
		CSRFToken string `json:"csrfToken"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &session); err != nil || login.Code != http.StatusOK {
		t.Fatalf("login = %d %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	send := func(method, path, body, csrf string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.AddCookie(cookie)
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		if csrf != "" {
			request.Header.Set("X-CSRF-Token", csrf)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}
	if home := send(http.MethodGet, "/api/v1/media/home", "", ""); home.Code != http.StatusOK || !strings.Contains(home.Body.String(), fixture.film) {
		t.Fatalf("home = %d %s", home.Code, home.Body.String())
	}
	if forged := send(http.MethodPut, "/api/v1/media/favorites/"+fixture.film, "", ""); forged.Code != http.StatusForbidden {
		t.Fatalf("favorite without CSRF = %d", forged.Code)
	}
	if saved := send(http.MethodPut, "/api/v1/media/favorites/"+fixture.film, "", session.CSRFToken); saved.Code != http.StatusNoContent {
		t.Fatalf("favorite = %d %s", saved.Code, saved.Body.String())
	}
	if favorites := send(http.MethodGet, "/api/v1/media/favorites", "", ""); !strings.Contains(favorites.Body.String(), fixture.film) {
		t.Fatalf("favorites = %s", favorites.Body.String())
	}

	// The original plays directly with range requests, opened as the user.
	partial := httptest.NewRequest(http.MethodGet, "/api/v1/media/videos/"+fixture.film+"/file", nil)
	partial.AddCookie(cookie)
	partial.Header.Set("Range", "bytes=10-13")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, partial)
	if recorder.Code != http.StatusPartialContent || recorder.Body.String() != "abcd" || recorder.Header().Get("Content-Type") != "video/x-matroska" {
		t.Fatalf("range = %d %q %v", recorder.Code, recorder.Body.String(), recorder.Header())
	}
	if poster := send(http.MethodGet, "/api/v1/media/artwork/"+fixture.film+"/poster", "", ""); poster.Code != http.StatusOK ||
		poster.Header().Get("Content-Type") != "image/jpeg" || !strings.Contains(poster.Header().Get("Cache-Control"), "max-age") {
		t.Fatalf("poster = %d %v", poster.Code, poster.Header())
	}
	if subtitle := send(http.MethodGet, "/api/v1/media/videos/"+fixture.film+"/subtitles/x0", "", ""); subtitle.Code != http.StatusOK || !strings.Contains(subtitle.Body.String(), "你好") {
		t.Fatalf("subtitle = %d %s", subtitle.Code, subtitle.Body.String())
	}
}
