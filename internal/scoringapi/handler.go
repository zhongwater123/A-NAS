// Package scoringapi serves the photo scoring module's API under
// /api/v1/modules/scoring (docs/specs/photo-scoring.md). It runs in the
// module's own process behind modules.Authenticate; the Product Service only
// imports its Endpoints to route and check the contract. A rater's
// operations never return another rater's answers: those are only in the
// administrators' statistics and export under AdminPrefix.
package scoringapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/modules"
	"github.com/zhongwater123/A-NAS/internal/scoring"
)

const (
	// ID is the module's ID (ADR 0037).
	ID = "scoring"
	// PathPrefix serves raters and AdminPrefix administrators.
	PathPrefix  = "/api/v1/modules/scoring"
	AdminPrefix = PathPrefix + "/admin"
	// Unavailable is the error code while the module cannot answer.
	Unavailable = "scoring_unavailable"
)

// Directory names accounts; modules.SessionResolver is one.
type Directory interface {
	ActiveProfiles(context.Context) ([]accounts.Profile, error)
}

type handler struct {
	store     *scoring.Store
	directory Directory
	version   string
	logger    *slog.Logger
	mux       *apiroute.Mux
	admin     *apiroute.Mux
}

type route func(http.ResponseWriter, *http.Request, accounts.User)

// New serves the API from store. version names the module build in
// exports.
func New(store *scoring.Store, directory Directory, version string, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &handler{store: store, directory: directory, version: version, logger: logger, mux: apiroute.NewMux(), admin: apiroute.NewMux()}
	for pattern, serve := range map[string]route{
		"GET /me":                        h.me,
		"GET /batches":                   h.raterBatches,
		"GET /batches/{batchId}/anchors": h.anchors,
		"GET /batches/{batchId}/anchors/{anchorId}/image": h.anchorImage,
		"POST /batches/{batchId}/claims":                  h.claim,
		"GET /claims/{token}/image":                       h.claimImage,
		"GET /claims/{token}/thumbnail":                   h.claimThumbnail,
		"GET /batches/{batchId}/recent":                   h.recent,
		"PUT /claims/{token}/rating":                      h.rate,
	} {
		method, path, _ := strings.Cut(pattern, " ")
		h.mux.HandleFunc(method+" "+PathPrefix+path, h.withUser(serve))
	}
	for pattern, serve := range map[string]route{
		"GET /sources":                  h.sources,
		"GET /batches":                  h.batches,
		"POST /batches":                 h.createBatch,
		"PATCH /batches/{batchId}":      h.updateBatch,
		"DELETE /batches/{batchId}":     h.deleteBatch,
		"GET /batches/{batchId}/stats":  h.stats,
		"GET /batches/{batchId}/cover":  h.cover,
		"GET /batches/{batchId}/export": h.export,
		"GET /raters":                   h.raters,
		"PUT /raters/{accountId}":       h.addRater,
		"DELETE /raters/{accountId}":    h.removeRater,
	} {
		method, path, _ := strings.Cut(pattern, " ")
		h.admin.HandleFunc(method+" "+AdminPrefix+path, h.withUser(h.administrator(serve)))
	}
	notFound := func(w http.ResponseWriter, _ *http.Request) {
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
	}
	h.mux.ServeMux.HandleFunc(PathPrefix+"/", notFound)
	h.admin.ServeMux.HandleFunc(AdminPrefix+"/", notFound)
	return h
}

// Endpoints are the raters' operations under PathPrefix.
func Endpoints() []apiroute.Endpoint { return New(nil, nil, "", nil).(*handler).mux.Endpoints() }

// AdminEndpoints are the administrators' operations under AdminPrefix.
func AdminEndpoints() []apiroute.Endpoint { return New(nil, nil, "", nil).(*handler).admin.Endpoints() }

// Mounts are how the Product Service routes the module: raters' operations
// need a session, administrators' the administrator's role, which the
// module checks again.
func Mounts() []modules.Mount {
	return []modules.Mount{
		{Prefix: PathPrefix, Posture: apiroute.Session, Endpoints: Endpoints()},
		{Prefix: AdminPrefix, Posture: apiroute.Admin, Endpoints: AdminEndpoints()},
	}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, Unavailable, "photo scoring is not available")
		return
	}
	if r.URL.Path == AdminPrefix || strings.HasPrefix(r.URL.Path, AdminPrefix+"/") {
		h.admin.ServeHTTP(w, r)
		return
	}
	h.mux.ServeHTTP(w, r)
}

func (h *handler) withUser(next route) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := accounts.UserFrom(r.Context())
		if !ok {
			httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", "authentication is required")
			return
		}
		next(w, r, user)
	}
}

// administrator checks the role again: the Product Service already
// required it for AdminPrefix, but the module does not rely on that.
func (h *handler) administrator(next route) route {
	return func(w http.ResponseWriter, r *http.Request, user accounts.User) {
		if !user.ActiveAdministrator() {
			httpjson.WriteError(w, http.StatusForbidden, "forbidden", "administrator permission is required")
			return
		}
		next(w, r, user)
	}
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, scoring.ErrNotFound):
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, scoring.ErrNotRater):
		httpjson.WriteError(w, http.StatusForbidden, "not_rater", "an administrator has not chosen you as a rater")
	case errors.Is(err, scoring.ErrInvalid):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "validation_failed", strings.TrimPrefix(err.Error(), scoring.ErrInvalid.Error()+": "))
	case errors.Is(err, scoring.ErrLocked):
		httpjson.WriteError(w, http.StatusConflict, "rating_locked", "only the latest answer can be changed")
	case errors.Is(err, scoring.ErrAlreadyRated):
		httpjson.WriteError(w, http.StatusConflict, "already_rated", "this photo already has your answer")
	case errors.Is(err, scoring.ErrNotReady):
		httpjson.WriteError(w, http.StatusConflict, "batch_importing", "the batch is still importing")
	case errors.Is(err, scoring.ErrInUse):
		httpjson.WriteError(w, http.StatusConflict, "batch_in_use", "the batch already has ratings")
	default:
		httpjson.WriteInternal(w, r, h.logger, err)
	}
}

type me struct {
	Admin bool `json:"admin"`
	Rater bool `json:"rater"`
}

func (h *handler) me(w http.ResponseWriter, r *http.Request, user accounts.User) {
	rater, err := h.store.IsRater(r.Context(), user.ID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpjson.Write(w, http.StatusOK, me{Admin: user.ActiveAdministrator(), Rater: rater})
}

type raterBatch struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Rubric   string `json:"rubric"`
	Rated    int    `json:"rated"`
	Finished bool   `json:"finished"`
}

func (h *handler) raterBatches(w http.ResponseWriter, r *http.Request, user accounts.User) {
	batches, err := h.store.OpenBatches(r.Context(), user.ID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]raterBatch, 0, len(batches))
	for _, batch := range batches {
		items = append(items, raterBatch{ID: batch.ID, Name: batch.Name, Rubric: batch.Rubric, Rated: batch.Rated, Finished: batch.Finished})
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": items})
}

type anchor struct {
	ID     int64   `json:"id"`
	Score  float64 `json:"score"`
	Width  int     `json:"width"`
	Height int     `json:"height"`
}

// canSee lets raters and administrators see a batch's reference photos.
func (h *handler) canSee(ctx context.Context, user accounts.User) error {
	if user.ActiveAdministrator() {
		return nil
	}
	rater, err := h.store.IsRater(ctx, user.ID)
	if err != nil || !rater {
		return errors.Join(err, scoring.ErrNotRater)
	}
	return nil
}

func (h *handler) anchors(w http.ResponseWriter, r *http.Request, user accounts.User) {
	if err := h.canSee(r.Context(), user); err != nil {
		h.fail(w, r, err)
		return
	}
	anchors, err := h.store.Anchors(r.Context(), r.PathValue("batchId"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]anchor, 0, len(anchors))
	for _, a := range anchors {
		items = append(items, anchor{ID: a.ID, Score: points(a.Score), Width: a.Width, Height: a.Height})
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": items})
}

func (h *handler) anchorImage(w http.ResponseWriter, r *http.Request, user accounts.User) {
	if err := h.canSee(r.Context(), user); err != nil {
		h.fail(w, r, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("anchorId"), 10, 64)
	if err != nil {
		h.fail(w, r, scoring.ErrNotFound)
		return
	}
	file, err := h.store.AnchorPreview(r.Context(), r.PathValue("batchId"), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.serveJPEG(w, r, file, true)
}

type claim struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
	Width     int       `json:"width"`
	Height    int       `json:"height"`
}

func (h *handler) claim(w http.ResponseWriter, r *http.Request, user accounts.User) {
	claims, finished, err := h.store.Claim(r.Context(), r.PathValue("batchId"), user.ID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]claim, 0, len(claims))
	for _, c := range claims {
		items = append(items, claim{Token: c.Token, ExpiresAt: c.ExpiresAt.UTC(), Width: c.Width, Height: c.Height})
	}
	w.Header().Set("Cache-Control", "no-store")
	httpjson.Write(w, http.StatusOK, map[string]any{"items": items, "finished": finished})
}

// points and hundredthsOf convert between the API's scores, 0 to 5 with
// two decimals, and the store's whole hundredths.
func points(hundredths int) float64 { return float64(hundredths) / 100 }

func hundredthsOf(score float64) (int, bool) {
	scaled := score * 100
	rounded := math.Round(scaled)
	if math.IsNaN(score) || math.Abs(scaled-rounded) > 1e-6 || rounded < 0 || rounded > scoring.MaxScore {
		return 0, false
	}
	return int(rounded), true
}

type recentScore struct {
	Token  string  `json:"token"`
	Score  float64 `json:"score"`
	Width  int     `json:"width"`
	Height int     `json:"height"`
}

func (h *handler) recent(w http.ResponseWriter, r *http.Request, user accounts.User) {
	recent, err := h.store.RecentScores(r.Context(), r.PathValue("batchId"), user.ID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]recentScore, 0, len(recent))
	for _, score := range recent {
		items = append(items, recentScore{Token: score.Token, Score: points(score.Score), Width: score.Width, Height: score.Height})
	}
	w.Header().Set("Cache-Control", "no-store")
	httpjson.Write(w, http.StatusOK, map[string]any{"items": items})
}

func (h *handler) claimThumbnail(w http.ResponseWriter, r *http.Request, user accounts.User) {
	file, err := h.store.ClaimThumbnail(r.Context(), r.PathValue("token"), user.ID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.serveJPEG(w, r, file, true)
}

func (h *handler) cover(w http.ResponseWriter, r *http.Request, _ accounts.User) {
	file, err := h.store.BatchCover(r.Context(), r.PathValue("batchId"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	// Photos imported later may come first in the batch's order.
	h.serveJPEG(w, r, file, false)
}

func (h *handler) claimImage(w http.ResponseWriter, r *http.Request, user accounts.User) {
	file, err := h.store.ClaimPreview(r.Context(), r.PathValue("token"), user.ID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.serveJPEG(w, r, file, true)
}

// serveJPEG answers a preview or thumbnail. One that is the same bytes for
// as long as its token or anchor exists is immutable, and the browser may
// keep it.
func (h *handler) serveJPEG(w http.ResponseWriter, r *http.Request, name string, immutable bool) {
	//nolint:gosec // G703: the store names its own preview file from a content hash in its database; the request only selects a claim or anchor.
	file, err := os.Open(name)
	if errors.Is(err, os.ErrNotExist) {
		h.fail(w, r, scoring.ErrNotFound)
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	defer func() { _ = file.Close() }()
	w.Header().Set("Content-Type", "image/jpeg")
	if immutable {
		w.Header().Set("Cache-Control", "private, max-age=86400, immutable")
	} else {
		w.Header().Set("Cache-Control", "private, no-cache")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "", time.Time{}, file)
}

type size struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type ratingRequest struct {
	Submission string   `json:"submission"`
	Kind       string   `json:"kind"`
	Score      *float64 `json:"score,omitempty"`
	ViewMs     int      `json:"viewMs"`
	Viewport   *size    `json:"viewport,omitempty"`
	Display    *size    `json:"display,omitempty"`
	PixelRatio float64  `json:"pixelRatio,omitempty"`
}

func (h *handler) rate(w http.ResponseWriter, r *http.Request, user accounts.User) {
	var request ratingRequest
	if !httpjson.Read(w, r, 16<<10, &request) {
		return
	}
	submission := scoring.Submission{
		ID: request.Submission, Kind: request.Kind, ViewMilliseconds: request.ViewMs, PixelRatio: request.PixelRatio,
	}
	if request.Score != nil {
		hundredths, ok := hundredthsOf(*request.Score)
		if !ok {
			h.fail(w, r, fmt.Errorf("%w: a score is 0 to 5 with at most two decimals", scoring.ErrInvalid))
			return
		}
		submission.Score = &hundredths
	}
	if request.Viewport != nil {
		submission.ViewportWidth, submission.ViewportHeight = request.Viewport.Width, request.Viewport.Height
	}
	if request.Display != nil {
		submission.DisplayWidth, submission.DisplayHeight = request.Display.Width, request.Display.Height
	}
	result, err := h.store.Submit(r.Context(), r.PathValue("token"), user.ID, submission)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"rated": result.Rated})
}

func (h *handler) sources(w http.ResponseWriter, r *http.Request, _ accounts.User) {
	sources, err := h.store.Sources(r.Context())
	if errors.Is(err, os.ErrNotExist) {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "photos_folder_missing", "the scoring folder is missing from the Shared space")
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	type source struct {
		Name string `json:"name"`
	}
	items := make([]source, 0, len(sources))
	for _, name := range sources {
		items = append(items, source{Name: name})
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": items})
}

type batch struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Source    string    `json:"source"`
	Required  int       `json:"required"`
	Rubric    string    `json:"rubric"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"createdAt"`
	Import    struct {
		Files    int `json:"files"`
		Imported int `json:"imported"`
		Skipped  int `json:"skipped"`
	} `json:"import"`
	Photos   int `json:"photos"`
	Anchors  int `json:"anchors"`
	Complete int `json:"complete"`
	Started  int `json:"started"`
	Flagged  int `json:"flagged"`
}

func batchBody(b scoring.Batch) batch {
	body := batch{
		ID: b.ID, Name: b.Name, Source: b.Source, Required: b.Required, Rubric: b.Rubric, State: b.State,
		CreatedAt: b.CreatedAt.UTC(), Photos: b.Photos, Anchors: b.Anchors, Complete: b.Complete, Started: b.Started, Flagged: b.Flagged,
	}
	body.Import.Files, body.Import.Imported, body.Import.Skipped = b.Files, b.Imported, b.Skipped
	return body
}

func (h *handler) batches(w http.ResponseWriter, r *http.Request, _ accounts.User) {
	batches, err := h.store.Batches(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]batch, 0, len(batches))
	for _, b := range batches {
		items = append(items, batchBody(b))
	}
	w.Header().Set("Cache-Control", "no-store")
	httpjson.Write(w, http.StatusOK, map[string]any{"items": items})
}

func (h *handler) createBatch(w http.ResponseWriter, r *http.Request, user accounts.User) {
	var request struct {
		Name     string `json:"name"`
		Source   string `json:"source"`
		Required int    `json:"required"`
		Rubric   string `json:"rubric"`
	}
	if !httpjson.Read(w, r, 64<<10, &request) {
		return
	}
	created, err := h.store.CreateBatch(r.Context(), user.ID, scoring.BatchInput{
		Name: request.Name, Source: request.Source, Required: request.Required, Rubric: request.Rubric,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusCreated, batchBody(created))
}

func (h *handler) updateBatch(w http.ResponseWriter, r *http.Request, _ accounts.User) {
	var request struct {
		Name     *string `json:"name,omitempty"`
		Rubric   *string `json:"rubric,omitempty"`
		Required *int    `json:"required,omitempty"`
		State    *string `json:"state,omitempty"`
	}
	if !httpjson.Read(w, r, 64<<10, &request) {
		return
	}
	updated, err := h.store.UpdateBatch(r.Context(), r.PathValue("batchId"), scoring.BatchPatch{
		Name: request.Name, Rubric: request.Rubric, Required: request.Required, State: request.State,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, batchBody(updated))
}

func (h *handler) deleteBatch(w http.ResponseWriter, r *http.Request, _ accounts.User) {
	if err := h.store.DeleteBatch(r.Context(), r.PathValue("batchId")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type person struct {
	ID            string `json:"id"`
	Username      string `json:"username,omitempty"`
	Nickname      string `json:"nickname,omitempty"`
	AvatarVersion string `json:"avatarVersion,omitempty"`
}

// people names the active accounts; one the directory cannot name stays
// just its ID.
func (h *handler) people(ctx context.Context) (map[string]accounts.Profile, error) {
	profiles, err := h.directory.ActiveProfiles(ctx)
	if err != nil {
		return nil, err
	}
	people := make(map[string]accounts.Profile, len(profiles))
	for _, profile := range profiles {
		people[profile.ID] = profile
	}
	return people, nil
}

func personOf(id string, people map[string]accounts.Profile) person {
	profile := people[id]
	return person{ID: id, Username: profile.Username, Nickname: profile.Nickname, AvatarVersion: profile.AvatarVersion}
}

type raterStats struct {
	Rater             person    `json:"rater"`
	Active            bool      `json:"active"`
	Scores            int       `json:"scores"`
	Skips             int       `json:"skips"`
	Flags             int       `json:"flags"`
	Repeats           int       `json:"repeats"`
	MedianViewMs      int       `json:"medianViewMs"`
	Mean              float64   `json:"mean"`
	StandardDeviation float64   `json:"standardDeviation"`
	Agreement         *float64  `json:"agreement"`
	AgreementPhotos   int       `json:"agreementPhotos"`
	RepeatDifference  *float64  `json:"repeatDifference"`
	FastShare         float64   `json:"fastShare"`
	LongestRun        int       `json:"longestRun"`
	LastAt            time.Time `json:"lastAt"`
}

func (h *handler) stats(w http.ResponseWriter, r *http.Request, _ accounts.User) {
	stats, err := h.store.Stats(r.Context(), r.PathValue("batchId"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	people, err := h.people(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]raterStats, 0, len(stats))
	for _, st := range stats {
		_, active := people[st.Rater]
		items = append(items, raterStats{
			Rater: personOf(st.Rater, people), Active: active, Scores: st.Scores, Skips: st.Skips, Flags: st.Flags, Repeats: st.Repeats,
			MedianViewMs: st.MedianViewMilliseconds, Mean: st.Mean, StandardDeviation: st.StandardDeviation,
			Agreement: st.Agreement, AgreementPhotos: st.AgreementPhotos, RepeatDifference: st.RepeatDifference,
			FastShare: st.FastShare, LongestRun: st.LongestRun, LastAt: st.LastAt.UTC(),
		})
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": items})
}

// export renders the zip into the store's staging folder first, since the
// store reads it on its only connection, and then sends the file.
func (h *handler) export(w http.ResponseWriter, r *http.Request, _ accounts.User) {
	id := r.PathValue("batchId")
	current, err := h.store.Batch(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	profiles, err := h.people(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	people := make(map[string]scoring.Person, len(profiles))
	for id, profile := range profiles {
		people[id] = scoring.Person{Username: profile.Username, Nickname: profile.Nickname}
	}
	file, err := os.CreateTemp(h.store.StagingDir(), "export-*.zip")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	defer func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}()
	if err := h.store.Export(r.Context(), id, file, people, h.version); err != nil {
		h.fail(w, r, err)
		return
	}
	size, err := file.Seek(0, io.SeekCurrent)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		h.fail(w, r, err)
		return
	}
	name := "照片打分-" + current.Name + "-" + time.Now().Format("20060102-1504") + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("Cache-Control", "no-store")
	if _, err := io.Copy(w, file); err != nil {
		h.logger.WarnContext(r.Context(), "sending a photo scoring export stopped", "error", err)
	}
}

type raterEntry struct {
	ID            string `json:"id"`
	Username      string `json:"username"`
	Nickname      string `json:"nickname,omitempty"`
	AvatarVersion string `json:"avatarVersion,omitempty"`
	Rater         bool   `json:"rater"`
}

func (h *handler) raters(w http.ResponseWriter, r *http.Request, _ accounts.User) {
	profiles, err := h.directory.ActiveProfiles(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	chosen, err := h.store.Raters(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	isRater := make(map[string]bool, len(chosen))
	for _, id := range chosen {
		isRater[id] = true
	}
	items := make([]raterEntry, 0, len(profiles))
	for _, profile := range profiles {
		items = append(items, raterEntry{
			ID: profile.ID, Username: profile.Username, Nickname: profile.Nickname, AvatarVersion: profile.AvatarVersion,
			Rater: isRater[profile.ID],
		})
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": items})
}

func (h *handler) addRater(w http.ResponseWriter, r *http.Request, user accounts.User) {
	people, err := h.people(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	account := r.PathValue("accountId")
	if _, active := people[account]; !active {
		h.fail(w, r, scoring.ErrNotFound)
		return
	}
	if err := h.store.SetRater(r.Context(), account, user.ID, true); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) removeRater(w http.ResponseWriter, r *http.Request, user accounts.User) {
	if err := h.store.SetRater(r.Context(), r.PathValue("accountId"), user.ID, false); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Start begins the store's background work, imports and daily backups, until
// ctx ends, and returns the API behind modules.Authenticate.
func Start(ctx context.Context, store *scoring.Store, sessions modules.SessionResolver, version string, logger *slog.Logger) http.Handler {
	go store.RunImports(ctx)
	go store.RunBackups(ctx)
	return modules.Authenticate(sessions, Unavailable, logger, New(store, sessions, version, logger))
}
