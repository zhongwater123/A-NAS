package taskicon

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/zhongwater123/A-NAS/internal/taskmanager"
)

const (
	// fileLimit bounds an installed icon file read; Normalize takes less.
	fileLimit = 512 << 10
	// payloadLimit bounds the data URLs of one snapshot.
	payloadLimit = 1 << 20
	// cacheAge is how long a file's icon, or its lack of one, is kept.
	cacheAge = 5 * time.Minute
	// cacheLimit bounds the files remembered between refreshes.
	cacheLimit = 1024
)

var errNotIcon = errors.New("not an installed icon file")

// Loader turns a snapshot's IconFiles into Icons. Files are read from
// taskmanager.IconRoots through os.Root, so a symlink cannot leave them, and
// each file's result is cached, so an invalid file is not decoded on every
// refresh. The zero Loader is ready to use.
type Loader struct {
	// ReadFile reads an installed icon file by its absolute name; nil reads it
	// below taskmanager.IconRoots.
	ReadFile func(name string) ([]byte, error)
	// Now defaults to time.Now.
	Now func() time.Time

	mu    sync.Mutex
	at    time.Time
	cache map[string]icon
}

type icon struct{ id, dataURL string }

// Resolve replaces s.IconFiles with the icons that load, and points each
// row at its icon, or at none.
func (l *Loader) Resolve(s *taskmanager.TaskSnapshot) {
	if len(s.IconFiles) == 0 {
		s.IconFiles = nil
		return
	}
	l.mu.Lock()
	now := l.now()
	if l.cache == nil || now.Before(l.at) || now.Sub(l.at) >= cacheAge || len(l.cache) >= cacheLimit {
		l.cache, l.at = map[string]icon{}, now
	}
	l.mu.Unlock()
	read := l.ReadFile
	if read == nil {
		var roots []root
		opened := false
		defer func() { closeRoots(roots) }()
		read = func(name string) ([]byte, error) {
			if !opened {
				roots, opened = openRoots(), true
			}
			return readInstalled(roots, name)
		}
	}
	icons := map[string]string{}
	resolved := map[string]string{}
	bytes := 0
	ids := make([]string, 0, len(s.IconFiles))
	for id := range s.IconFiles {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, fileID := range ids {
		loaded := l.load(read, s.IconFiles[fileID])
		if loaded.id == "" {
			continue
		}
		if _, exists := icons[loaded.id]; !exists {
			if len(icons) >= taskmanager.MaxIcons || bytes+len(loaded.dataURL) > payloadLimit {
				continue
			}
			icons[loaded.id] = loaded.dataURL
			bytes += len(loaded.dataURL)
		}
		resolved[fileID] = loaded.id
	}
	for _, group := range []*taskmanager.TaskGroup{&s.Processes, &s.Services} {
		for i := range group.Items {
			if row := &group.Items[i]; row.IconID != "" {
				row.IconID = resolved[row.IconID]
			}
		}
	}
	s.IconFiles = nil
	s.Icons = nil
	if len(icons) != 0 {
		s.Icons = icons
	}
}

func (l *Loader) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// load returns the icon of the first file that holds one: a small list of
// candidates lets an invalid SVG or a missing file fall back to another
// installed size or format.
func (l *Loader) load(read func(string) ([]byte, error), files []string) icon {
	for _, name := range files {
		// Only the cache is locked: concurrent refreshes decode in parallel,
		// at worst the same file twice.
		l.mu.Lock()
		loaded, cached := l.cache[name]
		l.mu.Unlock()
		if !cached {
			if data, err := read(name); err == nil {
				loaded.id, loaded.dataURL = Normalize(data)
			}
			l.mu.Lock()
			l.cache[name] = loaded
			l.mu.Unlock()
		}
		if loaded.id != "" {
			return loaded
		}
	}
	return icon{}
}

type root struct {
	prefix string
	root   *os.Root
}

func openRoots() []root {
	var roots []root
	for _, prefix := range taskmanager.IconRoots {
		if opened, err := os.OpenRoot(prefix); err == nil {
			roots = append(roots, root{prefix: prefix, root: opened})
		}
	}
	return roots
}

func closeRoots(roots []root) {
	for _, r := range roots {
		_ = r.root.Close()
	}
}

func readInstalled(roots []root, name string) ([]byte, error) {
	if !taskmanager.IconFile(name) {
		return nil, errNotIcon
	}
	for _, r := range roots {
		rest, ok := strings.CutPrefix(name, r.prefix+"/")
		if !ok {
			continue
		}
		return readLimited(r.root.FS(), rest, fileLimit)
	}
	return nil, fs.ErrNotExist
}

// readLimited reads a regular file of at most limit bytes.
func readLimited(fsys fs.FS, name string, limit int) ([]byte, error) {
	file, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > int64(limit) {
		return nil, errNotIcon
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if len(data) > limit {
		return nil, errNotIcon
	}
	return data, err
}
