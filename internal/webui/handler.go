// Package webui serves the embedded A-NAS desktop without changing API behavior.
package webui

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

//go:embed all:dist
var embedded embed.FS

type handler struct {
	api                  http.Handler
	assets               fs.FS
	index                []byte
	screensaverDirectory string
}

type Options struct {
	ScreensaverDirectory string
}

func New(api http.Handler) (http.Handler, error) {
	return NewWithOptions(api, Options{})
}

func NewWithOptions(api http.Handler, options Options) (http.Handler, error) {
	if options.ScreensaverDirectory != "" && !filepath.IsAbs(options.ScreensaverDirectory) {
		return nil, errors.New("screen saver directory must be absolute")
	}
	assets, err := fs.Sub(embedded, "dist")
	if err != nil {
		return nil, err
	}
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		return nil, errors.New("embedded web UI is not built")
	}
	return &handler{api: api, assets: assets, index: index, screensaverDirectory: options.ScreensaverDirectory}, nil
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" || strings.HasPrefix(r.URL.Path, "/api/") {
		h.api.ServeHTTP(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	switch {
	case r.URL.Path == "/":
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(h.index)
		}
	case strings.HasPrefix(r.URL.Path, "/assets/"):
		h.serveAsset(w, r)
	case r.URL.Path == "/local-console/screensavers":
		h.serveScreensaverManifest(w, r)
	case strings.HasPrefix(r.URL.Path, "/local-console/screensavers/"):
		h.serveScreensaverVideo(w, r)
	case r.URL.Path == "/local-console/screensaver.mp4":
		h.serveLegacyScreensaverVideo(w, r)
	default:
		http.NotFound(w, r)
	}
}

type screensaverManifest struct {
	Videos []string `json:"videos"`
}

type screensaverVideo struct {
	id   string
	name string
}

func (h *handler) serveScreensaverManifest(w http.ResponseWriter, r *http.Request) {
	videos, err := h.screensaverVideos()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	manifest := screensaverManifest{Videos: make([]string, 0, len(videos))}
	for _, video := range videos {
		manifest.Videos = append(manifest.Videos, "/local-console/screensavers/"+video.id+".mp4")
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := json.NewEncoder(w).Encode(manifest); err != nil {
		return
	}
}

func (h *handler) serveScreensaverVideo(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/local-console/screensavers/"), ".mp4")
	if len(id) != sha256.Size*2 || r.URL.Path != "/local-console/screensavers/"+id+".mp4" {
		http.NotFound(w, r)
		return
	}
	videos, err := h.screensaverVideos()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var name string
	for _, candidate := range videos {
		if candidate.id == id {
			name = candidate.name
			break
		}
	}
	if name == "" {
		http.NotFound(w, r)
		return
	}
	h.serveScreensaverFile(w, r, name)
}

func (h *handler) serveLegacyScreensaverVideo(w http.ResponseWriter, r *http.Request) {
	videos, err := h.screensaverVideos()
	if err != nil || len(videos) == 0 {
		http.NotFound(w, r)
		return
	}
	h.serveScreensaverFile(w, r, videos[0].name)
}

func (h *handler) serveScreensaverFile(w http.ResponseWriter, r *http.Request, name string) {
	video, err := os.Open(filepath.Join(h.screensaverDirectory, name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer video.Close()
	info, err := video.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "video/mp4")
	http.ServeContent(w, r, info.Name(), info.ModTime(), video)
}

func (h *handler) screensaverVideos() ([]screensaverVideo, error) {
	if h.screensaverDirectory == "" {
		return []screensaverVideo{}, nil
	}
	entries, err := os.ReadDir(h.screensaverDirectory)
	if errors.Is(err, fs.ErrNotExist) {
		return []screensaverVideo{}, nil
	}
	if err != nil {
		return nil, err
	}
	videos := make([]screensaverVideo, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || !strings.EqualFold(filepath.Ext(entry.Name()), ".mp4") {
			continue
		}
		digest := sha256.Sum256([]byte(entry.Name()))
		videos = append(videos, screensaverVideo{id: hex.EncodeToString(digest[:]), name: entry.Name()})
	}
	return videos, nil
}

func (h *handler) serveAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if !fs.ValidPath(name) || !strings.HasPrefix(name, "assets/") {
		http.NotFound(w, r)
		return
	}
	contents, err := fs.ReadFile(h.assets, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(contents)
	}
}
