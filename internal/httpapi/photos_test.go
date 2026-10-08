package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
)

func TestPhotoAPIUsesTheSessionUserAndRequiresCSRFForWrites(t *testing.T) {
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatalf("open account store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	photoService, err := photos.Open(filepath.Join(t.TempDir(), "photos"), photos.Options{DisableCapacityReserve: true})
	if err != nil {
		t.Fatalf("open photos: %v", err)
	}
	t.Cleanup(func() { _ = photoService.Close() })
	handler := httpapi.NewProduct(httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated,
		Accounts: accounts.NewService(store, apiCredentials{}, accounts.Options{}),
		Photos:   photosapi.New(photoService, nil),
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	anonymous := httptest.NewRecorder()
	handler.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/api/v1/photos/libraries", nil))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", anonymous.Code)
	}

	setupRequest := httptest.NewRequest(http.MethodPost, "/api/v1/setup/admin", bytes.NewBufferString(`{"username":"owner","password":"correct horse battery staple"}`))
	setupRequest.Header.Set("Content-Type", "application/json")
	setup := httptest.NewRecorder()
	handler.ServeHTTP(setup, setupRequest)
	var session struct {
		CSRFToken string `json:"csrfToken"`
		User      struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(setup.Body.Bytes(), &session); err != nil || setup.Code != http.StatusCreated {
		t.Fatalf("setup = %d %s", setup.Code, setup.Body.String())
	}
	cookie := setup.Result().Cookies()[0]

	librariesRequest := httptest.NewRequest(http.MethodGet, "/api/v1/photos/libraries", nil)
	librariesRequest.AddCookie(cookie)
	libraries := httptest.NewRecorder()
	handler.ServeHTTP(libraries, librariesRequest)
	var list struct {
		Items []photos.Library `json:"items"`
	}
	if err := json.Unmarshal(libraries.Body.Bytes(), &list); err != nil || libraries.Code != http.StatusOK || len(list.Items) != 2 {
		t.Fatalf("libraries = %d %s", libraries.Code, libraries.Body.String())
	}
	var private photos.Library
	for _, lib := range list.Items {
		if lib.Kind == photos.LibraryKindPrivate {
			private = lib
		}
	}
	if private.OwnerUserID != session.User.ID {
		t.Fatalf("private library owner = %q, want the session user %q", private.OwnerUserID, session.User.ID)
	}

	create := func(csrf string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/photos/libraries/"+private.ID+"/directories", bytes.NewBufferString(`{"name":"Trips"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", csrf)
		request.AddCookie(cookie)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}
	if response := create("wrong"); response.Code != http.StatusForbidden {
		t.Fatalf("write without CSRF status = %d", response.Code)
	}
	if response := create(session.CSRFToken); response.Code != http.StatusCreated {
		t.Fatalf("write with CSRF status = %d %s", response.Code, response.Body.String())
	}
}

// apiClient drives the product API as one signed-in browser.
type apiClient struct {
	t       *testing.T
	handler http.Handler
	cookie  *http.Cookie
	csrf    string
}

func (c *apiClient) call(method, path string, body any, wantStatus int, target any) {
	c.t.Helper()
	var encoded []byte
	if body != nil {
		encoded, _ = json.Marshal(body)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.cookie != nil {
		request.AddCookie(c.cookie)
		request.Header.Set("X-CSRF-Token", c.csrf)
	}
	recorder := httptest.NewRecorder()
	c.handler.ServeHTTP(recorder, request)
	if recorder.Code != wantStatus {
		c.t.Fatalf("%s %s status = %d, want %d; body=%s", method, path, recorder.Code, wantStatus, recorder.Body.String())
	}
	if target != nil {
		if err := json.Unmarshal(recorder.Body.Bytes(), target); err != nil {
			c.t.Fatalf("decode %s %s: %v", method, path, err)
		}
	}
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == "anas_session" {
			c.cookie = cookie
		}
	}
	var session struct {
		CSRFToken string `json:"csrfToken"`
	}
	if json.Unmarshal(recorder.Body.Bytes(), &session) == nil && session.CSRFToken != "" {
		c.csrf = session.CSRFToken
	}
}

func TestLibraryViewingThroughTheProductAPI(t *testing.T) {
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	photoService, err := photos.Open(filepath.Join(t.TempDir(), "photos"), photos.Options{DisableCapacityReserve: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = photoService.Close() })
	handler := httpapi.NewProduct(httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated,
		Accounts: accounts.NewService(store, apiCredentials{}, accounts.Options{}),
		Photos:   photosapi.New(photoService, nil),
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	admin := &apiClient{t: t, handler: handler}
	admin.call(http.MethodPost, "/api/v1/setup/admin", map[string]string{"username": "owner", "password": "correct horse battery staple"}, http.StatusCreated, nil)
	var alice accounts.User
	admin.call(http.MethodPost, "/api/v1/users", map[string]string{"username": "alice", "password": "alice password for testing"}, http.StatusCreated, &alice)
	member := &apiClient{t: t, handler: handler}
	member.call(http.MethodPost, "/api/v1/session", map[string]string{"username": "alice", "password": "alice password for testing"}, http.StatusOK, nil)

	var libraries struct {
		Items []photos.Library `json:"items"`
	}
	member.call(http.MethodGet, "/api/v1/photos/libraries", nil, http.StatusOK, &libraries)
	var private photos.Library
	for _, lib := range libraries.Items {
		if lib.Kind == photos.LibraryKindPrivate {
			private = lib
		}
	}
	asset, err := photoService.Import(context.Background(), photos.Principal{UserID: alice.ID, Username: "alice"}, photos.ImportRequest{
		LibraryID: private.ID, Name: "wedding.png", Content: bytes.NewReader(tinyPNG(t)),
	})
	if err != nil {
		t.Fatal(err)
	}
	timeline := "/api/v1/photos/libraries/" + private.ID + "/timeline"
	admin.call(http.MethodGet, timeline, nil, http.StatusNotFound, nil)

	var grant accounts.ViewingGrant
	admin.call(http.MethodPost, "/api/v1/users/"+alice.ID+"/viewing",
		map[string]string{"password": "correct horse battery staple", "reason": "find the wedding photos", "scope": "library"}, http.StatusCreated, &grant)
	if grant.Scope != accounts.ViewingScopeLibrary {
		t.Fatalf("grant = %+v", grant)
	}
	admin.call(http.MethodGet, "/api/v1/photos/libraries", nil, http.StatusOK, &libraries)
	var viewed *photos.Library
	for i := range libraries.Items {
		if libraries.Items[i].ID == private.ID {
			viewed = &libraries.Items[i]
		}
	}
	if viewed == nil || viewed.Viewing == nil || viewed.Viewing.GrantID != grant.ID || viewed.OwnerName != "alice" {
		t.Fatalf("admin libraries = %+v", libraries.Items)
	}
	var page struct {
		Items []photos.Asset `json:"items"`
	}
	admin.call(http.MethodGet, timeline, nil, http.StatusOK, &page)
	if len(page.Items) != 1 || page.Items[0].ID != asset.ID {
		t.Fatalf("viewed timeline = %+v", page.Items)
	}
	admin.call(http.MethodPatch, "/api/v1/photos/assets/"+asset.ID, map[string]string{"name": "mine.png"}, http.StatusForbidden, nil)
	admin.call(http.MethodDelete, "/api/v1/photos/assets/"+asset.ID, nil, http.StatusForbidden, nil)
	admin.call(http.MethodGet, "/api/v1/photos/libraries/"+private.ID+"/trash", nil, http.StatusOK, nil)

	var notifications struct {
		Items []accounts.Notification `json:"items"`
	}
	member.call(http.MethodGet, "/api/v1/notifications", nil, http.StatusOK, &notifications)
	if len(notifications.Items) != 1 || notifications.Items[0].Kind != accounts.NotificationAdminLibraryViewing {
		t.Fatalf("member notifications = %+v", notifications.Items)
	}

	admin.call(http.MethodDelete, "/api/v1/viewing/"+grant.ID, nil, http.StatusNoContent, nil)
	admin.call(http.MethodGet, timeline, nil, http.StatusNotFound, nil)
	admin.call(http.MethodPost, "/api/v1/users/"+alice.ID+"/viewing",
		map[string]string{"password": "correct horse battery staple", "reason": "x", "scope": "everything"}, http.StatusBadRequest, nil)
}

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewGray(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}
