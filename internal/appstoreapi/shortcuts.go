package appstoreapi

import (
	"net/http"

	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/appstore"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
)

// ShortcutsPrefix serves the desktop shortcuts of installed apps to every
// signed-in user: an app installed for the household is opened from the
// desktop, not from the administrators' App Center or Docker. It only says
// which apps have a web interface, where it is and whether it runs; how an
// app was installed stays in /api/v1/apps.
const ShortcutsPrefix = "/api/v1/app-shortcuts"

type shortcutsHandler struct {
	store appstore.Store
	mux   *apiroute.Mux
}

// NewShortcuts returns the shortcuts API; with a nil store, as when the App
// Center is disabled, the list is empty.
func NewShortcuts(store appstore.Store) http.Handler {
	h := &shortcutsHandler{store: store, mux: apiroute.NewMux()}
	h.mux.Only(http.MethodGet, ShortcutsPrefix, h.list)
	h.mux.Only(http.MethodGet, ShortcutsPrefix+"/{appID}/icon", withApp(h.icon))
	h.mux.HandleFunc("/", notFound)
	return h
}

// ShortcutEndpoints are the operations the shortcuts API serves under
// ShortcutsPrefix.
func ShortcutEndpoints() []apiroute.Endpoint {
	return NewShortcuts(nil).(*shortcutsHandler).mux.Endpoints()
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
	w.Header().Set("Cache-Control", "no-store")
	h.mux.ServeHTTP(w, r)
}

func (h *shortcutsHandler) icon(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		notFound(w, r)
		return
	}
	icon, contentType, err := h.store.Icon(r.Context(), r.PathValue("appID"))
	if err != nil {
		notFound(w, r)
		return
	}
	writeIcon(w, icon, contentType)
}

// list returns installed apps that have a web interface; installing,
// uninstalling and available apps have no shortcut.
func (h *shortcutsHandler) list(w http.ResponseWriter, r *http.Request) {
	response := shortcutsResponse{Items: []ShortcutDocument{}}
	if h.store == nil {
		httpjson.Write(w, http.StatusOK, response)
		return
	}
	apps, err := h.store.Apps(r.Context())
	if err != nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "apps_unavailable", "the container engine could not list apps")
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
	httpjson.Write(w, http.StatusOK, response)
}
