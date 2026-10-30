// Package familyapi exposes 家庭中心 under /api/v1/family. It does not
// authenticate: the family service, or the Product Service in development,
// wraps it so that every request carries the signed-in user and the session
// token (accounts.WithUser, accounts.WithSessionToken), and writes have
// passed the Product Service's CSRF check.
package familyapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/avatar"
	"github.com/zhongwater123/A-NAS/internal/family"
	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
)

const PathPrefix = "/api/v1/family"

// maxJSONBytes bounds a JSON request: a record names at most
// family.MaxPhotos photo IDs besides its text.
const maxJSONBytes = 256 << 10

type handler struct {
	store  *family.Store
	logger *slog.Logger
	media  ffmpegworker.Worker
	mux    *apiroute.Mux
}

type route func(http.ResponseWriter, *http.Request, family.Actor)

// New returns the family API. A nil store answers 503 family_unavailable;
// media converts recordings browsers cannot play and may be nil.
func New(store *family.Store, logger *slog.Logger, media ffmpegworker.Worker) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &handler{store: store, logger: logger, media: media, mux: apiroute.NewMux()}
	routes := map[string]route{
		"GET ":                                            h.overview,
		"GET /catalog":                                    h.catalog,
		"POST /join":                                      h.join,
		"DELETE /join":                                    h.cancelJoin,
		"POST /leave":                                     h.leave,
		"PATCH /me":                                       h.setRole,
		"POST /requests/{accountID}/approve":              h.approve,
		"DELETE /requests/{accountID}":                    h.reject,
		"PATCH /members/{accountID}":                      h.setGuardian,
		"DELETE /members/{accountID}":                     h.remove,
		"GET /hints":                                      h.hints,
		"POST /hints/seen":                                h.hintsSeen,
		"POST /babies":                                    h.addBaby,
		"GET /babies/{babyID}":                            h.baby,
		"PATCH /babies/{babyID}":                          h.updateBaby,
		"DELETE /babies/{babyID}":                         h.deleteBaby,
		"POST /babies/{babyID}/restore":                   h.restoreBaby,
		"GET /babies/{babyID}/avatar":                     h.avatar,
		"PUT /babies/{babyID}/avatar":                     h.setAvatar,
		"DELETE /babies/{babyID}/avatar":                  h.removeAvatar,
		"GET /babies/{babyID}/records":                    h.records,
		"POST /babies/{babyID}/records":                   h.addRecord,
		"POST /babies/{babyID}/recordings":                h.addRecording,
		"GET /babies/{babyID}/trash":                      h.trash,
		"GET /babies/{babyID}/album":                      h.album,
		"GET /babies/{babyID}/growth":                     h.growth,
		"GET /babies/{babyID}/vaccines":                   h.vaccines,
		"PUT /babies/{babyID}/vaccine-plan":               h.vaccinePlan,
		"GET /babies/{babyID}/milestones":                 h.milestones,
		"GET /babies/{babyID}/wall":                       h.wall,
		"POST /babies/{babyID}/wall":                      h.addWallComment,
		"GET /records/{recordID}":                         h.record,
		"PATCH /records/{recordID}":                       h.updateRecord,
		"DELETE /records/{recordID}":                      h.deleteRecord,
		"POST /records/{recordID}/restore":                h.restoreRecord,
		"GET /records/{recordID}/audio":                   h.audio,
		"POST /records/{recordID}/comments":               h.addComment,
		"DELETE /comments/{commentID}":                    h.deleteComment,
		"PUT /records/{recordID}/reactions/{reaction}":    h.react,
		"DELETE /records/{recordID}/reactions/{reaction}": h.unreact,
	}
	for pattern, serve := range routes {
		method, path, _ := strings.Cut(pattern, " ")
		h.mux.HandleFunc(method+" "+PathPrefix+path, h.withActor(serve))
	}
	h.mux.HandleFunc(PathPrefix+"/", func(w http.ResponseWriter, _ *http.Request) {
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
	})
	return h
}

// Endpoints are the operations the family API serves under PathPrefix.
func Endpoints() []apiroute.Endpoint {
	return New(nil, nil, nil).(*handler).mux.Endpoints()
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "family_unavailable", "the family center is not available")
		return
	}
	h.mux.ServeHTTP(w, r)
}

func (h *handler) withActor(next route) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := accounts.UserFrom(r.Context())
		if !ok || user.ID == "" {
			httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", "authentication is required")
			return
		}
		next(w, r, family.Actor{AccountID: user.ID, Username: user.Username, Session: accounts.SessionToken(r.Context())})
	}
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var bodyLimit *http.MaxBytesError
	switch {
	case errors.As(err, &bodyLimit), errors.Is(err, family.ErrTooLarge), errors.Is(err, avatar.ErrTooLarge):
		httpjson.WriteError(w, http.StatusRequestEntityTooLarge, "too_large", "the upload is too large")
	case errors.Is(err, family.ErrNotMember):
		httpjson.WriteError(w, http.StatusForbidden, "not_family_member", "join the family first")
	case errors.Is(err, family.ErrForbidden):
		httpjson.WriteError(w, http.StatusForbidden, "forbidden", "this is not yours to change")
	case errors.Is(err, family.ErrNotFound):
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, family.ErrInvalid):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "validation_failed", "the request is not valid")
	case errors.Is(err, avatar.ErrUnsupported):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "unsupported_image", "the picture is not a JPEG, PNG, GIF or WebP image")
	case errors.Is(err, family.ErrChanged):
		httpjson.WriteError(w, http.StatusConflict, "changed", "someone saved a change first")
	case errors.Is(err, family.ErrConflict):
		httpjson.WriteError(w, http.StatusConflict, "conflict", "the request conflicts with the current state")
	case errors.Is(err, family.ErrLastGuardian):
		httpjson.WriteError(w, http.StatusConflict, "last_guardian", "the family needs at least one guardian")
	case errors.Is(err, family.ErrPhotosUnavailable):
		httpjson.WriteError(w, http.StatusServiceUnavailable, "photos_unavailable", "the photo library is not available")
	default:
		httpjson.WriteInternal(w, r, h.logger, err)
	}
}

func (h *handler) reply(w http.ResponseWriter, r *http.Request, status int, value any, err error) {
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if value == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	httpjson.Write(w, status, value)
}

type items[T any] struct {
	Items []T `json:"items"`
}

func (h *handler) overview(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	overview, err := h.store.Overview(r.Context(), actor)
	h.reply(w, r, http.StatusOK, overview, err)
}

func (h *handler) catalog(w http.ResponseWriter, r *http.Request, _ family.Actor) {
	httpjson.Write(w, http.StatusOK, family.CurrentCatalog())
}

type roleRequest struct {
	Role string `json:"role"`
}

func (h *handler) join(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	var request roleRequest
	if !httpjson.Read(w, r, maxJSONBytes, &request) {
		return
	}
	me, err := h.store.Join(r.Context(), actor, request.Role)
	h.reply(w, r, http.StatusOK, me, err)
}

func (h *handler) cancelJoin(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	h.reply(w, r, http.StatusNoContent, nil, h.store.CancelJoin(r.Context(), actor))
}

func (h *handler) leave(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	h.reply(w, r, http.StatusNoContent, nil, h.store.Leave(r.Context(), actor))
}

func (h *handler) setRole(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	var request roleRequest
	if !httpjson.Read(w, r, maxJSONBytes, &request) {
		return
	}
	member, err := h.store.SetRole(r.Context(), actor, request.Role)
	h.reply(w, r, http.StatusOK, member, err)
}

func (h *handler) approve(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	member, err := h.store.Approve(r.Context(), actor, r.PathValue("accountID"))
	h.reply(w, r, http.StatusOK, member, err)
}

func (h *handler) reject(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	h.reply(w, r, http.StatusNoContent, nil, h.store.Reject(r.Context(), actor, r.PathValue("accountID")))
}

func (h *handler) setGuardian(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	var request struct {
		Guardian *bool `json:"guardian"`
	}
	if !httpjson.Read(w, r, maxJSONBytes, &request) {
		return
	}
	if request.Guardian == nil {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "guardian is required")
		return
	}
	member, err := h.store.SetGuardian(r.Context(), actor, r.PathValue("accountID"), *request.Guardian)
	h.reply(w, r, http.StatusOK, member, err)
}

func (h *handler) remove(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	h.reply(w, r, http.StatusNoContent, nil, h.store.Remove(r.Context(), actor, r.PathValue("accountID")))
}

func (h *handler) hints(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	hints, err := h.store.Hints(r.Context(), actor)
	h.reply(w, r, http.StatusOK, items[family.Hint]{Items: hints}, err)
}

func (h *handler) hintsSeen(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	var request struct {
		Keys []string `json:"keys"`
	}
	if !httpjson.Read(w, r, maxJSONBytes, &request) {
		return
	}
	h.reply(w, r, http.StatusNoContent, nil, h.store.MarkHintsSeen(r.Context(), actor, request.Keys))
}

func (h *handler) addBaby(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	var fields family.BabyFields
	if !httpjson.Read(w, r, maxJSONBytes, &fields) {
		return
	}
	baby, err := h.store.AddBaby(r.Context(), actor, fields)
	h.reply(w, r, http.StatusCreated, baby, err)
}

func (h *handler) baby(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	baby, err := h.store.Baby(r.Context(), actor, r.PathValue("babyID"))
	h.reply(w, r, http.StatusOK, baby, err)
}

func (h *handler) updateBaby(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	var fields family.BabyFields
	if !httpjson.Read(w, r, maxJSONBytes, &fields) {
		return
	}
	baby, err := h.store.UpdateBaby(r.Context(), actor, r.PathValue("babyID"), fields)
	h.reply(w, r, http.StatusOK, baby, err)
}

func (h *handler) deleteBaby(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	err := h.store.DeleteBaby(r.Context(), actor, r.PathValue("babyID"), r.URL.Query().Get("name"))
	h.reply(w, r, http.StatusNoContent, nil, err)
}

func (h *handler) restoreBaby(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	baby, err := h.store.RestoreBaby(r.Context(), actor, r.PathValue("babyID"))
	h.reply(w, r, http.StatusOK, baby, err)
}

func (h *handler) avatar(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	picture, err := h.store.BabyAvatar(r.Context(), actor, r.PathValue("babyID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	// The URL carries the avatar version, so the picture never changes
	// under it; it stays private to this browser.
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("Content-Type", avatar.ContentType)
	_, _ = w.Write(picture)
}

func (h *handler) setAvatar(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	picture, err := readUpload(w, r, "file", avatar.MaxUploadBytes)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	baby, err := h.store.SetBabyAvatar(r.Context(), actor, r.PathValue("babyID"), picture)
	h.reply(w, r, http.StatusOK, baby, err)
}

func (h *handler) removeAvatar(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	baby, err := h.store.RemoveBabyAvatar(r.Context(), actor, r.PathValue("babyID"))
	h.reply(w, r, http.StatusOK, baby, err)
}

// readUpload reads the one file field of a multipart form, at most limit
// bytes.
func readUpload(w http.ResponseWriter, r *http.Request, field string, limit int64) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, limit+1<<20)
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		return nil, family.ErrInvalid
	}
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, family.ErrInvalid
	}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return nil, family.ErrInvalid
		}
		if err != nil {
			return nil, err
		}
		if part.FormName() != field {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(part, limit+1))
		if err != nil {
			return nil, err
		}
		if int64(len(data)) > limit {
			return nil, family.ErrTooLarge
		}
		return data, nil
	}
}

func (h *handler) records(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	query := r.URL.Query()
	page, err := h.store.Records(r.Context(), actor, r.PathValue("babyID"), family.Kind(query.Get("kind")), query.Get("cursor"))
	h.reply(w, r, http.StatusOK, page, err)
}

func (h *handler) addRecord(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	var input family.RecordInput
	if !httpjson.Read(w, r, maxJSONBytes, &input) {
		return
	}
	record, err := h.store.AddRecord(r.Context(), actor, r.PathValue("babyID"), input)
	h.reply(w, r, http.StatusCreated, record, err)
}

// addRecording streams a recording to the store. The form's text fields
// come before the file, so the file is never buffered in memory.
func (h *handler) addRecording(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	r.Body = http.MaxBytesReader(w, r.Body, family.MaxRecordingBytes+1<<20)
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		httpjson.WriteError(w, http.StatusUnsupportedMediaType, "invalid_request", "send the recording as multipart/form-data")
		return
	}
	reader, err := r.MultipartReader()
	if err != nil {
		h.fail(w, r, family.ErrInvalid)
		return
	}
	var input family.RecordingInput
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			h.fail(w, r, family.ErrInvalid)
			return
		}
		if err != nil {
			h.fail(w, r, err)
			return
		}
		if part.FormName() == "file" {
			input.FileName = part.FileName()
			input.ContentType = part.Header.Get("Content-Type")
			record, err := h.store.AddRecording(r.Context(), actor, r.PathValue("babyID"), input, part)
			h.reply(w, r, http.StatusCreated, record, err)
			return
		}
		value, err := io.ReadAll(io.LimitReader(part, 4<<10))
		if err != nil {
			h.fail(w, r, err)
			return
		}
		switch part.FormName() {
		case "title":
			input.Title = string(value)
		case "occurredOn":
			input.OccurredOn = string(value)
		case "requestKey":
			input.RequestKey = string(value)
		}
	}
}

func (h *handler) trash(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	records, err := h.store.Trash(r.Context(), actor, r.PathValue("babyID"))
	h.reply(w, r, http.StatusOK, items[family.Record]{Items: records}, err)
}

func (h *handler) album(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	page, err := h.store.Album(r.Context(), actor, r.PathValue("babyID"), r.URL.Query().Get("cursor"))
	h.reply(w, r, http.StatusOK, page, err)
}

func (h *handler) growth(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	growth, err := h.store.Growth(r.Context(), actor, r.PathValue("babyID"))
	h.reply(w, r, http.StatusOK, growth, err)
}

func (h *handler) vaccines(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	schedule, err := h.store.Vaccines(r.Context(), actor, r.PathValue("babyID"))
	h.reply(w, r, http.StatusOK, schedule, err)
}

func (h *handler) vaccinePlan(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	var request struct {
		Codes []string `json:"codes"`
	}
	if !httpjson.Read(w, r, maxJSONBytes, &request) {
		return
	}
	schedule, err := h.store.SetVaccinePlan(r.Context(), actor, r.PathValue("babyID"), request.Codes)
	h.reply(w, r, http.StatusOK, schedule, err)
}

func (h *handler) milestones(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	entries, err := h.store.Timeline(r.Context(), actor, r.PathValue("babyID"))
	h.reply(w, r, http.StatusOK, items[family.TimelineEntry]{Items: entries}, err)
}

func (h *handler) wall(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	comments, err := h.store.Wall(r.Context(), actor, r.PathValue("babyID"))
	h.reply(w, r, http.StatusOK, items[family.Comment]{Items: comments}, err)
}

func (h *handler) addWallComment(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	var input family.CommentInput
	if !httpjson.Read(w, r, maxJSONBytes, &input) {
		return
	}
	comment, err := h.store.WallComment(r.Context(), actor, r.PathValue("babyID"), input)
	h.reply(w, r, http.StatusCreated, comment, err)
}

func (h *handler) record(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	record, err := h.store.Record(r.Context(), actor, r.PathValue("recordID"))
	h.reply(w, r, http.StatusOK, record, err)
}

func (h *handler) updateRecord(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	var input family.RecordInput
	if !httpjson.Read(w, r, maxJSONBytes, &input) {
		return
	}
	record, err := h.store.UpdateRecord(r.Context(), actor, r.PathValue("recordID"), input)
	h.reply(w, r, http.StatusOK, record, err)
}

func (h *handler) deleteRecord(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	h.reply(w, r, http.StatusNoContent, nil, h.store.DeleteRecord(r.Context(), actor, r.PathValue("recordID")))
}

func (h *handler) restoreRecord(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	record, err := h.store.RestoreRecord(r.Context(), actor, r.PathValue("recordID"))
	h.reply(w, r, http.StatusOK, record, err)
}

// audio plays a recording. Formats browsers play are served as stored, with
// Range for seeking; the others are converted to FLAC by the media worker,
// which cannot seek.
func (h *handler) audio(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	file, recording, err := h.store.OpenRecording(r.Context(), actor, r.PathValue("recordID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	defer func() { _ = file.Close() }()
	w.Header().Set("Cache-Control", "private, no-cache")
	if !family.NeedsConversion(recording.ContentType) {
		w.Header().Set("Content-Type", recording.ContentType)
		http.ServeContent(w, r, "", time.Time{}, file)
		return
	}
	if h.media == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "media_unavailable", "this recording cannot be played on this device")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	body, err := h.media.Audio(ctx, file, ffmpegworker.AudioRequest{Bits: 16})
	if err != nil {
		h.logger.WarnContext(r.Context(), "recording conversion failed", "error", err)
		httpjson.WriteError(w, http.StatusServiceUnavailable, "media_unavailable", "this recording cannot be played right now")
		return
	}
	defer func() { _ = body.Close() }()
	w.Header().Set("Content-Type", "audio/flac")
	w.Header().Set("Accept-Ranges", "none")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, body)
}

func (h *handler) addComment(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	var input family.CommentInput
	if !httpjson.Read(w, r, maxJSONBytes, &input) {
		return
	}
	comment, err := h.store.Comment(r.Context(), actor, r.PathValue("recordID"), input)
	h.reply(w, r, http.StatusCreated, comment, err)
}

func (h *handler) deleteComment(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	h.reply(w, r, http.StatusNoContent, nil, h.store.DeleteComment(r.Context(), actor, r.PathValue("commentID")))
}

func (h *handler) react(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	reactions, err := h.store.React(r.Context(), actor, r.PathValue("recordID"), r.PathValue("reaction"), true)
	h.reply(w, r, http.StatusOK, items[family.Reaction]{Items: reactions}, err)
}

func (h *handler) unreact(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	reactions, err := h.store.React(r.Context(), actor, r.PathValue("recordID"), r.PathValue("reaction"), false)
	h.reply(w, r, http.StatusOK, items[family.Reaction]{Items: reactions}, err)
}
