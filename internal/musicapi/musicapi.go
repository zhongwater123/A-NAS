// Package musicapi serves the music library (/api/v1/music) to every
// signed-in member. Reads of files go through files.Service, so the File
// Broker reads them as the member (ADR 0018).
package musicapi

import (
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/filesapi"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/music"
)

// API serves the library; a nil library answers music_unavailable.
type API struct {
	music  *music.Service
	logger *slog.Logger
}

func New(service *music.Service, logger *slog.Logger) *API {
	return &API{music: service, logger: logger}
}

func (h *API) Routes() []apiroute.Route {
	read := func(method, pattern string, handler apiroute.Handler) apiroute.Route {
		return apiroute.Route{Method: method, Pattern: pattern, Posture: apiroute.Session, Handler: h.requireMusic(handler)}
	}
	change := func(method, pattern string, handler apiroute.Handler) apiroute.Route {
		return apiroute.Route{Method: method, Pattern: pattern, Posture: apiroute.Mutation, Handler: h.requireMusic(handler)}
	}
	return []apiroute.Route{
		read(http.MethodGet, "/api/v1/music/library", h.handleMusicStatus),
		change(http.MethodPost, "/api/v1/music/library/scan", h.handleMusicScan),
		read(http.MethodGet, "/api/v1/music/tracks", h.handleMusicTracks),
		read(http.MethodGet, "/api/v1/music/tracks/{trackID}/stream", h.handleMusicStream),
		read(http.MethodGet, "/api/v1/music/tracks/{trackID}/lyrics", h.handleMusicLyrics),
		read(http.MethodGet, "/api/v1/music/covers/{coverID}", h.handleMusicCover),
		read(http.MethodGet, "/api/v1/music/sources", h.handleMusicSources),
		change(http.MethodPut, "/api/v1/music/sources", h.handleSetMusicSources),
		read(http.MethodGet, "/api/v1/music/favorites", h.handleMusicFavorites),
		change(http.MethodPut, "/api/v1/music/favorites/{trackID}", h.handleMusicFavorite(true)),
		change(http.MethodDelete, "/api/v1/music/favorites/{trackID}", h.handleMusicFavorite(false)),
		read(http.MethodGet, "/api/v1/music/recent", h.handleMusicRecent),
		change(http.MethodDelete, "/api/v1/music/recent", h.handleClearMusicRecent),
		change(http.MethodPost, "/api/v1/music/plays", h.handleMusicPlay),
		read(http.MethodGet, "/api/v1/music/playlists", h.handleMusicPlaylists),
		change(http.MethodPost, "/api/v1/music/playlists", h.handleCreateMusicPlaylist),
		change(http.MethodPatch, "/api/v1/music/playlists/{playlistID}", h.handleUpdateMusicPlaylist),
		change(http.MethodDelete, "/api/v1/music/playlists/{playlistID}", h.handleDeleteMusicPlaylist),
		change(http.MethodPost, "/api/v1/music/playlists/{playlistID}/tracks", h.handleAddToMusicPlaylist),
	}
}

func (h *API) requireMusic(next apiroute.Handler) apiroute.Handler {
	return func(w http.ResponseWriter, r *http.Request, session accounts.Session) {
		if h.music == nil {
			httpjson.WriteError(w, http.StatusServiceUnavailable, "music_unavailable", "music library is unavailable")
			return
		}
		next(w, r, session)
	}
}

func (h *API) handleMusicStatus(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	status, err := h.music.Status(r.Context(), session.User)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, status)
}

func (h *API) handleMusicScan(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	status, err := h.music.StartScan(r.Context(), session.User)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusAccepted, status)
}

func (h *API) handleMusicTracks(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	library, err := h.music.Tracks(r.Context(), session.User)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, library)
}

func (h *API) handleMusicStream(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	track, err := h.music.Track(r.Context(), session.User, r.PathValue("trackID"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if track.Transcode {
		h.streamDecodedTrack(w, r, session, track)
		return
	}
	stream, err := h.music.OpenTrack(r.Context(), session.User, track.ID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	defer func() { _ = stream.Reader.Close() }()
	if contentType := stream.Track.ContentType(); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	// Audio elements seek with Range requests, which ServeContent answers.
	http.ServeContent(w, r, stream.Name, stream.ModifiedAt, stream.Reader)
}

// streamDecodedTrack sends the FLAC the media worker decodes from ?start=
// seconds. It cannot seek, so players request a new start instead of a Range.
func (h *API) streamDecodedTrack(w http.ResponseWriter, r *http.Request, session accounts.Session, track music.Track) {
	start := 0.0
	if value := r.URL.Query().Get("start"); value != "" {
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 {
			httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "start must be a non-negative number of seconds")
			return
		}
		start = parsed
	}
	body, err := h.music.Decode(r.Context(), session.User, track.ID, start)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	defer body.Close()
	w.Header().Set("Content-Type", track.ContentType())
	w.Header().Set("Accept-Ranges", "none")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, body)
}

func (h *API) handleMusicLyrics(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	lyrics, err := h.music.Lyrics(r.Context(), session.User, r.PathValue("trackID"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]string{"text": lyrics})
}

func (h *API) handleMusicCover(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	size := r.URL.Query().Get("size")
	if size == "" {
		size = music.CoverSmall
	}
	if size != music.CoverSmall && size != music.CoverLarge {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "size must be small or large")
		return
	}
	file, err := h.music.CoverPath(r.Context(), session.User, r.PathValue("coverID"), size)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	//nolint:gosec // G703: the music catalog names its own cover cache file; the request only selects a catalog entry.
	content, err := os.Open(file)
	if err != nil {
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "music resource was not found")
		return
	}
	defer func() { _ = content.Close() }()
	info, err := content.Stat()
	if err != nil {
		httpjson.WriteInternal(w, r, h.logger, err)
		return
	}
	// A cover ID names the picture's content, so a rendition never changes.
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeContent(w, r, "", info.ModTime(), content)
}

func (h *API) handleMusicSources(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	sources, err := h.music.Sources(r.Context(), session.User)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, sources)
}

func (h *API) handleSetMusicSources(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		Custom bool                `json:"custom"`
		Items  []music.SourceInput `json:"items"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &request) {
		return
	}
	sources, err := h.music.SetSources(r.Context(), session.User, request.Items, !request.Custom)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, sources)
}

func (h *API) handleMusicFavorites(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	items, err := h.music.Favorites(r.Context(), session.User)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": items})
}

func (h *API) handleMusicFavorite(favorite bool) apiroute.Handler {
	return func(w http.ResponseWriter, r *http.Request, session accounts.Session) {
		if err := h.music.SetFavorite(r.Context(), session.User, r.PathValue("trackID"), favorite); err != nil {
			h.writeError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *API) handleMusicRecent(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	items, err := h.music.Recent(r.Context(), session.User)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": items})
}

func (h *API) handleClearMusicRecent(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if err := h.music.ClearRecent(r.Context(), session.User); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *API) handleMusicPlay(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		TrackID string `json:"trackId"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &request) {
		return
	}
	if strings.TrimSpace(request.TrackID) == "" {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	if err := h.music.RecordPlay(r.Context(), session.User, request.TrackID); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *API) handleMusicPlaylists(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	items, err := h.music.Playlists(r.Context(), session.User)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": items})
}

func (h *API) handleCreateMusicPlaylist(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		Name     string   `json:"name"`
		TrackIDs []string `json:"trackIds"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &request) {
		return
	}
	playlist, err := h.music.CreatePlaylist(r.Context(), session.User, request.Name, request.TrackIDs)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusCreated, playlist)
}

func (h *API) handleUpdateMusicPlaylist(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var change music.PlaylistChange
	if !httpjson.Read(w, r, httpjson.Limit, &change) {
		return
	}
	playlist, err := h.music.UpdatePlaylist(r.Context(), session.User, r.PathValue("playlistID"), change)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, playlist)
}

func (h *API) handleDeleteMusicPlaylist(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if err := h.music.DeletePlaylist(r.Context(), session.User, r.PathValue("playlistID")); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *API) handleAddToMusicPlaylist(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		TrackIDs []string `json:"trackIds"`
	}
	if !httpjson.Read(w, r, httpjson.Limit, &request) {
		return
	}
	playlist, err := h.music.AddToPlaylist(r.Context(), session.User, r.PathValue("playlistID"), request.TrackIDs)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, playlist)
}

func (h *API) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, music.ErrNotFound), errors.Is(err, files.ErrNotFound):
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "music resource was not found")
	case errors.Is(err, music.ErrInvalid):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "validation_failed", "music request is invalid")
	case errors.Is(err, music.ErrUndecodable):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "undecodable", "the music file cannot be decoded")
	case errors.Is(err, music.ErrDecoderUnavailable):
		httpjson.WriteError(w, http.StatusServiceUnavailable, "decoder_unavailable", "the media worker that decodes this format is unavailable")
	case errors.Is(err, accounts.ErrForbidden):
		httpjson.WriteError(w, http.StatusForbidden, "forbidden", "music library access is forbidden")
	default:
		filesapi.WriteError(w, r, h.logger, err)
	}
}
