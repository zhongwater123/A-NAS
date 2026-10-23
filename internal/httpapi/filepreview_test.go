package httpapi_test

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/filepreview"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
	"github.com/zhongwater123/A-NAS/internal/mediaworker"
)

type previewWorker struct{ mediaworker.Worker }

func (previewWorker) Image(context.Context, *os.File, int) ([]byte, error) {
	var b bytes.Buffer
	err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	return b.Bytes(), err
}
func TestFilePreviewRequiresSessionAndSourceAccess(t *testing.T) {
	ctx := context.Background()
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	accountService := accounts.NewService(store, apiCredentials{}, accounts.Options{})
	owner, err := accountService.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accountService.CreateMember(ctx, owner, "member", "member horse battery staple"); err != nil {
		t.Fatal(err)
	}
	fileStore, err := files.OpenSQLite(filepath.Join(t.TempDir(), "files.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fileStore.Close() }()
	fileService := files.NewService(fileStore, filepath.Join(t.TempDir(), "volume"), accountService, files.Options{DisableCapacityReserve: true, AllowUnverifiedVolume: true})
	spaces, err := accountService.ListSpaces(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	var private string
	for _, space := range spaces {
		if space.Kind == accounts.SpaceKindPrivate {
			private = space.ID
		}
	}
	entry, err := fileService.Upload(ctx, owner, private, "", "photo.heic", strings.NewReader("original HEIC"))
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.NewProduct(httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), Accounts: accountService, Files: fileService,
		FilePreviews: filepreview.New(fileService, previewWorker{}), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ownerLogin := signIn(t, handler, "owner", "correct horse battery staple")
	memberLogin := signIn(t, handler, "member", "member horse battery staple")
	path := "/api/v1/files/" + entry.ID + "/preview"
	request := func(path string, cookie *http.Cookie, etag ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if len(etag) > 0 {
			r.Header.Set("If-None-Match", etag[0])
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := request(path, nil); w.Code != 401 {
		t.Fatalf("anonymous = %d", w.Code)
	}
	for _, size := range []string{"thumbnail", "preview"} {
		w := request(path+"?size="+size, ownerLogin.cookie)
		if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || w.Header().Get("Cache-Control") != "private, no-cache" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("preview = %d %v %s", w.Code, w.Header(), w.Body)
		}
	}
	etag := request(path, ownerLogin.cookie).Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing validator")
	}
	if w := request(path, ownerLogin.cookie, etag); w.Code != 304 || w.Body.Len() != 0 || w.Header().Get("Cache-Control") != "private, no-cache" || w.Header().Get("Vary") != "Cookie" {
		t.Fatalf("unchanged preview = %d %v %s", w.Code, w.Header(), w.Body)
	}
	if w := request(path, ownerLogin.cookie, "\"old-version\""); w.Code != 200 {
		t.Fatalf("changed validator = %d", w.Code)
	}
	if w := request(path, nil, etag); w.Code != 401 {
		t.Fatalf("anonymous conditional = %d", w.Code)
	}
	if w := request(path, memberLogin.cookie, etag); w.Code != 403 {
		t.Fatalf("other account after cache = %d %s", w.Code, w.Body)
	}
	if w := request(path+"?size=99999", ownerLogin.cookie); w.Code != 400 {
		t.Fatalf("size = %d", w.Code)
	}
	if w := request("/api/v1/files/"+entry.ID+"/content", ownerLogin.cookie); w.Code != 200 || w.Body.String() != "original HEIC" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("original = %d %v %s", w.Code, w.Header(), w.Body)
	}
	if _, err := fileService.DeleteByID(ctx, owner, entry.ID); err != nil {
		t.Fatal(err)
	}
	if w := request(path, ownerLogin.cookie, etag); w.Code != 404 {
		t.Fatalf("trashed after cache = %d %s", w.Code, w.Body)
	}
}
