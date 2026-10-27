// Package appstoreapi exposes the App Center under /api/v1/apps. Installs and
// uninstalls are state-changing and pass localorigin.CheckWrite; an install
// must carry the digest of the plan the owner confirmed. Each app runs as its
// own Linux identity, whose folders the Host Agent prepares before Docker
// starts anything (ADR 0008).
package appstoreapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/appid"
	"github.com/zhongwater123/A-NAS/internal/appstore"
	"github.com/zhongwater123/A-NAS/internal/appstore/agent"
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
	store       appstore.Store
	dataSource  DataSource
	host        appid.Host
	volumeReady func(context.Context) error
	logger      *slog.Logger
	mux         *apiroute.Mux
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
	h := &handler{store: store, dataSource: dataSource, host: options.Host, volumeReady: options.VolumeReady, logger: options.Logger, mux: apiroute.NewMux()}
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

// identity asks the Host Agent for the app's Linux identity.
func (h *handler) identity(ctx context.Context, id string) (appstore.Identity, error) {
	if h.host == nil {
		return appstore.Identity{}, fmt.Errorf("%w: no host for app identities", appstore.ErrUnavailable)
	}
	identity, err := h.host.AppIdentity(ctx, id)
	if err != nil {
		return appstore.Identity{}, fmt.Errorf("%w: %v", appstore.ErrUnavailable, err)
	}
	return identity, nil
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
	apps, err := h.store.Apps(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, appsResponse{DataSource: h.dataSource, AppsDocument: agent.AppsFromDomain(apps)})
}

func (h *handler) icon(w http.ResponseWriter, r *http.Request) {
	if !h.enabled(w) {
		return
	}
	icon, contentType, err := h.store.Icon(r.Context(), r.PathValue("appID"))
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
	id := r.PathValue("appID")
	identity, err := h.identity(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	plan, err := h.store.Plan(r.Context(), id, identity)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, agent.PlanFromDomain(plan))
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
	var request agent.InstallRequest
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
		job, err = h.install(r.Context(), id, request.Digest)
	} else {
		if job, err = h.store.Uninstall(r.Context(), id); err == nil && h.host != nil {
			// The identity and its data stay for a reinstall.
			if releaseErr := h.host.ReleaseApp(r.Context(), id); releaseErr != nil {
				h.logger.ErrorContext(r.Context(), "releasing app access failed", "app", id, "error", releaseErr)
			}
		}
	}
	if errors.Is(err, errVolumeUnavailable) {
		httpjson.WriteError(w, http.StatusLocked, "volume_unavailable", "the data volume is not available")
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.logger.InfoContext(r.Context(), "app job requested", "app", id, "action", job.Action)
	httpjson.Write(w, http.StatusAccepted, agent.JobFromDomain(job))
}

var errVolumeUnavailable = errors.New("data volume is unavailable")

// install checks the confirmed plan against the app's identity, has the Host
// Agent prepare its folders, and only then starts Compose.
func (h *handler) install(ctx context.Context, id, digest string) (appstore.Job, error) {
	if h.volumeReady != nil {
		if err := h.volumeReady(ctx); err != nil {
			return appstore.Job{}, errVolumeUnavailable
		}
	}
	identity, err := h.identity(ctx, id)
	if err != nil {
		return appstore.Job{}, err
	}
	plan, err := h.store.Plan(ctx, id, identity)
	if err != nil {
		return appstore.Job{}, err
	}
	if plan.Digest != digest {
		return appstore.Job{}, appstore.ErrPlanChanged
	}
	if err := h.host.PrepareApp(ctx, id, plan.HostFolders(), plan.SharesFolders()); err != nil {
		return appstore.Job{}, fmt.Errorf("%w: prepare app folders: %v", appstore.ErrUnavailable, err)
	}
	return h.store.Install(ctx, id, digest, identity)
}

func (h *handler) enabled(w http.ResponseWriter) bool {
	if h.store != nil {
		return true
	}
	httpjson.WriteError(w, http.StatusServiceUnavailable, "apps_disabled", "the App Center is disabled")
	return false
}

// fail keeps conflict details (which port, which container) but hides engine internals.
func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	status, code := agent.StatusForError(err)
	message := err.Error()
	if status >= 500 {
		h.logger.ErrorContext(r.Context(), "app request failed", "path", r.URL.Path, "error", err)
		message = "the container engine could not complete the request"
	}
	if _, ok := errors.AsType[*appstore.PolicyError](err); ok {
		message = "this app's manifest does not meet the install policy"
	}
	httpjson.WriteError(w, status, code, message)
}

type appsResponse struct {
	DataSource DataSource `json:"dataSource"`
	agent.AppsDocument
}

// DevelopmentHost stands in for the Host Agent with the fake app store: each
// app gets a stable identity in the app range and nothing is created.
type DevelopmentHost struct{}

func (DevelopmentHost) AppIdentity(_ context.Context, id string) (appstore.Identity, error) {
	sum := 0
	for _, character := range id {
		sum = (sum*31 + int(character)) % (appid.LastUID - appid.FirstUID + 1)
	}
	uid := appid.FirstUID + sum
	return appstore.Identity{Username: appid.Username(id), UID: uid, GID: uid}, nil
}

func (DevelopmentHost) PrepareApp(context.Context, string, []string, bool) error { return nil }
func (DevelopmentHost) ReleaseApp(context.Context, string) error                 { return nil }
