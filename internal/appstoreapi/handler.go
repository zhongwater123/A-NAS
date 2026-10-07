// Package appstoreapi exposes the App Center under /api/v1/apps. Installs and
// uninstalls are state-changing and pass localorigin.CheckWrite; an install
// must carry the digest of the plan the owner confirmed. Each app runs as its
// own Linux identity, whose folders the Host Agent prepares before Docker
// starts anything (ADR 0008).
package appstoreapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/appid"
	"github.com/zhongwater123/A-NAS/internal/appstore"
	"github.com/zhongwater123/A-NAS/internal/appstore/agent"
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
	return &handler{store: store, dataSource: dataSource, host: options.Host, volumeReady: options.VolumeReady, logger: options.Logger}
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

func Matches(path string) bool {
	return path == PathPrefix || strings.HasPrefix(path, PathPrefix+"/")
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == PathPrefix {
		if !allow(w, r, http.MethodGet) || !h.enabled(w) {
			return
		}
		apps, err := h.store.Apps(r.Context())
		if err != nil {
			h.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, appsResponse{DataSource: h.dataSource, AppsDocument: agent.AppsFromDomain(apps)})
		return
	}

	id, operation, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, PathPrefix+"/"), "/")
	if !ok || !appstore.ValidID(id) || strings.Contains(operation, "/") {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	switch operation {
	case "icon":
		if !allow(w, r, http.MethodGet) || !h.enabled(w) {
			return
		}
		icon, contentType, err := h.store.Icon(r.Context(), id)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		// Icons are fixed for a given catalog; SVGs must not run scripts.
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(icon)
	case "plan":
		if !allow(w, r, http.MethodGet) || !h.enabled(w) {
			return
		}
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
		writeJSON(w, http.StatusOK, agent.PlanFromDomain(plan))
	case "install", "uninstall":
		if !allow(w, r, http.MethodPost) {
			return
		}
		if err := localorigin.CheckWrite(r); err != nil {
			writeError(w, http.StatusForbidden, "forbidden", "app changes must come from the local desktop")
			return
		}
		if !h.enabled(w) {
			return
		}
		var request agent.InstallRequest
		decoder := json.NewDecoder(io.LimitReader(r.Body, 1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil || (operation == "install" && len(request.Digest) != 64) {
			writeError(w, http.StatusBadRequest, "invalid_request", "install requires the confirmed plan digest")
			return
		}
		var job appstore.Job
		var err error
		if operation == "install" {
			job, err = h.install(r.Context(), id, request.Digest)
		} else {
			if job, err = h.store.Uninstall(r.Context(), id); err == nil && h.host != nil {
				// Withdraw Shared access at once; the identity and its data stay.
				if releaseErr := h.host.ReleaseApp(r.Context(), id); releaseErr != nil {
					h.logger.ErrorContext(r.Context(), "releasing app access failed", "app", id, "error", releaseErr)
				}
			}
		}
		if errors.Is(err, errVolumeUnavailable) {
			writeError(w, http.StatusLocked, "volume_unavailable", "the data volume is not available")
			return
		}
		if err != nil {
			h.fail(w, r, err)
			return
		}
		h.logger.InfoContext(r.Context(), "app job requested", "app", id, "action", job.Action)
		writeJSON(w, http.StatusAccepted, agent.JobFromDomain(job))
	default:
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	}
}

var errVolumeUnavailable = errors.New("data volume is unavailable")

// install checks the confirmed plan against the app's identity, has the Host
// Agent prepare its folders and Shared access, and only then starts Compose.
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
	writeError(w, http.StatusServiceUnavailable, "apps_disabled", "the App Center is disabled")
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
	var policy *appstore.PolicyError
	if errors.As(err, &policy) {
		message = "this app's manifest does not meet the install policy"
	}
	writeError(w, status, code, message)
}

type appsResponse struct {
	DataSource DataSource `json:"dataSource"`
	agent.AppsDocument
}

func allow(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	return false
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorResponse struct {
	Error errorDetail `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Error: errorDetail{Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		panic(err)
	}
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
