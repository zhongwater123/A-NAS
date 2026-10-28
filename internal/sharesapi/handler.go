// Package sharesapi exposes member shares to authenticated callers. The outer
// Product Service checks CSRF; production shareservice independently resolves
// the original session and replaces the request user before entering this API.
package sharesapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/mediaworker"
	"github.com/zhongwater123/A-NAS/internal/shares"
)

const PathPrefix = "/api/v1/shares"

type handler struct {
	store   *shares.Store
	logger  *slog.Logger
	members func(context.Context) ([]accounts.Profile, error)
	mux     *apiroute.Mux
	// jobMux serves the save jobs, whose paths ServeMux cannot hold beside
	// the paths of one share: GET /jobs/messages would match both
	// /jobs/{jobID} and /{shareID}/messages (the resource IDs never equal
	// "jobs").
	jobMux       *apiroute.Mux
	media        mediaworker.Worker
	previewSlots chan struct{}
}

func New(store *shares.Store, logger *slog.Logger, members func(context.Context) ([]accounts.Profile, error)) http.Handler {
	return NewWithPreview(store, logger, members, nil)
}
func NewWithPreview(store *shares.Store, logger *slog.Logger, members func(context.Context) ([]accounts.Profile, error), media mediaworker.Worker) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &handler{store: store, logger: logger, members: members, mux: apiroute.NewMux(), jobMux: apiroute.NewMux(), media: media, previewSlots: make(chan struct{}, 2)}
	h.routes()
	return h
}

// Endpoints are the operations the share API serves under PathPrefix.
func Endpoints() []apiroute.Endpoint {
	h := New(nil, nil, nil).(*handler)
	return append(h.mux.Endpoints(), h.jobMux.Endpoints()...)
}
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, ok := accounts.UserFrom(r.Context()); !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", "authentication is required")
		return
	}
	if h.store == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "shares_unavailable", "member shares are not available")
		return
	}
	if strings.HasPrefix(r.URL.Path, PathPrefix+"/jobs/") {
		h.jobMux.ServeHTTP(w, r)
		return
	}
	h.mux.ServeHTTP(w, r)
}
func actor(r *http.Request) shares.User {
	u, _ := accounts.UserFrom(r.Context())
	return shares.User{ID: u.ID, Username: u.Username}
}
func (h *handler) routes() {
	p := PathPrefix
	h.mux.HandleFunc("GET "+p, h.list)
	h.mux.HandleFunc("POST "+p, h.create)
	h.mux.HandleFunc("GET "+p+"/members", h.listMembers)
	h.mux.HandleFunc("GET "+p+"/notifications", h.notifications)
	h.mux.HandleFunc("GET "+p+"/{shareID}", h.get)
	h.mux.HandleFunc("POST "+p+"/{shareID}/view", h.view)
	h.mux.HandleFunc("GET "+p+"/{shareID}/messages", h.messages)
	h.mux.HandleFunc("POST "+p+"/{shareID}/messages", h.message)
	h.mux.HandleFunc("POST "+p+"/{shareID}/read", h.read)
	h.mux.HandleFunc("POST "+p+"/{shareID}/save", h.save)
	h.mux.HandleFunc("GET "+p+"/{shareID}/jobs", h.jobs)
	h.mux.HandleFunc("POST "+p+"/{shareID}/revoke", h.revoke)
	h.mux.HandleFunc("GET "+p+"/{shareID}/entries/{entryID}/content", h.content)
	h.mux.HandleFunc("GET "+p+"/{shareID}/entries/{entryID}/preview", h.preview)
	h.jobMux.HandleFunc("GET "+p+"/jobs/{jobID}", h.job)
	notFound := func(w http.ResponseWriter, _ *http.Request) {
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
	}
	h.mux.HandleFunc("/", notFound)
	h.jobMux.HandleFunc("/", notFound)
}
func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, shares.ErrNotFound):
		httpjson.WriteError(w, http.StatusNotFound, "share_not_found", "share not found")
	case errors.Is(err, shares.ErrUnauthorized):
		httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", "session or account unavailable")
	case errors.Is(err, shares.ErrRecipientInactive):
		httpjson.WriteError(w, http.StatusConflict, "share_recipient_unavailable", "the recipient account is unavailable")
	case errors.Is(err, shares.ErrInactive):
		httpjson.WriteError(w, http.StatusGone, "share_inactive", "share has expired or was withdrawn")
	case errors.Is(err, shares.ErrInvalid):
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_share_request", "invalid share request")
	case errors.Is(err, shares.ErrConflict):
		httpjson.WriteError(w, http.StatusConflict, "share_request_conflict", "request key was used for different content")
	case errors.Is(err, shares.ErrBusy):
		httpjson.WriteError(w, http.StatusTooManyRequests, "share_busy", "too many active share operations; retry later")
	default:
		h.logger.WarnContext(r.Context(), "share operation failed", "error", err)
		httpjson.WriteError(w, http.StatusServiceUnavailable, "shares_unavailable", "share storage or file operation unavailable")
	}
}
func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	if err := httpjson.Decode(w, r, 256<<10, value); err != nil {
		httpjson.WriteDecodeError(w, err, "invalid_share_request", "invalid request body")
		return false
	}
	return true
}
func number(r *http.Request, key string, def int64) (int64, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, shares.ErrInvalid
	}
	return n, nil
}
func paging(r *http.Request) (int64, int, error) {
	after, err := number(r, "after", 0)
	if err != nil {
		return 0, 0, err
	}
	limit, err := number(r, "limit", 100)
	if err != nil || limit < 1 || limit > 100 {
		return 0, 0, shares.ErrInvalid
	}
	return after, int(limit), nil
}
func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var req shares.CreateRequest
	if !decode(w, r, &req) {
		return
	}
	v, err := h.store.Create(r.Context(), actor(r), req)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusAccepted, v)
}
func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	direction := r.URL.Query().Get("direction")
	if direction == "" {
		direction = "received"
	}
	_, limit, err := paging(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	offset, err := number(r, "offset", 0)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items, err := h.store.List(r.Context(), actor(r), direction)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if offset > int64(len(items)) {
		offset = int64(len(items))
	}
	end := int(offset) + limit
	response := map[string]any{}
	if end < len(items) {
		response["nextOffset"] = end
	} else {
		end = len(items)
	}
	response["items"] = items[int(offset):end]
	httpjson.Write(w, http.StatusOK, response)
}
func (h *handler) listMembers(w http.ResponseWriter, r *http.Request) {
	if h.members == nil {
		h.fail(w, r, errors.New("member directory unavailable"))
		return
	}
	items, err := h.members(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := []accounts.Profile{}
	for _, p := range items {
		if p.ID != actor(r).ID {
			out = append(out, p)
		}
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": out})
}
func (h *handler) get(w http.ResponseWriter, r *http.Request)  { h.detail(w, r, false) }
func (h *handler) view(w http.ResponseWriter, r *http.Request) { h.detail(w, r, true) }
func (h *handler) detail(w http.ResponseWriter, r *http.Request, viewed bool) {
	v, err := h.store.Get(r.Context(), actor(r), r.PathValue("shareID"), viewed)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, v)
}
func (h *handler) messages(w http.ResponseWriter, r *http.Request) {
	after, limit, err := paging(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items, err := h.store.Messages(r.Context(), actor(r), r.PathValue("shareID"), r.URL.Query().Get("recipientId"), after, limit)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": items})
}
func (h *handler) message(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RecipientID string `json:"recipientId"`
		RequestID   string `json:"requestId"`
		Text        string `json:"text"`
	}
	if !decode(w, r, &req) {
		return
	}
	v, err := h.store.SendMessage(r.Context(), actor(r), r.PathValue("shareID"), req.RecipientID, req.RequestID, req.Text)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusCreated, v)
}
func (h *handler) read(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RecipientID string `json:"recipientId"`
		Through     int64  `json:"through"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := h.store.MarkRead(r.Context(), actor(r), r.PathValue("shareID"), req.RecipientID, req.Through); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *handler) save(w http.ResponseWriter, r *http.Request) {
	var req shares.SaveRequest
	if !decode(w, r, &req) {
		return
	}
	v, err := h.store.Save(r.Context(), actor(r), r.PathValue("shareID"), req)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusAccepted, v)
}
func (h *handler) job(w http.ResponseWriter, r *http.Request) {
	v, err := h.store.Job(r.Context(), actor(r), r.PathValue("jobID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, v)
}
func (h *handler) jobs(w http.ResponseWriter, r *http.Request) {
	items, err := h.store.Jobs(r.Context(), actor(r), r.PathValue("shareID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": items})
}
func (h *handler) notifications(w http.ResponseWriter, r *http.Request) {
	after, limit, err := paging(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items, err := h.store.Notifications(r.Context(), actor(r), after, limit)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": items})
}
func (h *handler) revoke(w http.ResponseWriter, r *http.Request) {
	if err := h.store.Revoke(r.Context(), actor(r), r.PathValue("shareID")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *handler) content(w http.ResponseWriter, r *http.Request) {
	reader, entry, err := h.store.OpenContent(r.Context(), actor(r), r.PathValue("shareID"), r.PathValue("entryID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	defer reader.Close()
	seeker, ok := reader.(io.ReadSeeker)
	if !ok {
		h.fail(w, r, errors.New("content seeking unavailable"))
		return
	}
	name := path.Base(entry.Path)
	typ := mime.TypeByExtension(strings.ToLower(path.Ext(name)))
	inline := strings.HasPrefix(typ, "image/") && !strings.HasPrefix(typ, "image/svg") || strings.HasPrefix(typ, "audio/") || strings.HasPrefix(typ, "video/") || typ == "application/pdf" || strings.HasPrefix(typ, "text/plain")
	disposition := "attachment"
	if inline {
		disposition = "inline"
	}
	if typ == "" {
		typ = "application/octet-stream"
	}
	w.Header().Set("Content-Type", typ)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	http.ServeContent(w, r, name, time.Time{}, seeker)
}
