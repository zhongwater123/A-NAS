// Package feedbackapi exposes 问题反馈 under /api/v1/feedback. It does not
// authenticate: the Product Service wraps it so that every request carries
// the signed-in user (accounts.WithUser) and writes have passed CSRF checks. Every
// signed-in account may read and change every item and every comment.
package feedbackapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/feedback"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
)

const PathPrefix = "/api/v1/feedback"

// maxRequestBytes admits the most images an item can hold, plus its text.
const maxRequestBytes = feedback.MaxImages*feedback.MaxImageBytes + 1<<20

// maxResolveBytes bounds the JSON that marks an item resolved.
const maxResolveBytes = 1 << 10

type handler struct {
	store  *feedback.Store
	logger *slog.Logger
	mux    *apiroute.Mux
}

// New returns the feedback API. A nil store answers 503 feedback_unavailable.
func New(store *feedback.Store, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &handler{store: store, logger: logger, mux: apiroute.NewMux()}
	routes := map[string]func(http.ResponseWriter, *http.Request, feedback.Actor){
		"GET ":                              h.list,
		"POST ":                             h.create,
		"PUT /{id}":                         h.edit,
		"PATCH /{id}":                       h.resolve,
		"DELETE /{id}":                      h.delete,
		"GET /{id}/images/{imageID}":        h.image,
		"POST /{id}/comments":               h.addComment,
		"PUT /{id}/comments/{commentID}":    h.editComment,
		"DELETE /{id}/comments/{commentID}": h.deleteComment,
	}
	for pattern, route := range routes {
		method, path, _ := strings.Cut(pattern, " ")
		h.mux.HandleFunc(method+" "+PathPrefix+path, h.withUser(route))
	}
	h.mux.HandleFunc(PathPrefix+"/", func(w http.ResponseWriter, _ *http.Request) {
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
	})
	return h
}

// Endpoints are the operations the feedback API serves under PathPrefix.
func Endpoints() []apiroute.Endpoint {
	return New(nil, nil).(*handler).mux.Endpoints()
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "feedback_unavailable", "feedback is unavailable")
		return
	}
	h.mux.ServeHTTP(w, r)
}

func (h *handler) withUser(next func(http.ResponseWriter, *http.Request, feedback.Actor)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := accounts.UserFrom(r.Context())
		if !ok || user.ID == "" {
			httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", "authentication is required")
			return
		}
		next(w, r, feedback.Actor{ID: user.ID, Username: user.Username})
	}
}

func (h *handler) list(w http.ResponseWriter, r *http.Request, _ feedback.Actor) {
	items, err := h.store.List(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": items})
}

func (h *handler) create(w http.ResponseWriter, r *http.Request, actor feedback.Actor) {
	form, err := readForm(w, r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if form.versioned || len(form.content.Keep) > 0 {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "a new item has no version or kept images")
		return
	}
	item, err := h.store.Create(r.Context(), actor, form.content)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusCreated, item)
}

func (h *handler) edit(w http.ResponseWriter, r *http.Request, actor feedback.Actor) {
	form, err := readForm(w, r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if !form.versioned {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "an edit names the version it was made from")
		return
	}
	item, err := h.store.Edit(r.Context(), actor, r.PathValue("id"), form.version, form.content)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, item)
}

func (h *handler) resolve(w http.ResponseWriter, r *http.Request, actor feedback.Actor) {
	var request struct {
		Resolved *bool `json:"resolved"`
	}
	if !httpjson.Read(w, r, maxResolveBytes, &request) {
		return
	}
	if request.Resolved == nil {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	item, err := h.store.SetResolved(r.Context(), actor, r.PathValue("id"), *request.Resolved)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, item)
}

// Comment writes answer with the whole item, so the list replaces it in place.
func (h *handler) addComment(w http.ResponseWriter, r *http.Request, actor feedback.Actor) {
	form, err := readForm(w, r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if form.versioned || len(form.content.Keep) > 0 {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "a new comment has no version or kept images")
		return
	}
	item, err := h.store.AddComment(r.Context(), actor, r.PathValue("id"), form.content)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusCreated, item)
}

func (h *handler) editComment(w http.ResponseWriter, r *http.Request, actor feedback.Actor) {
	form, err := readForm(w, r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if !form.versioned {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "an edit names the version it was made from")
		return
	}
	item, err := h.store.EditComment(r.Context(), actor, r.PathValue("id"), r.PathValue("commentID"), form.version, form.content)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, item)
}

func (h *handler) deleteComment(w http.ResponseWriter, r *http.Request, _ feedback.Actor) {
	item, err := h.store.DeleteComment(r.Context(), r.PathValue("id"), r.PathValue("commentID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, item)
}

func (h *handler) delete(w http.ResponseWriter, r *http.Request, _ feedback.Actor) {
	if err := h.store.Delete(r.Context(), r.PathValue("id")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) image(w http.ResponseWriter, r *http.Request, _ feedback.Actor) {
	image, data, err := h.store.ImageContent(r.Context(), r.PathValue("id"), r.PathValue("imageID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	// An image ID never changes content, and the sniffed type is one that
	// browsers only ever render as a picture.
	w.Header().Set("Content-Type", image.ContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

type form struct {
	content   feedback.Content
	version   int64
	versioned bool
}

var errRequestTooLarge = errors.New("request is too large")

// readForm reads a multipart form of one text part, an optional version,
// keep parts naming attached images that stay, and image parts.
func readForm(w http.ResponseWriter, r *http.Request) (form, error) {
	var result form
	if mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType != "multipart/form-data" {
		return result, errInvalid
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	parts, err := r.MultipartReader()
	if err != nil {
		return result, errInvalid
	}
	for {
		part, err := parts.NextPart()
		if errors.Is(err, io.EOF) {
			return result, nil
		}
		if err != nil {
			return result, bodyError(err)
		}
		switch part.FormName() {
		case "text":
			value, err := readAll(part, 64<<10)
			if err != nil {
				return result, err
			}
			result.content.Text = string(value)
		case "version":
			value, err := readAll(part, 32)
			if err != nil {
				return result, err
			}
			if result.version, err = strconv.ParseInt(string(value), 10, 64); err != nil || result.version < 1 {
				return result, errInvalid
			}
			result.versioned = true
		case "keep":
			value, err := readAll(part, 128)
			if err != nil {
				return result, err
			}
			result.content.Keep = append(result.content.Keep, string(value))
		case "image":
			if len(result.content.New) == feedback.MaxImages {
				return result, feedback.ErrTooManyImages
			}
			value, err := readAll(part, feedback.MaxImageBytes)
			if errors.Is(err, errPartTooLarge) {
				return result, feedback.ErrImageTooLarge
			}
			if err != nil {
				return result, err
			}
			result.content.New = append(result.content.New, value)
		default:
			return result, errInvalid
		}
	}
}

var (
	errInvalid      = errors.New("request body is invalid")
	errPartTooLarge = errors.New("form part is too large")
)

func readAll(part io.Reader, limit int64) ([]byte, error) {
	value, err := io.ReadAll(io.LimitReader(part, limit+1))
	if err != nil {
		return nil, bodyError(err)
	}
	if int64(len(value)) > limit {
		return nil, errPartTooLarge
	}
	return value, nil
}

func bodyError(err error) error {
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		return errRequestTooLarge
	}
	return errInvalid
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, feedback.ErrNotFound):
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "feedback not found")
	case errors.Is(err, feedback.ErrCommentNotFound):
		httpjson.WriteError(w, http.StatusNotFound, "comment_not_found", "comment not found")
	case errors.Is(err, feedback.ErrChanged):
		httpjson.WriteError(w, http.StatusConflict, "feedback_changed", err.Error())
	case errors.Is(err, feedback.ErrCommentChanged):
		httpjson.WriteError(w, http.StatusConflict, "comment_changed", err.Error())
	case errors.Is(err, feedback.ErrEmpty):
		httpjson.WriteError(w, http.StatusBadRequest, "feedback_empty", err.Error())
	case errors.Is(err, feedback.ErrTextTooLong):
		httpjson.WriteError(w, http.StatusBadRequest, "text_too_long", err.Error())
	case errors.Is(err, feedback.ErrTooManyImages):
		httpjson.WriteError(w, http.StatusBadRequest, "too_many_images", err.Error())
	case errors.Is(err, feedback.ErrImageTooLarge), errors.Is(err, errRequestTooLarge):
		httpjson.WriteError(w, http.StatusRequestEntityTooLarge, "image_too_large", feedback.ErrImageTooLarge.Error())
	case errors.Is(err, feedback.ErrUnsupportedImage):
		httpjson.WriteError(w, http.StatusUnsupportedMediaType, "unsupported_image", err.Error())
	case errors.Is(err, errInvalid), errors.Is(err, errPartTooLarge):
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
	case errors.Is(err, context.Canceled):
	default:
		h.logger.ErrorContext(r.Context(), "feedback request failed", "path", r.URL.Path, "error", err)
		httpjson.WriteError(w, http.StatusInternalServerError, "internal_error", "the request failed")
	}
}
