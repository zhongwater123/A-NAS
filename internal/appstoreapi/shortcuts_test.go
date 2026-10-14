package appstoreapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/appstoreapi"
)

type shortcutList struct {
	Items []struct {
		ID, Title, Scheme, Path string
		WebPort                 int
		Running, Total          int
	}
}

// Only installed apps with a web interface get a desktop shortcut.
func TestShortcutsListInstalledAppsWithAWebInterface(t *testing.T) {
	store := newStore(t)
	handler := appstoreapi.NewShortcuts(store)
	var list shortcutList
	decode(t, serve(handler, http.MethodGet, "/api/v1/app-shortcuts", "", nil), http.StatusOK, &list)
	if len(list.Items) != 0 {
		t.Fatalf("shortcuts before any install = %+v", list.Items)
	}

	ctx := context.Background()
	host := &recordingHost{}
	identity, _ := host.AppIdentity(ctx, "navidrome")
	plan, err := store.Plan(ctx, "navidrome", identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Install(ctx, "navidrome", plan.Digest, identity); err != nil {
		t.Fatal(err)
	}
	store.Wait()
	decode(t, serve(handler, http.MethodGet, "/api/v1/app-shortcuts", "", nil), http.StatusOK, &list)
	if len(list.Items) != 1 {
		t.Fatalf("shortcuts = %+v, want Navidrome", list.Items)
	}
	if got := list.Items[0]; got.ID != "navidrome" || got.Title != "Navidrome" || got.WebPort != 4533 || got.Running != 1 || got.Total != 1 {
		t.Fatalf("shortcut = %+v", got)
	}
}

func TestShortcutsAreEmptyWhileTheAppCenterIsDisabled(t *testing.T) {
	response := serve(appstoreapi.NewShortcuts(nil), http.MethodGet, "/api/v1/app-shortcuts", "", nil)
	var raw map[string]json.RawMessage
	decode(t, response, http.StatusOK, &raw)
	if string(raw["items"]) != "[]" {
		t.Fatalf("items = %s, want an empty list", raw["items"])
	}
}

func TestShortcutIconsAreSandboxedAndReadOnly(t *testing.T) {
	handler := appstoreapi.NewShortcuts(newStore(t))
	icon := serve(handler, http.MethodGet, "/api/v1/app-shortcuts/navidrome/icon", "", nil)
	if icon.Code != http.StatusOK || icon.Header().Get("X-Content-Type-Options") != "nosniff" || icon.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("icon = %d %v", icon.Code, icon.Header())
	}
	for path, want := range map[string]int{
		"/api/v1/app-shortcuts/no-such-app/icon": http.StatusNotFound,
		"/api/v1/app-shortcuts/navidrome":        http.StatusNotFound,
		"/api/v1/app-shortcuts/NotValid/icon":    http.StatusNotFound,
	} {
		if got := serve(handler, http.MethodGet, path, "", nil); got.Code != want {
			t.Errorf("GET %s = %d, want %d", path, got.Code, want)
		}
	}
	if got := serve(handler, http.MethodPost, "/api/v1/app-shortcuts", "{}", nil); got.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, want 405", got.Code)
	}
}
