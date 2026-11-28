package protocol

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/music"
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
	"github.com/zhongwater123/A-NAS/internal/videocut/app"
	"github.com/zhongwater123/A-NAS/internal/videocut/domain"
)

// SongHeader carries what the Product Service tells of the track it copies
// (app.SongSource), as base64 of JSON. Only the Product Service sets it,
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
// (docs/specs/videocut-center.md, 配乐): it opens the track the member names
// through the File Broker as the member, the way the music library plays it,
// and streams it to VideoCut, next, with what it knows of the
// track. Another member's track is not_found, as a missing one.
func CopySong(tracks Tracks, next http.Handler, logger *slog.Logger) apiroute.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request, session accounts.Session) {
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
		// As the music library answers when it plays the track
		// (musicapi): the file may have gone, or the member's access to
		// it, since the library was scanned.
		case errors.Is(err, music.ErrNotFound), errors.Is(err, files.ErrNotFound):
			httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
			return
		case errors.Is(err, accounts.ErrForbidden):
			httpjson.WriteError(w, http.StatusForbidden, "forbidden", "music library access is forbidden")
			return
		case err != nil:
			logger.WarnContext(r.Context(), "opening a track for VideoCut failed", "error", err)
			httpjson.WriteError(w, http.StatusServiceUnavailable, "music_unavailable", "music library is unavailable")
			return
		}
		defer func() { _ = stream.Reader.Close() }()
		track := stream.Track
		// What the library knows already answers without copying anything;
		// VideoCut checks the copy itself.
		if track.SizeBytes > app.MaxSongBytes || stream.SizeBytes > app.MaxSongBytes {
			httpjson.WriteError(w, http.StatusRequestEntityTooLarge, "song_too_large", "the song is larger than 100 MiB")
			return
		}
		if seconds := float64(track.DurationMs) / 1000; track.DurationMs > 0 && (seconds < app.MinSongSeconds || seconds > app.MaxSongSeconds) {
			httpjson.WriteError(w, http.StatusUnprocessableEntity, "song_length", "the song is shorter than 30 seconds or longer than 8 minutes")
			return
		}
		// The library does not bound a title; the header's reader does
		// (decodeSource).
		source := domain.SongSource{TrackID: track.ID, Title: app.SongTitle(track.Title)}
		if !track.Transcode {
			source.ContentType = track.ContentType()
		}
		header, err := encodeSource(source)
		if err != nil {
			httpjson.WriteInternal(w, r, logger, err)
			return
		}
		copied := r.Clone(r.Context())
		copied.Body = io.NopCloser(io.LimitReader(stream.Reader, app.MaxSongBytes+1))
		copied.ContentLength = -1
		copied.Header.Del("Content-Length")
		copied.Header.Set("Content-Type", "application/octet-stream")
		ctx := serviceproxy.WithInternalHeader(accounts.WithUser(r.Context(), session.User), SongHeader, header)
		next.ServeHTTP(w, copied.WithContext(ctx))
	}
}

func encodeSource(source domain.SongSource) (string, error) {
	data, err := json.Marshal(source)
	return base64.StdEncoding.EncodeToString(data), err
}

func decodeSource(header string) (domain.SongSource, bool) {
	data, err := base64.StdEncoding.DecodeString(header)
	if err != nil || len(data) > 4<<10 {
		return domain.SongSource{}, false
	}
	var source domain.SongSource
	if err := json.Unmarshal(data, &source); err != nil {
		return domain.SongSource{}, false
	}
	return source, true
}
