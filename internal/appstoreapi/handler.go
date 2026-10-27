// Package appstoreapi exposes the App Center under /api/v1/apps. Installs and
// uninstalls are state-changing and pass localorigin.CheckWrite; an install
// must carry the digest of the plan the owner confirmed. What an install
// does is appstore.Center's; this package maps it to the public documents
// (documents.go), which do not change with the container agent's protocol
// (#222).
package appstoreapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/appid"
	"github.com/zhongwater123/A-NAS/internal/appstore"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/localorigin"
)

const PathPrefix = "/api/v1/apps"

type DataSource string

const (
	DataSourceSimulated DataSource = "simulated"
	DataSourceLive      DataSource = "live"
)

type handler struct {
	// center is nil when the App Center is disabled.
	center     *appstore.Center
	dataSource DataSource
	logger     *slog.Logger
	mux        *apiroute.Mux
}

type Options struct {
	// Host allocates app identities and prepares their folders.
	Host appid.Host
	// VolumeReady fails while the data volume is unavailable; installs are
	// refused then. Nil skips the check on development volumes.
	VolumeReady func(context.Context) error
	Logger      *slog.Logger
}

// New returns the App Center API; a nil store disables it.
func New(store appstore.Store, dataSource DataSource, options Options) http.Handler {
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	h := &handler{dataSource: dataSource, logger: options.Logger, mux: apiroute.NewMux()}
	if store != nil {
		h.center = appstore.NewCenter(store, options.Host, options.VolumeReady, options.Logger)
	}
	h.mux.Only(http.MethodGet, PathPrefix, h.list)
	h.mux.Only(http.MethodGet, PathPrefix+"/{appID}/icon", withApp(h.icon))
	h.mux.Only(http.MethodGet, PathPrefix+"/{appID}/plan", withApp(h.plan))
	h.mux.Only(http.MethodPost, PathPrefix+"/{appID}/install", withApp(h.change))
	h.mux.Only(http.MethodPost, PathPrefix+"/{appID}/uninstall", withApp(h.change))
	h.mux.HandleFunc("/", notFound)
	return h
}

// Endpoints are the operations the App Center API serves under PathPrefix.
func Endpoints() []apiroute.Endpoint {
	return New(nil, "", Options{}).(*handler).mux.Endpoints()
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	h.mux.ServeHTTP(w, r)
}

// withApp answers not_found for a path whose app ID no catalog app can have.
func withApp(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !appstore.ValidID(r.PathValue("appID")) {
			notFound(w, r)
			return
		}
		next(w, r)
	}
}

func notFound(w http.ResponseWriter, _ *http.Request) {
	httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	if !h.enabled(w) {
		return
	}
	apps, err := h.center.Apps(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, appsResponse{DataSource: h.dataSource, Apps: appsDocument(apps)})
}

func (h *handler) icon(w http.ResponseWriter, r *http.Request) {
	if !h.enabled(w) {
		return
	}
	icon, contentType, err := h.center.Icon(r.Context(), r.PathValue("appID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeIcon(w, icon, contentType)
}

// writeIcon sends a catalog icon, which is fixed for a release; SVGs must
// not run scripts.
func writeIcon(w http.ResponseWriter, icon []byte, contentType string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(icon)
}

func (h *handler) plan(w http.ResponseWriter, r *http.Request) {
	if !h.enabled(w) {
		return
	}
	plan, err := h.center.Plan(r.Context(), r.PathValue("appID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, planDocument(plan))
}

// change installs or uninstalls an app, as the last path segment says.
func (h *handler) change(w http.ResponseWriter, r *http.Request) {
	if err := localorigin.CheckWrite(r); err != nil {
		httpjson.WriteError(w, http.StatusForbidden, "forbidden", "app changes must come from the local desktop")
		return
	}
	if !h.enabled(w) {
		return
	}
	var request installRequest
	if err := httpjson.Decode(w, r, 1024, &request); err != nil {
		httpjson.WriteDecodeError(w, err, "invalid_request", "install requires the confirmed plan digest")
		return
	}
	id, install := r.PathValue("appID"), strings.HasSuffix(r.URL.Path, "/install")
	if install && len(request.Digest) != 64 {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "install requires the confirmed plan digest")
		return
	}
	var job appstore.Job
	var err error
	if install {
		job, err = h.center.Install(r.Context(), id, request.Digest)
	} else {
		job, err = h.center.Uninstall(r.Context(), id)
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.logger.InfoContext(r.Context(), "app job requested", "app", id, "action", job.Action)
	httpjson.Write(w, http.StatusAccepted, jobDocument(job))
}

func (h *handler) enabled(w http.ResponseWriter) bool {
	if h.center != nil {
		return true
	}
	httpjson.WriteError(w, http.StatusServiceUnavailable, "apps_disabled", "the App Center is disabled")
	return false
}

// errorCodes are the App Center's answers to the store's errors.
var errorCodes = []struct {
	err    error
	status int
	code   string
}{
	{appstore.ErrVolumeUnavailable, http.StatusLocked, "volume_unavailable"},
	{appstore.ErrNotFound, http.StatusNotFound, "app_not_found"},
	{appstore.ErrPlanChanged, http.StatusConflict, "plan_changed"},
	{appstore.ErrBusy, http.StatusConflict, "app_busy"},
	{appstore.ErrAlreadyInstalled, http.StatusConflict, "already_installed"},
	{appstore.ErrNotInstalled, http.StatusConflict, "not_installed"},
	{appstore.ErrPortInUse, http.StatusConflict, "port_in_use"},
	{appstore.ErrNameInUse, http.StatusConflict, "name_in_use"},
	{appstore.ErrAddressPoolMissing, http.StatusConflict, "address_pool_missing"},
	{appstore.ErrRuntimeOutdated, http.StatusConflict, "docker_runtime_outdated"},
	{appstore.ErrUnavailable, http.StatusServiceUnavailable, "apps_unavailable"},
}

// fail keeps conflict details (which port, which container) but hides engine
// internals.
func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := http.StatusBadGateway, "app_engine_error", err.Error()
	for _, mapping := range errorCodes {
		if errors.Is(err, mapping.err) {
			status, code = mapping.status, mapping.code
			break
		}
	}
	if _, ok := errors.AsType[*appstore.PolicyError](err); ok {
		status, code, message = http.StatusUnprocessableEntity, "policy_violation", "this app's manifest does not meet the install policy"
	}
	switch {
	case code == "volume_unavailable":
		message = "the data volume is not available"
	case status >= 500:
		h.logger.ErrorContext(r.Context(), "app request failed", "path", r.URL.Path, "error", err)
		message = "the container engine could not complete the request"
	}
	httpjson.WriteError(w, status, code, message)
}
