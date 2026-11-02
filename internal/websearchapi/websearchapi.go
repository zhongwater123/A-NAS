// Package websearchapi serves web search (docs/specs/web-search.md): every
// signed-in account searches, and only administrators turn web search and
// its torrent tab on or off. The search text travels in the request body,
// never in a URL, and is not logged.
package websearchapi

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/websearch"
)

// ThumbnailPath serves the images SearXNG's proxy fetches.
const ThumbnailPath = "/api/v1/web-search/thumbnail"

// API serves service; a nil service answers web_search_unavailable.
type API struct {
	service *websearch.Service
	release websearch.Release
	logger  *slog.Logger
}

func New(service *websearch.Service, release websearch.Release, logger *slog.Logger) *API {
	return &API{service: service, release: release, logger: logger}
}

func (a *API) Routes() []apiroute.Route {
	return []apiroute.Route{
		{Method: http.MethodGet, Pattern: "/api/v1/web-search", Posture: apiroute.Session, Handler: a.ready(a.status)},
		{Method: http.MethodPut, Pattern: "/api/v1/web-search", Posture: apiroute.Admin, Handler: a.ready(a.setSettings)},
		// A search changes nothing, but its text must not travel in a URL.
		{Method: http.MethodPost, Pattern: "/api/v1/web-search/query", Posture: apiroute.Mutation, Handler: a.ready(a.search)},
		{Method: http.MethodGet, Pattern: ThumbnailPath, Posture: apiroute.Session, Handler: a.ready(a.thumbnail)},
		{Method: http.MethodGet, Pattern: "/api/v1/web-search/license", Posture: apiroute.Session, Handler: a.license},
		{Method: http.MethodGet, Pattern: "/api/v1/web-search/license/text", Posture: apiroute.Session, Handler: a.licenseText},
		{Method: http.MethodGet, Pattern: "/api/v1/web-search/source", Posture: apiroute.Session, Handler: a.source},
	}
}

func (a *API) ready(next apiroute.Handler) apiroute.Handler {
	return func(w http.ResponseWriter, r *http.Request, session accounts.Session) {
		if a.service == nil {
			httpjson.WriteError(w, http.StatusServiceUnavailable, "web_search_unavailable", "web search is unavailable")
			return
		}
		next(w, r, session)
	}
}

func (a *API) status(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	status, err := a.service.Status(r.Context())
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, status)
}

func (a *API) setSettings(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var settings websearch.Settings
	if !httpjson.Read(w, r, 1<<10, &settings) {
		return
	}
	status, err := a.service.SetSettings(r.Context(), settings)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	a.logger.InfoContext(r.Context(), "web search settings changed",
		"by", session.User.Username, "enabled", settings.Enabled, "torrents", settings.Torrents)
	httpjson.Write(w, http.StatusOK, status)
}

func (a *API) search(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var query websearch.Query
	if !httpjson.Read(w, r, 4<<10, &query) {
		return
	}
	page, err := a.service.Search(r.Context(), session.User.ID, query)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpjson.Write(w, http.StatusOK, page)
}

func (a *API) thumbnail(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	image, err := a.service.Thumbnail(r.Context(), r.URL.Query().Get("url"), r.URL.Query().Get("h"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", image.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	// The address names the image, so the browser may keep it for a while.
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(image.Body))
}

func (a *API) license(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	license, err := a.release.License()
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, license)
}

func (a *API) licenseText(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	text, err := a.release.LicenseText()
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.WriteString(w, text)
}

func (a *API) source(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	file, name, err := a.release.Source()
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		httpjson.WriteInternal(w, r, a.logger, err)
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, info.ModTime(), file)
}

func (a *API) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, websearch.ErrInvalidQuery):
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_query", "the search is invalid")
	case errors.Is(err, websearch.ErrDisabled):
		httpjson.WriteError(w, http.StatusForbidden, "web_search_disabled", "an administrator has turned web search off")
	case errors.Is(err, websearch.ErrTorrentsDisabled):
		httpjson.WriteError(w, http.StatusForbidden, "torrent_search_disabled", "an administrator has not turned torrent search on")
	case errors.Is(err, websearch.ErrTooFrequent):
		w.Header().Set("Retry-After", "2")
		httpjson.WriteError(w, http.StatusTooManyRequests, "too_many_searches", "too many searches; try again shortly")
	case errors.Is(err, websearch.ErrNotSigned):
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_thumbnail", "the thumbnail address is not one web search gave")
	case errors.Is(err, websearch.ErrNotFound):
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "the image cannot be fetched")
	case errors.Is(err, websearch.ErrNoRelease):
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "this release carries no web search runtime")
	case errors.Is(err, websearch.ErrUnavailable):
		a.logger.WarnContext(r.Context(), "web search unavailable", "error", err)
		httpjson.WriteError(w, http.StatusServiceUnavailable, "web_search_unavailable", "web search is unavailable")
	default:
		httpjson.WriteInternal(w, r, a.logger, err)
	}
}
