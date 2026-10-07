package httpapi_test

import (
	"bytes"
	"encoding/json"
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
