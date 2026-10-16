package appstoreapi

import (
	"net/http"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/appstore"
)

// ShortcutsPrefix serves the desktop shortcuts of installed apps to every
// signed-in user: an app installed for the household is opened from the
// desktop, not from the administrators' App Center or Docker. It only says
// which apps have a web interface, where it is and whether it runs; how an
// app was installed stays in /api/v1/apps.
const ShortcutsPrefix = "/api/v1/app-shortcuts"

type shortcutsHandler struct {
	store appstore.Store
}

// NewShortcuts returns the shortcuts API; with a nil store, as when the App
// Center is disabled, the list is empty.
func NewShortcuts(store appstore.Store) http.Handler {
	return &shortcutsHandler{store: store}
}

// ShortcutDocument is an installed app the desktop can open in a new window.
type ShortcutDocument struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	WebPort uint16 `json:"webPort"`
	Scheme  string `json:"scheme"`
	Path    string `json:"path"`
	// Running and Total count the app's containers.
	Running int `json:"running"`
	Total   int `json:"total"`
}

type shortcutsResponse struct {
	Items []ShortcutDocument `json:"items"`
}

func (h *shortcutsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodGet) {
		return
	}
	if r.URL.Path == ShortcutsPrefix {
		h.list(w, r)
		return
	}
	id, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, ShortcutsPrefix+"/"), "/icon")
	if !ok || !appstore.ValidID(id) || h.store == nil {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	icon, contentType, err := h.store.Icon(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	// Catalog icons are fixed for a release; SVGs must not run scripts.
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(icon)
}

// list returns installed apps that have a web interface; installing,
// uninstalling and available apps have no shortcut.
func (h *shortcutsHandler) list(w http.ResponseWriter, r *http.Request) {
	response := shortcutsResponse{Items: []ShortcutDocument{}}
	if h.store == nil {
		writeJSON(w, http.StatusOK, response)
		return
	}
	apps, err := h.store.Apps(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "apps_unavailable", "the container engine could not list apps")
		return
	}
	for _, app := range apps {
		if app.State != appstore.StateInstalled || app.WebPort == 0 {
			continue
		}
		response.Items = append(response.Items, ShortcutDocument{
			ID: app.ID, Title: app.Title, WebPort: app.WebPort, Scheme: app.Scheme, Path: app.Path, Running: app.Running, Total: app.Total,
		})
	}
	writeJSON(w, http.StatusOK, response)
}
