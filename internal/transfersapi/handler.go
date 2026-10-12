// Package transfersapi exposes 传输中心: /api/v1/transfers to follow and
// manage uploads and downloads, and /api/v1/uploads, the tus endpoint that
// uploads arrive at (ADR 0019). Like feedbackapi it does not authenticate:
// the Product Service attaches the signed-in user (WithUser) and the session
// token the File Broker needs, and checks CSRF on writes.
package transfersapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	tusd "github.com/tus/tusd/v2/pkg/handler"
	"github.com/tus/tusd/v2/pkg/memorylocker"
	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/transfers"
	xslog "golang.org/x/exp/slog"
)

const (
	TransfersPath = "/api/v1/transfers"
	UploadsPath   = "/api/v1/uploads"
)

// networkTimeout closes an upload connection that sent nothing for this
// long, so a vanished browser frees its upload for another to resume.
const networkTimeout = 60 * time.Second

type userKey struct{}

// WithUser attaches the signed-in user.
func WithUser(ctx context.Context, user accounts.User) context.Context {
	return context.WithValue(ctx, userKey{}, user)
}

func userOf(ctx context.Context) (accounts.User, bool) {
	user, ok := ctx.Value(userKey{}).(accounts.User)
	return user, ok && user.ID != ""
}

type handler struct {
	service *transfers.Service
	uploads http.Handler
	logger  *slog.Logger
	mux     *http.ServeMux
}

// New returns the transfer API. A nil service answers 503
// transfers_unavailable.
func New(service *transfers.Service, logger *slog.Logger) (http.Handler, error) {
	if logger == nil {
		logger = slog.Default()
	}
	h := &handler{service: service, logger: logger, mux: http.NewServeMux()}
	if service != nil {
		composer := tusd.NewStoreComposer()
		store := uploadStore{service: service, logger: logger}
		composer.UseCore(store)
		composer.UseTerminater(store)
		composer.UseLocker(memorylocker.New())
		tus, err := tusd.NewHandler(tusd.Config{
			BasePath: UploadsPath + "/", StoreComposer: composer,
			DisableDownload: true, DisableConcatenation: true,
			// The desktop is same-origin; cross-origin requests never reach here.
			Cors:           &tusd.CorsConfig{Disable: true},
			NetworkTimeout: networkTimeout,
			// tusd reports every request; uploadStore logs what fails.
			Logger: xslog.New(xslog.NewTextHandler(io.Discard, nil)),
		})
		if err != nil {
			return nil, err
		}
		h.uploads = http.StripPrefix(UploadsPath, tus)
	}
	routes := map[string]func(http.ResponseWriter, *http.Request, accounts.User){
		"GET " + TransfersPath:                   h.list,
		"DELETE " + TransfersPath:                h.clear,
		"POST " + TransfersPath + "/downloads":   h.createDownload,
		"GET " + TransfersPath + "/{id}":         h.get,
		"DELETE " + TransfersPath + "/{id}":      h.remove,
		"GET " + TransfersPath + "/{id}/content": h.content,
		"POST " + TransfersPath + "/{id}/pause":  h.pause,
		"POST " + TransfersPath + "/{id}/cancel": h.cancel,
		UploadsPath:                              h.upload,
		UploadsPath + "/":                        h.upload,
	}
	for pattern, route := range routes {
		h.mux.HandleFunc(pattern, h.withUser(route))
	}
	h.mux.HandleFunc(TransfersPath+"/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	})
	return h, nil
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		writeError(w, http.StatusServiceUnavailable, "transfers_unavailable", "transfers are unavailable")
		return
	}
	h.mux.ServeHTTP(w, r)
}

func (h *handler) withUser(next func(http.ResponseWriter, *http.Request, accounts.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := userOf(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "authentication_required", "authentication is required")
			return
		}
		next(w, r, user)
	}
}

func (h *handler) list(w http.ResponseWriter, r *http.Request, user accounts.User) {
	items, err := h.service.List(r.Context(), user)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if items == nil {
		items = []transfers.Transfer{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *handler) get(w http.ResponseWriter, r *http.Request, user accounts.User) {
	t, err := h.service.Get(r.Context(), user, r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *handler) createDownload(w http.ResponseWriter, r *http.Request, user accounts.User) {
	var request struct {
		EntryIDs []string `json:"entryIds"`
	}
	if err := decode(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	t, err := h.service.CreateDownload(r.Context(), user, request.EntryIDs)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

// content sends a download. Once the response has begun, a failure cuts
// the connection so that the browser reports the download failed instead of
// keeping a truncated file.
func (h *handler) content(w http.ResponseWriter, r *http.Request, user accounts.User) {
	started, err := h.service.ServeDownload(w, r, user, r.PathValue("id"))
	if err == nil {
		return
	}
	if started {
		panic(http.ErrAbortHandler)
	}
	h.fail(w, r, err)
}

func (h *handler) pause(w http.ResponseWriter, r *http.Request, user accounts.User) {
	t, err := h.service.Pause(r.Context(), user, r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *handler) cancel(w http.ResponseWriter, r *http.Request, user accounts.User) {
	t, err := h.service.Cancel(r.Context(), user, r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *handler) remove(w http.ResponseWriter, r *http.Request, user accounts.User) {
	if err := h.service.Remove(r.Context(), user, r.PathValue("id")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) clear(w http.ResponseWriter, r *http.Request, user accounts.User) {
	if err := h.service.ClearFinished(r.Context(), user); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) upload(w http.ResponseWriter, r *http.Request, _ accounts.User) {
	h.uploads.ServeHTTP(w, r)
}

// statuses gives each failure code of transfers.FailureOf its HTTP status.
var statuses = map[string]int{
	"forbidden":            http.StatusForbidden,
	"not_found":            http.StatusNotFound,
	"name_conflict":        http.StatusConflict,
	"folder_blocked":       http.StatusConflict,
	"transfer_state":       http.StatusConflict,
	"canceled":             http.StatusGone,
	"interrupted":          http.StatusBadRequest,
	"validation_failed":    http.StatusUnprocessableEntity,
	"volume_unavailable":   http.StatusLocked,
	"too_many_uploads":     http.StatusTooManyRequests,
	"insufficient_storage": http.StatusInsufficientStorage,
}

func statusOf(failure *transfers.Failure) int {
	if status, ok := statuses[failure.Code]; ok {
		return status
	}
	return http.StatusInternalServerError
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	failure := transfers.FailureOf(err)
	status := statusOf(failure)
	if status == http.StatusInternalServerError {
		h.logger.ErrorContext(r.Context(), "transfer request failed", "path", r.URL.Path, "error", err)
	}
	writeError(w, status, failure.Code, failure.Message)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody(code, message))
}

func errorBody(code, message string) map[string]map[string]string {
	return map[string]map[string]string{"error": {"code": code, "message": message}}
}

func decode(r *http.Request, target any) error {
	if mediaType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])); mediaType != "application/json" {
		return errors.New("Content-Type must be application/json")
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}
