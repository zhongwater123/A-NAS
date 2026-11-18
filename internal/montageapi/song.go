package montageapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/montage"
	"github.com/zhongwater123/A-NAS/internal/music"
)

// SongHeader carries what the Product Service tells of the track it copies
// (montage.SongSource), as base64 of JSON. Only the Product Service sets it,
// on the request CopySong makes; it removes the header from every other
// request first.
const SongHeader = "X-A-NAS-Song"

// SongPath is where a project's song is chosen.
const SongPath = PathPrefix + "/projects/{projectID}/song"

// Tracks opens a track of the member's music library as the member;
// music.Service provides it.
type Tracks interface {
	OpenTrack(ctx context.Context, actor accounts.User, id string) (music.Stream, error)
}

// CopySong serves PUT /projects/{id}/song in the Product Service
// (docs/specs/montage-center.md, 配乐): it opens the track the member names
// through the File Broker as the member, the way the music library plays it,
// and streams it to the montage center, next, with what it knows of the
// track. Another member's track is not_found, as a missing one.
func CopySong(tracks Tracks, next http.Handler, logger *slog.Logger) apiroute.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request, session accounts.Session) {
		r.Header.Del(SongHeader)
		r.Header.Del(AssistantHeader)
		if tracks == nil {
			httpjson.WriteError(w, http.StatusServiceUnavailable, "music_unavailable", "music library is unavailable")
			return
		}
		var request struct {
			TrackID string `json:"trackId"`
		}
		if !httpjson.Read(w, r, 4<<10, &request) {
			return
		}
		if request.TrackID == "" || len(request.TrackID) > 256 {
			httpjson.WriteError(w, http.StatusBadRequest, "validation_failed", "the request is not valid")
			return
		}
		stream, err := tracks.OpenTrack(r.Context(), session.User, request.TrackID)
		switch {
		case errors.Is(err, music.ErrNotFound):
			httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
			return
		case err != nil:
			logger.WarnContext(r.Context(), "opening a track for the montage center failed", "error", err)
			httpjson.WriteError(w, http.StatusServiceUnavailable, "music_unavailable", "music library is unavailable")
			return
		}
		defer func() { _ = stream.Reader.Close() }()
		track := stream.Track
		// What the library knows already answers without copying anything;
		// the montage center checks the copy itself.
		if track.SizeBytes > montage.MaxSongBytes || stream.SizeBytes > montage.MaxSongBytes {
			httpjson.WriteError(w, http.StatusRequestEntityTooLarge, "song_too_large", "the song is larger than 100 MiB")
			return
		}
		if seconds := float64(track.DurationMs) / 1000; track.DurationMs > 0 && (seconds < montage.MinSongSeconds || seconds > montage.MaxSongSeconds) {
			httpjson.WriteError(w, http.StatusUnprocessableEntity, "song_length", "the song is shorter than 30 seconds or longer than 8 minutes")
			return
		}
		source := montage.SongSource{TrackID: track.ID, Title: track.Title}
		if !track.Transcode {
			source.ContentType = track.ContentType()
		}
		header, err := encodeSource(source)
		if err != nil {
			httpjson.WriteInternal(w, r, logger, err)
			return
		}
		copied := r.Clone(r.Context())
		copied.Body = io.NopCloser(io.LimitReader(stream.Reader, montage.MaxSongBytes+1))
		copied.ContentLength = -1
		copied.Header.Del("Content-Length")
		copied.Header.Set("Content-Type", "application/octet-stream")
		copied.Header.Set(SongHeader, header)
		next.ServeHTTP(w, copied.WithContext(accounts.WithUser(r.Context(), session.User)))
	}
}

func encodeSource(source montage.SongSource) (string, error) {
	data, err := json.Marshal(source)
	return base64.StdEncoding.EncodeToString(data), err
}

func decodeSource(header string) (montage.SongSource, bool) {
	data, err := base64.StdEncoding.DecodeString(header)
	if err != nil || len(data) > 4<<10 {
		return montage.SongSource{}, false
	}
	var source montage.SongSource
	if err := json.Unmarshal(data, &source); err != nil {
		return montage.SongSource{}, false
	}
	return source, true
}

// putSong takes the song CopySong streams.
func (h *handler) putSong(w http.ResponseWriter, r *http.Request, user accounts.User) {
	source, ok := decodeSource(r.Header.Get(SongHeader))
	if !ok {
		httpjson.WriteError(w, http.StatusBadRequest, "validation_failed", "the request is not valid")
		return
	}
	detail, err := h.store.PutSong(r.Context(), user.ID, r.PathValue("projectID"), source, r.Body)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, detail)
}

func (h *handler) song(w http.ResponseWriter, r *http.Request, user accounts.User) {
	detail, err := h.store.Song(r.Context(), user.ID, r.PathValue("projectID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, detail)
}

// songAudio lets the member hear the copy: as it is with Range, or decoded
// to FLAC from ?start= seconds when browsers cannot play it, as the music
// library plays such tracks.
func (h *handler) songAudio(w http.ResponseWriter, r *http.Request, user accounts.User) {
	audio, err := h.store.OpenSongAudio(r.Context(), user.ID, r.PathValue("projectID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if audio.ContentType != "" {
		defer func() { _ = audio.File.Close() }()
		w.Header().Set("Content-Type", audio.ContentType)
		w.Header().Set("Cache-Control", "private, no-cache")
		http.ServeContent(w, r, "", audio.ModifiedAt, audio.File)
		return
	}
	_ = audio.File.Close()
	start := 0.0
	if value := r.URL.Query().Get("start"); value != "" {
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 {
			httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "start must be a non-negative number of seconds")
			return
		}
		start = parsed
	}
	body, err := h.store.DecodeSong(r.Context(), user.ID, r.PathValue("projectID"), start)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	defer func() { _ = body.Close() }()
	w.Header().Set("Content-Type", "audio/flac")
	w.Header().Set("Accept-Ranges", "none")
	w.Header().Set("Cache-Control", "private, no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, body)
}

// hero serves the device's background film for the home page, or its
// poster: 404 when the device has none, and the page plays the member's
// videos instead.
func (h *handler) hero(which string) route {
	return func(w http.ResponseWriter, r *http.Request, _ accounts.User) {
		file, err := h.store.OpenHero(which)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		defer func() { _ = file.File.Close() }()
		w.Header().Set("Content-Type", file.ContentType)
		w.Header().Set("Cache-Control", "private, max-age=3600")
		http.ServeContent(w, r, "", file.Info.ModTime(), file.File)
	}
}
