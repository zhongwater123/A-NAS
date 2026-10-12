package httpapi

import (
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/music"
)

// musicRoutes serve the music library to every signed-in member. Reads of
// files go through files.Service, so the File Broker reads them as the
// member (ADR 0018).
func (h *productHandler) musicRoutes() {
	h.mux.HandleFunc("GET /api/v1/music/library", h.withMusic(h.handleMusicStatus))
	h.mux.HandleFunc("POST /api/v1/music/library/scan", h.withMusicMutation(h.handleMusicScan))
	h.mux.HandleFunc("GET /api/v1/music/tracks", h.withMusic(h.handleMusicTracks))
	h.mux.HandleFunc("GET /api/v1/music/tracks/{trackID}/stream", h.withMusic(h.handleMusicStream))
	h.mux.HandleFunc("GET /api/v1/music/tracks/{trackID}/lyrics", h.withMusic(h.handleMusicLyrics))
	h.mux.HandleFunc("GET /api/v1/music/covers/{coverID}", h.withMusic(h.handleMusicCover))
	h.mux.HandleFunc("GET /api/v1/music/sources", h.withMusic(h.handleMusicSources))
	h.mux.HandleFunc("PUT /api/v1/music/sources", h.withMusicMutation(h.handleSetMusicSources))
	h.mux.HandleFunc("GET /api/v1/music/favorites", h.withMusic(h.handleMusicFavorites))
	h.mux.HandleFunc("PUT /api/v1/music/favorites/{trackID}", h.withMusicMutation(h.handleMusicFavorite(true)))
	h.mux.HandleFunc("DELETE /api/v1/music/favorites/{trackID}", h.withMusicMutation(h.handleMusicFavorite(false)))
	h.mux.HandleFunc("GET /api/v1/music/recent", h.withMusic(h.handleMusicRecent))
	h.mux.HandleFunc("DELETE /api/v1/music/recent", h.withMusicMutation(h.handleClearMusicRecent))
	h.mux.HandleFunc("POST /api/v1/music/plays", h.withMusicMutation(h.handleMusicPlay))
	h.mux.HandleFunc("GET /api/v1/music/playlists", h.withMusic(h.handleMusicPlaylists))
	h.mux.HandleFunc("POST /api/v1/music/playlists", h.withMusicMutation(h.handleCreateMusicPlaylist))
	h.mux.HandleFunc("PATCH /api/v1/music/playlists/{playlistID}", h.withMusicMutation(h.handleUpdateMusicPlaylist))
	h.mux.HandleFunc("DELETE /api/v1/music/playlists/{playlistID}", h.withMusicMutation(h.handleDeleteMusicPlaylist))
	h.mux.HandleFunc("POST /api/v1/music/playlists/{playlistID}/tracks", h.withMusicMutation(h.handleAddToMusicPlaylist))
}

type musicHandler func(http.ResponseWriter, *http.Request, accounts.Session)

func (h *productHandler) withMusic(next musicHandler) http.HandlerFunc {
	return h.withSession(h.requireMusic(next))
}

func (h *productHandler) withMusicMutation(next musicHandler) http.HandlerFunc {
	return h.withMutation(h.requireMusic(next))
}

func (h *productHandler) requireMusic(next musicHandler) musicHandler {
	return func(w http.ResponseWriter, r *http.Request, session accounts.Session) {
		if h.music == nil {
			writeError(w, http.StatusServiceUnavailable, "music_unavailable", "music library is unavailable")
			return
		}
		next(w, r, session)
	}
}

func (h *productHandler) handleMusicStatus(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	status, err := h.music.Status(r.Context(), session.User)
	if err != nil {
		h.writeMusicError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (h *productHandler) handleMusicScan(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	status, err := h.music.StartScan(r.Context(), session.User)
	if err != nil {
		h.writeMusicError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, status)
}

func (h *productHandler) handleMusicTracks(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	library, err := h.music.Tracks(r.Context(), session.User)
	if err != nil {
		h.writeMusicError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, library)
}

func (h *productHandler) handleMusicStream(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	track, err := h.music.Track(r.Context(), session.User, r.PathValue("trackID"))
	if err != nil {
		h.writeMusicError(w, r, err)
		return
	}
	if track.Transcode {
		h.streamDecodedTrack(w, r, session, track)
		return
	}
	stream, err := h.music.OpenTrack(r.Context(), session.User, track.ID)
	if err != nil {
		h.writeMusicError(w, r, err)
		return
	}
	defer stream.Reader.Close()
	if contentType := stream.Track.ContentType(); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	// Audio elements seek with Range requests, which ServeContent answers.
	http.ServeContent(w, r, stream.Name, stream.ModifiedAt, stream.Reader)
}

// streamDecodedTrack sends the FLAC the media worker decodes from ?start=
// seconds. It cannot seek, so players request a new start instead of a Range.
func (h *productHandler) streamDecodedTrack(w http.ResponseWriter, r *http.Request, session accounts.Session, track music.Track) {
	start := 0.0
	if value := r.URL.Query().Get("start"); value != "" {
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "start must be a non-negative number of seconds")
			return
		}
		start = parsed
	}
	body, err := h.music.Decode(r.Context(), session.User, track.ID, start)
	if err != nil {
		h.writeMusicError(w, r, err)
		return
	}
	defer body.Close()
	w.Header().Set("Content-Type", track.ContentType())
	w.Header().Set("Accept-Ranges", "none")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, body)
}

func (h *productHandler) handleMusicLyrics(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	lyrics, err := h.music.Lyrics(r.Context(), session.User, r.PathValue("trackID"))
	if err != nil {
		h.writeMusicError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": lyrics})
}

func (h *productHandler) handleMusicCover(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	size := r.URL.Query().Get("size")
	if size == "" {
		size = music.CoverSmall
	}
	if size != music.CoverSmall && size != music.CoverLarge {
		writeError(w, http.StatusBadRequest, "invalid_request", "size must be small or large")
		return
	}
	file, err := h.music.CoverPath(r.Context(), session.User, r.PathValue("coverID"), size)
	if err != nil {
		h.writeMusicError(w, r, err)
		return
	}
	content, err := os.Open(file)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "music resource was not found")
		return
	}
	defer content.Close()
	info, err := content.Stat()
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	// A cover ID names the picture's content, so a rendition never changes.
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeContent(w, r, "", info.ModTime(), content)
}

func (h *productHandler) handleMusicSources(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	sources, err := h.music.Sources(r.Context(), session.User)
	if err != nil {
		h.writeMusicError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sources)
}

func (h *productHandler) handleSetMusicSources(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		Custom bool                `json:"custom"`
		Items  []music.SourceInput `json:"items"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	sources, err := h.music.SetSources(r.Context(), session.User, request.Items, !request.Custom)
	if err != nil {
		h.writeMusicError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sources)
}

func (h *productHandler) handleMusicFavorites(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	items, err := h.music.Favorites(r.Context(), session.User)
	if err != nil {
		h.writeMusicError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *productHandler) handleMusicFavorite(favorite bool) musicHandler {
	return func(w http.ResponseWriter, r *http.Request, session accounts.Session) {
		if err := h.music.SetFavorite(r.Context(), session.User, r.PathValue("trackID"), favorite); err != nil {
			h.writeMusicError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *productHandler) handleMusicRecent(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	items, err := h.music.Recent(r.Context(), session.User)
	if err != nil {
		h.writeMusicError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *productHandler) handleClearMusicRecent(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if err := h.music.ClearRecent(r.Context(), session.User); err != nil {
		h.writeMusicError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *productHandler) handleMusicPlay(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		TrackID string `json:"trackId"`
	}
	if err := decodeJSON(r, &request); err != nil || strings.TrimSpace(request.TrackID) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	if err := h.music.RecordPlay(r.Context(), session.User, request.TrackID); err != nil {
		h.writeMusicError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *productHandler) handleMusicPlaylists(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	items, err := h.music.Playlists(r.Context(), session.User)
	if err != nil {
		h.writeMusicError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *productHandler) handleCreateMusicPlaylist(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		Name     string   `json:"name"`
		TrackIDs []string `json:"trackIds"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	playlist, err := h.music.CreatePlaylist(r.Context(), session.User, request.Name, request.TrackIDs)
	if err != nil {
		h.writeMusicError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, playlist)
}

func (h *productHandler) handleUpdateMusicPlaylist(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var change music.PlaylistChange
	if err := decodeJSON(r, &change); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	playlist, err := h.music.UpdatePlaylist(r.Context(), session.User, r.PathValue("playlistID"), change)
	if err != nil {
		h.writeMusicError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, playlist)
}

func (h *productHandler) handleDeleteMusicPlaylist(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	if err := h.music.DeletePlaylist(r.Context(), session.User, r.PathValue("playlistID")); err != nil {
		h.writeMusicError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *productHandler) handleAddToMusicPlaylist(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	var request struct {
		TrackIDs []string `json:"trackIds"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	playlist, err := h.music.AddToPlaylist(r.Context(), session.User, r.PathValue("playlistID"), request.TrackIDs)
	if err != nil {
		h.writeMusicError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, playlist)
}

func (h *productHandler) writeMusicError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, music.ErrNotFound), errors.Is(err, files.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "music resource was not found")
	case errors.Is(err, music.ErrInvalid):
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", "music request is invalid")
	case errors.Is(err, music.ErrUndecodable):
		writeError(w, http.StatusUnprocessableEntity, "undecodable", "the music file cannot be decoded")
	case errors.Is(err, music.ErrDecoderUnavailable):
		writeError(w, http.StatusServiceUnavailable, "decoder_unavailable", "the media worker that decodes this format is unavailable")
	case errors.Is(err, accounts.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "music library access is forbidden")
	default:
		h.writeFileError(w, r, err)
	}
}
