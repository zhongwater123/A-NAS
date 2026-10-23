package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/photos"
)

func profilePNG(t *testing.T, width, height int) string {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := range width {
		for y := range height {
			picture.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 160, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, picture); err != nil {
		t.Fatal(err)
	}
	return encoded.String()
}

func decodeUser(t *testing.T, recorder *httptest.ResponseRecorder) accounts.User {
	t.Helper()
	var user accounts.User
	if err := json.Unmarshal(recorder.Body.Bytes(), &user); err != nil {
		t.Fatalf("decode user: %v; body=%s", err, recorder.Body.String())
	}
	return user
}

func TestEveryoneChoosesTheirOwnNicknameAndAvatar(t *testing.T) {
	ctx := context.Background()
	f := newActivityFixture(t, func(*photos.Service) http.Handler { return nil })
	owner := f.signIn("/api/v1/setup/admin", "owner", "correct horse battery staple", "", http.StatusCreated)
	admin, err := f.accounts.UserIDForUsername(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.accounts.CreateMember(ctx, accounts.User{ID: admin, Role: accounts.RoleAdmin, Status: accounts.UserStatusActive}, "alice", "alice password for testing"); err != nil {
		t.Fatal(err)
	}
	alice := f.signIn("/api/v1/session", "alice", "alice password for testing", "192.168.1.20", http.StatusOK)

	renamed := decodeUser(t, alice.do(http.MethodPatch, "/api/v1/session/profile", `{"nickname":" 小爱 "}`, nil, http.StatusOK))
	if renamed.Nickname != "小爱" || renamed.Username != "alice" {
		t.Fatalf("renamed user = %#v", renamed)
	}
	owner.do(http.MethodPatch, "/api/v1/session/profile", `{"nickname":"小爱"}`, nil, http.StatusConflict)
	owner.do(http.MethodPatch, "/api/v1/session/profile", `{"nickname":"ALICE"}`, nil, http.StatusConflict)
	alice.do(http.MethodPatch, "/api/v1/session/profile", `{"nickname":"a\u202eb"}`, nil, http.StatusUnprocessableEntity)
	// Requests the contract itself rules out are sent around the contract client.
	outsideContract := func(method, path, contentType, body string) int {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.AddCookie(alice.cookie)
		request.Header.Set("Content-Type", contentType)
		request.Header.Set("X-CSRF-Token", alice.csrf)
		request.Header.Set("X-Forwarded-For", alice.forwardedFor)
		recorder := httptest.NewRecorder()
		f.handler.ServeHTTP(recorder, request)
		return recorder.Code
	}
	if got := outsideContract(http.MethodPatch, "/api/v1/session/profile", "application/json", `{}`); got != http.StatusBadRequest {
		t.Fatalf("profile without a nickname = %d", got)
	}

	// The page sends a cropped picture; the server always makes the avatar.
	if got := outsideContract(http.MethodPut, "/api/v1/session/avatar", "text/plain", "plain text"); got != http.StatusUnsupportedMediaType {
		t.Fatalf("avatar sent as text = %d", got)
	}
	alice.do(http.MethodPut, "/api/v1/session/avatar", "not a png", map[string]string{"Content-Type": "image/png"}, http.StatusUnsupportedMediaType)
	pictured := decodeUser(t, alice.do(http.MethodPut, "/api/v1/session/avatar", profilePNG(t, 600, 400), map[string]string{"Content-Type": "image/png"}, http.StatusOK))
	if pictured.AvatarVersion == "" || pictured.Nickname != "小爱" {
		t.Fatalf("pictured user = %#v", pictured)
	}
	withoutCSRF := *alice
	withoutCSRF.csrf = "wrong"
	withoutCSRF.do(http.MethodDelete, "/api/v1/session/avatar", "", nil, http.StatusForbidden)

	var session struct{ User accounts.User }
	_ = json.Unmarshal(alice.do(http.MethodGet, "/api/v1/session", "", nil, http.StatusOK).Body.Bytes(), &session)
	if session.User.Nickname != "小爱" || session.User.AvatarVersion != pictured.AvatarVersion {
		t.Fatalf("session user = %#v", session.User)
	}

	// Every signed-in account, members included, reads every profile.
	var profiles struct{ Items []accounts.Profile }
	_ = json.Unmarshal(owner.do(http.MethodGet, "/api/v1/profiles", "", nil, http.StatusOK).Body.Bytes(), &profiles)
	if len(profiles.Items) != 2 || profiles.Items[0].ID != pictured.ID || profiles.Items[0].Nickname != "小爱" || profiles.Items[0].AvatarVersion != pictured.AvatarVersion {
		t.Fatalf("profiles = %#v", profiles.Items)
	}
	alice.do(http.MethodGet, "/api/v1/profiles", "", nil, http.StatusOK)

	avatarPath := "/api/v1/users/" + pictured.ID + "/avatar"
	served := owner.do(http.MethodGet, avatarPath+"?v="+pictured.AvatarVersion, "", nil, http.StatusOK)
	if got := served.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Fatalf("versioned avatar Cache-Control = %q", got)
	}
	picture, err := jpeg.Decode(bytes.NewReader(served.Body.Bytes()))
	if err != nil {
		t.Fatalf("avatar is not a JPEG: %v", err)
	}
	if bounds := picture.Bounds(); bounds.Dx() != 256 || bounds.Dy() != 256 {
		t.Fatalf("avatar size = %v", bounds)
	}
	if served.Body.Len() > 40<<10 {
		t.Fatalf("avatar is %d bytes", served.Body.Len())
	}
	unversioned := owner.do(http.MethodGet, avatarPath, "", nil, http.StatusOK)
	if got := unversioned.Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Fatalf("unversioned avatar Cache-Control = %q", got)
	}
	revalidate := httptest.NewRequest(http.MethodGet, avatarPath, nil)
	revalidate.AddCookie(owner.cookie)
	revalidate.Header.Set("If-None-Match", unversioned.Header().Get("ETag"))
	notModified := httptest.NewRecorder()
	f.handler.ServeHTTP(notModified, revalidate)
	if notModified.Code != http.StatusNotModified {
		t.Fatalf("revalidated avatar status = %d", notModified.Code)
	}
	anonymous := httptest.NewRecorder()
	f.handler.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, avatarPath, nil))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("avatar without a session = %d", anonymous.Code)
	}

	// The log names people by nickname and finds them by it.
	page := owner.activity("?category=account&q=" + "%E5%B0%8F%E7%88%B1")
	var found bool
	for _, item := range page.Items {
		if item.Action == "user.nickname_changed" && item.Actor != nil && item.Actor.Nickname == "小爱" {
			found = true
		}
	}
	if !found {
		t.Fatalf("nickname change not found by nickname: %#v", page.Items)
	}

	cleared := decodeUser(t, alice.do(http.MethodDelete, "/api/v1/session/avatar", "", nil, http.StatusOK))
	if cleared.AvatarVersion != "" {
		t.Fatalf("cleared user = %#v", cleared)
	}
	owner.do(http.MethodGet, avatarPath, "", nil, http.StatusNotFound)
}
