// Package voiceapi exposes 语音处理 under /api/v1/voice. It does not
// authenticate: the Product Service wraps it so that every request carries
// the signed-in user (WithUser) and the session token the File Broker needs,
// and so that writes have passed CSRF checks. Every member reaches only
// their own recordings and people; the recognition service's credentials
// are administrators' alone.
package voiceapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/mediaworker"
	"github.com/zhongwater123/A-NAS/internal/voice"
)

const PathPrefix = "/api/v1/voice"

type userKey struct{}

// WithUser attaches the signed-in user.
func WithUser(ctx context.Context, user accounts.User) context.Context {
	return context.WithValue(ctx, userKey{}, user)
}

type handler struct {
	service *voice.Service
	logger  *slog.Logger
	mux     *http.ServeMux
}

// New returns the voice API. A nil service answers 503 voice_unavailable.
func New(service *voice.Service, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &handler{service: service, logger: logger, mux: http.NewServeMux()}
	routes := map[string]func(http.ResponseWriter, *http.Request, accounts.User){
		"GET /status":                                     h.status,
		"GET /settings/recognizer":                        h.recognizerSettings,
		"PUT /settings/recognizer":                        h.setRecognizer,
		"DELETE /settings/recognizer":                     h.clearRecognizer,
		"POST /settings/recognizer/test":                  h.testRecognizer,
		"GET /recordings":                                 h.list,
		"POST /recordings":                                h.importRecording,
		"GET /recordings/{id}":                            h.detail,
		"PATCH /recordings/{id}":                          h.rename,
		"DELETE /recordings/{id}":                         h.deleteRecording,
		"POST /recordings/{id}/retry":                     h.retry,
		"GET /recordings/{id}/audio":                      h.audio,
		"PATCH /recordings/{id}/utterances/{seq}":         h.editUtterance,
		"PUT /recordings/{id}/speakers/{key}":             h.assignSpeaker,
		"POST /recordings/{id}/speakers/{key}/reject":     h.rejectPerson,
		"POST /recordings/{id}/speakers/{key}/merge":      h.mergeSpeakers,
		"POST /recordings/{id}/translations":              h.requestTranslation,
		"DELETE /recordings/{id}/translations/{language}": h.deleteTranslation,
		"POST /recordings/{id}/summaries":                 h.requestSummary,
		"DELETE /recordings/{id}/summaries/{template}":    h.deleteSummary,
		"GET /people":                                     h.people,
		"PATCH /people/{id}":                              h.renamePerson,
		"DELETE /people/{id}":                             h.deletePerson,
		"POST /people/{id}/merge":                         h.mergePeople,
	}
	for pattern, route := range routes {
		method, rest, _ := strings.Cut(pattern, " ")
		h.mux.HandleFunc(method+" "+PathPrefix+rest, h.withUser(route))
	}
	h.mux.HandleFunc(PathPrefix+"/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	})
	return h
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		writeError(w, http.StatusServiceUnavailable, "voice_unavailable", "voice processing is unavailable")
		return
	}
	h.mux.ServeHTTP(w, r)
}

func (h *handler) withUser(next func(http.ResponseWriter, *http.Request, accounts.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := r.Context().Value(userKey{}).(accounts.User)
		if !ok || user.ID == "" {
			writeError(w, http.StatusUnauthorized, "authentication_required", "authentication is required")
			return
		}
		next(w, r, user)
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func decode(r *http.Request, target any) error {
	if mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType != "application/json" {
		return errors.New("Content-Type must be application/json")
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, voice.ErrNotFound), errors.Is(err, files.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, voice.ErrForbidden), errors.Is(err, files.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "you cannot do this")
	case errors.Is(err, voice.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_request", strings.TrimPrefix(err.Error(), voice.ErrInvalid.Error()+": "))
	case errors.Is(err, voice.ErrNotReady):
		writeError(w, http.StatusConflict, "not_ready", "the recording has no transcript yet")
	case errors.Is(err, voice.ErrDuplicate):
		writeError(w, http.StatusConflict, "already_imported", "the file is already in 语音处理")
	case errors.Is(err, voice.ErrNameTaken):
		writeError(w, http.StatusConflict, "name_taken", "another person has this name")
	case errors.Is(err, voice.ErrNotAudio):
		writeError(w, http.StatusUnprocessableEntity, "not_audio", "the file holds no readable audio")
	case errors.Is(err, voice.ErrTooLong):
		writeError(w, http.StatusUnprocessableEntity, "too_long", "the recording lasts longer than 5 hours")
	case errors.Is(err, voice.ErrUnavailable), errors.Is(err, mediaworker.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, "voice_unavailable", "a part of voice processing is unavailable")
	case errors.Is(err, files.ErrVolumeUnavailable):
		writeError(w, http.StatusServiceUnavailable, "volume_unavailable", "the data volume is unavailable")
	case errors.Is(err, context.Canceled):
	default:
		h.logger.ErrorContext(r.Context(), "voice request failed", "path", r.URL.Path, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "the request failed")
	}
}

func (h *handler) status(w http.ResponseWriter, r *http.Request, _ accounts.User) {
	writeJSON(w, http.StatusOK, h.service.Status(r.Context()))
}

func (h *handler) recognizerSettings(w http.ResponseWriter, r *http.Request, user accounts.User) {
	settings, err := h.service.RecognizerSettings(r.Context(), user)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (h *handler) setRecognizer(w http.ResponseWriter, r *http.Request, user accounts.User) {
	var request voice.RecognizerCredentials
	if err := decode(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	settings, err := h.service.SetRecognizer(r.Context(), user, request)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (h *handler) clearRecognizer(w http.ResponseWriter, r *http.Request, user accounts.User) {
	if err := h.service.ClearRecognizer(r.Context(), user); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) testRecognizer(w http.ResponseWriter, r *http.Request, user accounts.User) {
	var credentials *voice.RecognizerCredentials
	if r.ContentLength != 0 {
		credentials = &voice.RecognizerCredentials{}
		if err := decode(r, credentials); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
			return
		}
	}
	result, err := h.service.TestRecognizer(r.Context(), user, credentials)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *handler) list(w http.ResponseWriter, r *http.Request, user accounts.User) {
	recordings, err := h.service.List(r.Context(), user)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recordings": recordings})
}

func (h *handler) importRecording(w http.ResponseWriter, r *http.Request, user accounts.User) {
	var request voice.ImportRequest
	if err := decode(r, &request); err != nil || request.FileID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	recording, err := h.service.Import(r.Context(), user, request)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, recording)
}

func (h *handler) detail(w http.ResponseWriter, r *http.Request, user accounts.User) {
	detail, err := h.service.Get(r.Context(), user, r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (h *handler) rename(w http.ResponseWriter, r *http.Request, user accounts.User) {
	var request struct {
		Title string `json:"title"`
	}
	if err := decode(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	recording, err := h.service.Rename(r.Context(), user, r.PathValue("id"), request.Title)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, recording)
}

func (h *handler) deleteRecording(w http.ResponseWriter, r *http.Request, user accounts.User) {
	if err := h.service.Delete(r.Context(), user, r.PathValue("id")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) retry(w http.ResponseWriter, r *http.Request, user accounts.User) {
	recording, err := h.service.Retry(r.Context(), user, r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, recording)
}

// audioTypes names what browsers are told they receive.
var audioTypes = map[string]string{
	".mp3": "audio/mpeg", ".m4a": "audio/mp4", ".m4b": "audio/mp4", ".mp4": "audio/mp4", ".aac": "audio/aac",
	".wav": "audio/wav", ".flac": "audio/flac", ".ogg": "audio/ogg", ".oga": "audio/ogg", ".opus": "audio/ogg",
	".webm": "audio/webm", ".mkv": "audio/webm", ".mov": "audio/mp4",
}

// audio plays the recording: the file itself with ranges, or, for one
// browsers cannot play, the audio decoded to FLAC from ?start=.
func (h *handler) audio(w http.ResponseWriter, r *http.Request, user accounts.User) {
	if start := r.URL.Query().Get("start"); start != "" {
		at, err := strconv.ParseFloat(start, 64)
		if err != nil || at < 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "start is a position in seconds")
			return
		}
		stream, err := h.service.DecodedAudio(r.Context(), user, r.PathValue("id"), at)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		defer stream.Close()
		w.Header().Set("Content-Type", "audio/flac")
		w.Header().Set("Accept-Ranges", "none")
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(flushingWriter{w}, stream)
		return
	}
	content, recording, err := h.service.Audio(r.Context(), user, r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	defer content.Reader.Close()
	contentType := audioTypes[strings.ToLower(path.Ext(recording.Name))]
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": content.Name}))
	http.ServeContent(w, r, content.Name, content.ModifiedAt, content.Reader)
}

// flushingWriter sends each chunk as FFmpeg produces it.
type flushingWriter struct{ w http.ResponseWriter }

func (f flushingWriter) Write(data []byte) (int, error) {
	count, err := f.w.Write(data)
	if flusher, ok := f.w.(http.Flusher); ok {
		flusher.Flush()
	}
	return count, err
}

func (h *handler) editUtterance(w http.ResponseWriter, r *http.Request, user accounts.User) {
	seq, err := strconv.Atoi(r.PathValue("seq"))
	var request struct {
		Text    *string `json:"text"`
		Speaker *string `json:"speaker"`
	}
	if err != nil || decode(r, &request) != nil || request.Text == nil && request.Speaker == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	detail, err := h.service.EditUtterance(r.Context(), user, r.PathValue("id"), seq, request.Text, request.Speaker)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (h *handler) assignSpeaker(w http.ResponseWriter, r *http.Request, user accounts.User) {
	var request voice.AssignRequest
	if err := decode(r, &request); err != nil || request.PersonID == "" && strings.TrimSpace(request.Name) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "name the person or give their id")
		return
	}
	detail, err := h.service.AssignSpeaker(r.Context(), user, r.PathValue("id"), r.PathValue("key"), request)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (h *handler) rejectPerson(w http.ResponseWriter, r *http.Request, user accounts.User) {
	var request struct {
		PersonID string `json:"personId"`
	}
	if err := decode(r, &request); err != nil || request.PersonID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	detail, err := h.service.RejectPerson(r.Context(), user, r.PathValue("id"), r.PathValue("key"), request.PersonID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (h *handler) mergeSpeakers(w http.ResponseWriter, r *http.Request, user accounts.User) {
	var request struct {
		Into string `json:"into"`
	}
	if err := decode(r, &request); err != nil || request.Into == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	detail, err := h.service.MergeSpeakers(r.Context(), user, r.PathValue("id"), r.PathValue("key"), request.Into)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (h *handler) requestTranslation(w http.ResponseWriter, r *http.Request, user accounts.User) {
	var request struct {
		Language string `json:"language"`
	}
	if err := decode(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	detail, err := h.service.RequestTranslation(r.Context(), user, r.PathValue("id"), request.Language)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, detail)
}

func (h *handler) deleteTranslation(w http.ResponseWriter, r *http.Request, user accounts.User) {
	if err := h.service.DeleteTranslation(r.Context(), user, r.PathValue("id"), r.PathValue("language")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) requestSummary(w http.ResponseWriter, r *http.Request, user accounts.User) {
	var request struct {
		Template string `json:"template"`
	}
	if err := decode(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	detail, err := h.service.RequestSummary(r.Context(), user, r.PathValue("id"), request.Template)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, detail)
}

func (h *handler) deleteSummary(w http.ResponseWriter, r *http.Request, user accounts.User) {
	if err := h.service.DeleteSummary(r.Context(), user, r.PathValue("id"), r.PathValue("template")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) people(w http.ResponseWriter, r *http.Request, user accounts.User) {
	people, err := h.service.People(r.Context(), user)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"people": people})
}

func (h *handler) renamePerson(w http.ResponseWriter, r *http.Request, user accounts.User) {
	var request struct {
		Name string `json:"name"`
	}
	if err := decode(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	person, err := h.service.RenamePerson(r.Context(), user, r.PathValue("id"), request.Name)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, person)
}

func (h *handler) deletePerson(w http.ResponseWriter, r *http.Request, user accounts.User) {
	if err := h.service.DeletePerson(r.Context(), user, r.PathValue("id")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) mergePeople(w http.ResponseWriter, r *http.Request, user accounts.User) {
	var request struct {
		Into string `json:"into"`
	}
	if err := decode(r, &request); err != nil || request.Into == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	person, err := h.service.MergePeople(r.Context(), user, r.PathValue("id"), request.Into)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, person)
}
