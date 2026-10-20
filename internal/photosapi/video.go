package photosapi

import (
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/photos"
)

// A video plays in one of two ways (docs/specs/photo-video.md): the browser
// reads the original by ranges, or it reads a stream the Media Worker
// converts the original into, restarted at a new start for each seek. The
// playback route says which, for the codecs the browser reports in caps.

func (h *handler) videoRoutes() {
	p := PathPrefix
	h.mux.HandleFunc("GET "+p+"/assets/{assetID}/video/playback", h.videoPlayback)
	h.mux.HandleFunc("GET "+p+"/assets/{assetID}/video/original", h.videoOriginal)
	h.mux.HandleFunc("GET "+p+"/assets/{assetID}/video/stream", h.videoStream)
}

// browserCaps reads what the browser decodes, as the media center's player
// reports it: a comma-separated caps parameter.
func browserCaps(values url.Values) photos.BrowserCaps {
	var caps photos.BrowserCaps
	for _, value := range strings.Split(values.Get("caps"), ",") {
		switch strings.TrimSpace(value) {
		case "hevc":
			caps.HEVC = true
		case "av1":
			caps.AV1 = true
		case "vp9":
			caps.VP9 = true
		case "webm":
			caps.WebM = true
		case "mkv":
			caps.Matroska = true
		case "ac3":
			caps.AC3 = true
		case "eac3":
			caps.EAC3 = true
		}
	}
	return caps
}

func (h *handler) videoPlayback(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	playback, err := h.service.VideoPlayback(r.Context(), principal, r.PathValue("assetID"), browserCaps(r.URL.Query()))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, playback)
}

func (h *handler) videoOriginal(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	content, err := h.service.OpenVideo(r.Context(), principal, r.PathValue("assetID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.serve(w, r, content, "inline")
}

func (h *handler) videoStream(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	query := r.URL.Query()
	start := 0.0
	if value := query.Get("start"); value != "" {
		var err error
		if start, err = strconv.ParseFloat(value, 64); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid_request", "start must be a number of seconds")
			return
		}
	}
	stream, err := h.service.StreamVideo(r.Context(), principal, r.PathValue("assetID"), photos.VideoStreamRequest{
		Start: start, Transcode: query.Get("transcode") == "1", Caps: browserCaps(query),
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	defer stream.Close()
	// A converted stream cannot seek; the player restarts it elsewhere.
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Accept-Ranges", "none")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(flushingWriter{w}, stream)
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

// uploadResult answers an upload: the new asset, created, or the Live Photos
// a MOV completed.
func (h *handler) uploadResult(w http.ResponseWriter, result photos.UploadResult) {
	if result.Asset != nil {
		writeJSON(w, http.StatusCreated, result.Asset)
		return
	}
	writeJSON(w, http.StatusOK, completedResponse{Completed: result.Completed})
}

type completedResponse struct {
	Completed []photos.Asset `json:"completed"`
}
