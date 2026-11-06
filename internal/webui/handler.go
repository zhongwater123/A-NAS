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
	"net/url"
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
	companionDirectory   string
}

type Options struct {
	ScreensaverDirectory string
	// CompanionDirectory holds the companion's animations, which
	// install-companion-animations.sh keeps outside releases like the screen
	// saver videos (ADR 0014).
	CompanionDirectory string
}

func NewWithOptions(api http.Handler, options Options) (http.Handler, error) {
	if options.ScreensaverDirectory != "" && !filepath.IsAbs(options.ScreensaverDirectory) {
		return nil, errors.New("screen saver directory must be absolute")
	}
	if options.CompanionDirectory != "" && !filepath.IsAbs(options.CompanionDirectory) {
		return nil, errors.New("companion directory must be absolute")
	}
	assets, err := fs.Sub(embedded, "dist")
	if err != nil {
		return nil, err
	}
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		return nil, errors.New("embedded web UI is not built")
	}
	return &handler{
		api: api, assets: assets, index: index,
		screensaverDirectory: options.ScreensaverDirectory, companionDirectory: options.CompanionDirectory,
	}, nil
}

// desktopPolicy is the desktop's Content Security Policy. Installed apps are
// served from other ports of the same address, which the browser counts as
// the same site, so no other origin may frame the desktop (issue #161). Images
// stay on the device: Markdown from the assistant must not make the browser
// fetch an outside address (ADR 0024).
const desktopPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; media-src 'self' blob:; connect-src 'self' data: blob:; " +
	"frame-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" || strings.HasPrefix(r.URL.Path, "/api/") {
		// The desktop frames its own file previews, such as PDFs.
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		h.api.ServeHTTP(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", desktopPolicy)
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
	case r.URL.Path == companionAnimationsPath:
		h.serveCompanionManifest(w, r)
	case strings.HasPrefix(r.URL.Path, companionAnimationsPath+"/"):
		h.serveCompanionAnimation(w, r)
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
	defer func() { _ = video.Close() }()
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

const companionAnimationsPath = "/companion/animations"

// companionAnimationTypes are the formats dsh-pet ships: VP9 with alpha for
// Chromium and Firefox, and HEVC with alpha for Safari.
var companionAnimationTypes = map[string]string{".webm": "video/webm", ".mov": "video/quicktime"}

type companionManifest struct {
	// Animations maps each animation's name to its URL per format.
	Animations map[string]map[string]string `json:"animations"`
}

// serveCompanionManifest lists the animations installed in the current pool,
// so the page plays only those and shows no companion when none are.
func (h *handler) serveCompanionManifest(w http.ResponseWriter, r *http.Request) {
	manifest := companionManifest{Animations: map[string]map[string]string{}}
	for _, name := range h.companionAnimations() {
		extension := filepath.Ext(name)
		animation := strings.TrimSuffix(name, extension)
		if manifest.Animations[animation] == nil {
			manifest.Animations[animation] = map[string]string{}
		}
		manifest.Animations[animation][strings.TrimPrefix(extension, ".")] = companionAnimationsPath + "/" + url.PathEscape(name)
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

func (h *handler) serveCompanionAnimation(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, companionAnimationsPath+"/")
	contentType, ok := companionAnimationTypes[filepath.Ext(name)]
	if !ok || !companionAnimationName(name) || h.companionDirectory == "" {
		http.NotFound(w, r)
		return
	}
	// The pool is a directory of hard links; nothing may lead out of it.
	root, err := os.OpenRoot(h.companionDirectory)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = root.Close() }()
	animation, err := root.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = animation.Close() }()
	info, err := animation.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	// A new pool brings new files, so revalidating by modification time is enough.
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", contentType)
	http.ServeContent(w, r, info.Name(), info.ModTime(), animation)
}

// companionAnimationName accepts a plain file name in the pool: no directory,
// no hidden file and no control characters.
func companionAnimationName(name string) bool {
	if name == "" || strings.HasPrefix(name, ".") || strings.ContainsAny(name, `/\`) {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	_, ok := companionAnimationTypes[filepath.Ext(name)]
	return ok
}

func (h *handler) companionAnimations() []string {
	if h.companionDirectory == "" {
		return nil
	}
	entries, err := os.ReadDir(h.companionDirectory)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || !companionAnimationName(entry.Name()) {
			continue
		}
		names = append(names, entry.Name())
	}
	return names
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
