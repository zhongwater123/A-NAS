package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/photos"
)

type wallpaperView struct {
	Style string `json:"style"`
	Image *struct {
		Version string `json:"version"`
		Width   int    `json:"width"`
		Height  int    `json:"height"`
	} `json:"image"`
}

func TestAdministratorsChooseTheWallpaperEveryoneSees(t *testing.T) {
	ctx := context.Background()
	f := newActivityFixture(t, func(*photos.Service, *accounts.Service) http.Handler { return nil })
	decode := func(recorder *httptest.ResponseRecorder) wallpaperView {
		t.Helper()
		var wallpaper wallpaperView
		if err := json.Unmarshal(recorder.Body.Bytes(), &wallpaper); err != nil {
			t.Fatalf("decode wallpaper: %v; body=%s", err, recorder.Body.String())
		}
		return wallpaper
	}
	owner := f.signIn("/api/v1/setup/admin", "owner", "correct horse battery staple", "", http.StatusCreated)
	admin, err := f.accounts.UserIDForUsername(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.accounts.CreateMember(ctx, accounts.User{ID: admin, Role: accounts.RoleAdmin, Status: accounts.UserStatusActive}, "alice", "alice password for testing"); err != nil {
		t.Fatal(err)
	}
	alice := f.signIn("/api/v1/session", "alice", "alice password for testing", "192.168.1.20", http.StatusOK)

	if got := decode(alice.do(http.MethodGet, "/api/v1/wallpaper", "", nil, http.StatusOK)); got.Style != "fill" || got.Image != nil {
		t.Fatalf("default wallpaper = %#v", got)
	}
	owner.do(http.MethodGet, "/api/v1/wallpaper/image", "", nil, http.StatusNotFound)

	// Members see the wallpaper but cannot change it.
	png := map[string]string{"Content-Type": "image/png"}
	alice.do(http.MethodPut, "/api/v1/wallpaper/image", profilePNG(t, 800, 600), png, http.StatusForbidden)
	alice.do(http.MethodPatch, "/api/v1/wallpaper", `{"style":"tile"}`, nil, http.StatusForbidden)
	alice.do(http.MethodDelete, "/api/v1/wallpaper/image", "", nil, http.StatusForbidden)

	owner.do(http.MethodPut, "/api/v1/wallpaper/image", "not a png", png, http.StatusUnsupportedMediaType)
	set := decode(owner.do(http.MethodPut, "/api/v1/wallpaper/image", profilePNG(t, 800, 600), png, http.StatusOK))
	if set.Style != "fill" || set.Image == nil || set.Image.Width != 800 || set.Image.Height != 600 {
		t.Fatalf("after uploading = %#v", set)
	}
	if got := decode(owner.do(http.MethodPatch, "/api/v1/wallpaper", `{"style":"center"}`, nil, http.StatusOK)); got.Style != "center" || got.Image == nil || got.Image.Version != set.Image.Version {
		t.Fatalf("after changing the style = %#v", got)
	}
	if got := decode(alice.do(http.MethodGet, "/api/v1/wallpaper", "", nil, http.StatusOK)); got.Style != "center" || got.Image == nil || got.Image.Version != set.Image.Version {
		t.Fatalf("member sees %#v", got)
	}

	served := alice.do(http.MethodGet, "/api/v1/wallpaper/image?v="+set.Image.Version, "", nil, http.StatusOK)
	if got := served.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Fatalf("versioned wallpaper Cache-Control = %q", got)
	}
	picture, err := jpeg.Decode(bytes.NewReader(served.Body.Bytes()))
	if err != nil || picture.Bounds().Dx() != 800 || picture.Bounds().Dy() != 600 {
		t.Fatalf("served wallpaper = %v, %v", picture.Bounds(), err)
	}
	if got := alice.do(http.MethodGet, "/api/v1/wallpaper/image", "", nil, http.StatusOK).Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Fatalf("unversioned wallpaper Cache-Control = %q", got)
	}
	anonymous := httptest.NewRecorder()
	f.handler.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/api/v1/wallpaper/image", nil))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("wallpaper without a session = %d", anonymous.Code)
	}

	// Requests the contract itself rules out are sent around the contract client.
	patch := func(body string) int {
		request := httptest.NewRequest(http.MethodPatch, "/api/v1/wallpaper", strings.NewReader(body))
		request.AddCookie(owner.cookie)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", owner.csrf)
		recorder := httptest.NewRecorder()
		f.handler.ServeHTTP(recorder, request)
		return recorder.Code
	}
	if got := patch(`{"style":"zoom"}`); got != http.StatusUnprocessableEntity {
		t.Errorf("unknown style = %d", got)
	}
	if got := patch(`{}`); got != http.StatusBadRequest {
		t.Errorf("no style = %d", got)
	}

	reset := decode(owner.do(http.MethodDelete, "/api/v1/wallpaper/image", "", nil, http.StatusOK))
	if reset.Style != "fill" || reset.Image != nil {
		t.Fatalf("after restoring the default = %#v", reset)
	}
	alice.do(http.MethodGet, "/api/v1/wallpaper/image", "", nil, http.StatusNotFound)
}
