package linux

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/taskmanager"
)

const (
	taskIconFileLimit    = 512 << 10
	taskDesktopFileLimit = 64 << 10
	taskIconCacheAge     = 5 * time.Minute
	taskIconAssetLimit   = 64
	taskIconPayloadLimit = 1 << 20
	taskIconURLLimit     = 24576
)

type taskIconRoot struct {
	prefix string
	fs     fs.FS
	close  func() error
}
type taskIconLocation struct {
	root string
	name string
}
type taskIconAsset struct{ id, data string }
type taskIconCache struct {
	at        time.Time
	desktops  map[string]string
	locations map[string][]taskIconLocation
	assets    map[string]taskIconAsset
	bytes     int
}

func openTaskIconRoots() []taskIconRoot {
	var roots []taskIconRoot
	for _, prefix := range []string{"/usr/local/share", "/usr/share"} {
		root, err := os.OpenRoot(prefix)
		if err == nil {
			roots = append(roots, taskIconRoot{prefix: prefix, fs: root.FS(), close: root.Close})
		}
	}
	return roots
}

func readTaskExe(pid int) (string, error) {
	if pid <= 0 {
		return "", fs.ErrInvalid
	}
	return os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
}

// Follow installed command aliases (for example vim -> /etc/alternatives/vim
// -> /usr/bin/vim.basic). This reads only symlink metadata in fixed system
// directories; it never opens executables or follows aliases into user storage.
func resolveTaskExecutable(name string) string {
	if name == "" || (!path.IsAbs(name) && path.Base(name) != name) {
		return ""
	}
	dirs := []string{"/usr/local/bin", "/usr/local/sbin", "/usr/bin", "/usr/sbin", "/bin", "/sbin"}
	allowed := func(file string) bool {
		dir := path.Dir(file)
		if dir == "/etc/alternatives" {
			return true
		}
		for _, candidate := range dirs {
			if dir == candidate {
				return true
			}
		}
		return false
	}
	candidates := []string{name}
	if !path.IsAbs(name) {
		candidates = nil
		for _, dir := range dirs {
			candidates = append(candidates, dir+"/"+name)
		}
	}
	for _, candidate := range candidates {
		current := path.Clean(candidate)
		for hop := 0; hop < 16 && allowed(current); hop++ {
			// Debian's /bin and /sbin may themselves be usrmerge symlinks.
			dir := path.Dir(current)
			if target, err := os.Readlink(dir); err == nil {
				if !path.IsAbs(target) {
					target = path.Join(path.Dir(dir), target)
				}
				current = path.Join(target, path.Base(current))
				if !allowed(current) {
					break
				}
			}
			info, err := os.Lstat(current)
			if err != nil {
				break
			}
			if info.Mode()&os.ModeSymlink == 0 {
				if info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
					return current
				}
				break
			}
			target, err := os.Readlink(current)
			if err != nil {
				break
			}
			if !path.IsAbs(target) {
				target = path.Join(path.Dir(current), target)
			}
			current = path.Clean(target)
		}
	}
	return ""
}

// attachTaskIcons runs after measurement and under the workload gate. Failure
// only removes decoration; it never changes availability or measured counters.
func (r *Reader) attachTaskIcons(ctx context.Context, result *taskmanager.TaskSnapshot, services map[string]taskUsage) {
	if r.dependencies.openTaskIconRoots == nil || r.dependencies.readTaskExe == nil || ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	cache := &r.workloadTasks.icons
	var roots []taskIconRoot
	opened := false
	open := func() []taskIconRoot {
		if !opened {
			roots = r.dependencies.openTaskIconRoots()
			opened = true
		}
		return roots
	}
	defer func() {
		for _, root := range roots {
			if root.close != nil {
				_ = root.close()
			}
		}
	}()
	now := r.dependencies.now()
	if cache.desktops == nil || now.Before(cache.at) || now.Sub(cache.at) >= taskIconCacheAge {
		*cache = taskIconCache{at: now, desktops: map[string]string{}, locations: map[string][]taskIconLocation{}, assets: map[string]taskIconAsset{}}
		// Theme directories can contain many unrelated assets. Reserve time for
		// attaching icons already found rather than spending the entire optional
		// decoration budget discovering additional theme candidates.
		discoveryCtx, stopDiscovery := context.WithTimeout(ctx, 200*time.Millisecond)
		cache.discover(discoveryCtx, open(), r.dependencies.resolveTaskExecutable)
		stopDiscovery()
	}
	assets := map[string]string{}
	bytes := 0
	attach := func(row *taskmanager.Task, pid int) {
		if ctx.Err() != nil || pid <= 0 {
			return
		}
		exe, err := r.dependencies.readTaskExe(pid)
		if err != nil || !strings.HasPrefix(exe, "/") || strings.HasSuffix(exe, " (deleted)") {
			return
		}
		if strings.HasPrefix(row.ID, "process:") {
			data, err := readTaskFile(r.dependencies.root, "proc/"+strconv.Itoa(pid)+"/stat")
			if err != nil {
				return
			}
			current, err := parseTaskProcess(data, pid)
			if err != nil || current.row.ID != row.ID {
				return
			}
		}
		icon, ok := cache.desktops[exe]
		if !ok {
			return
		}
		asset, cached := cache.assets[icon]
		if !cached {
			// Cache misses too, so an invalid installed image is not repeatedly decoded.
			asset = cache.load(ctx, open(), icon)
			cache.assets[icon] = asset
		}
		if asset.id == "" {
			return
		}
		if _, exists := assets[asset.id]; !exists {
			if len(assets) >= taskIconAssetLimit || bytes+len(asset.data) > taskIconPayloadLimit {
				return
			}
			assets[asset.id] = asset.data
			bytes += len(asset.data)
		}
		row.IconID = asset.id
	}
	for i := range result.Processes.Items {
		attach(&result.Processes.Items[i], result.Processes.Items[i].PID)
	}
	for i := range result.Services.Items {
		attach(&result.Services.Items[i], services[result.Services.Items[i].ID].mainPID)
	}
	if len(assets) != 0 {
		result.Icons = assets
	}
}

func (c *taskIconCache) discover(ctx context.Context, roots []taskIconRoot, resolve func(string) string) {
	desktopBudget, entryBudget := 1024, 20000
	for _, root := range roots {
		entries, _, _ := taskDirectories(root.fs, "applications", desktopBudget)
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			if ctx.Err() != nil || desktopBudget <= 0 {
				break
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".desktop") {
				continue
			}
			desktopBudget--
			data, err := readTaskIconFile(root.fs, "applications/"+entry.Name(), taskDesktopFileLimit)
			if err != nil {
				continue
			}
			exe, icon := parseTaskDesktop(data)
			if exe != "" && icon != "" {
				if path.IsAbs(exe) {
					if _, found := c.desktops[exe]; !found {
						c.desktops[exe] = icon
					}
				}
				if resolve != nil {
					if alias := resolve(exe); path.IsAbs(alias) {
						if _, found := c.desktops[alias]; !found {
							c.desktops[alias] = icon
						}
					}
				}
			}
		}
		// Only installed theme/pixmap trees are enumerated. Directory symlinks
		// are never followed, and os.Root confines image symlinks to share.
		var walk func(string, int)
		walk = func(dir string, depth int) {
			if depth > 6 || entryBudget <= 0 || ctx.Err() != nil {
				return
			}
			entries, _, _ := taskDirectories(root.fs, dir, entryBudget)
			entryBudget -= len(entries)
			sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
			for _, entry := range entries {
				if ctx.Err() != nil {
					return
				}
				name := dir + "/" + entry.Name()
				if entry.IsDir() {
					walk(name, depth+1)
					continue
				}
				ext := strings.ToLower(path.Ext(name))
				if ext != ".png" && ext != ".jpg" && ext != ".jpeg" && ext != ".webp" && ext != ".svg" {
					continue
				}
				location := taskIconLocation{root: root.prefix, name: name}
				base := strings.TrimSuffix(entry.Name(), path.Ext(entry.Name()))
				// A small candidate list permits a safe fallback from invalid SVG
				// or missing symlinks to another installed size/format.
				if len(c.locations[base]) < 8 {
					c.locations[base] = append(c.locations[base], location)
				}
				if len(c.locations[entry.Name()]) < 8 {
					c.locations[entry.Name()] = append(c.locations[entry.Name()], location)
				}
			}
		}
		walk("icons/hicolor", 0)
		walk("pixmaps", 0)
		if !c.referencedIconsFound() {
			walk("icons", 0)
		}
	}
}

func (c *taskIconCache) referencedIconsFound() bool {
	for _, icon := range c.desktops {
		if !strings.HasPrefix(icon, "/") && len(c.locations[icon]) == 0 {
			return false
		}
	}
	return true
}

func (c *taskIconCache) load(ctx context.Context, roots []taskIconRoot, icon string) taskIconAsset {
	if len(c.assets) >= 1024 || c.bytes >= taskIconPayloadLimit {
		return taskIconAsset{}
	}
	locations := c.locations[icon]
	if strings.HasPrefix(icon, "/") {
		locations = nil
		for _, root := range roots {
			if name, ok := strings.CutPrefix(icon, root.prefix+"/"); ok && fs.ValidPath(name) {
				locations = append(locations, taskIconLocation{root: root.prefix, name: name})
			}
		}
	}
	for _, location := range locations {
		if ctx.Err() != nil {
			break
		}
		var source fs.FS
		for _, root := range roots {
			if root.prefix == location.root {
				source = root.fs
				break
			}
		}
		if source == nil {
			continue
		}
		data, err := readTaskIconFile(source, location.name, taskIconFileLimit)
		if err != nil {
			continue
		}
		id, url := taskmanager.NormalizeIcon(data)
		if id == "" || len(url) > taskIconURLLimit || c.bytes+len(url) > taskIconPayloadLimit {
			continue
		}
		c.bytes += len(url)
		return taskIconAsset{id: id, data: url}
	}
	return taskIconAsset{}
}

func readTaskIconFile(root fs.FS, name string, limit int) ([]byte, error) {
	if !fs.ValidPath(name) {
		return nil, fs.ErrInvalid
	}
	info, err := fs.Stat(root, name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > int64(limit) {
		return nil, errTaskObservationLimit
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > int64(limit) {
		return nil, errTaskObservationLimit
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if len(data) > limit {
		return nil, errTaskObservationLimit
	}
	return data, err
}

func parseTaskDesktop(data []byte) (string, string) {
	section := ""
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			section = line
			continue
		}
		if section != "[Desktop Entry]" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	if values["Type"] != "Application" || values["Hidden"] == "true" {
		return "", ""
	}
	icon := values["Icon"]
	if icon == "" || len(icon) > 512 || strings.ContainsAny(icon, "\x00\\") || (!strings.HasPrefix(icon, "/") && strings.Contains(icon, "/")) {
		return "", ""
	}
	return taskDesktopExecutable(values["Exec"]), icon
}

// Parse desktop Exec syntax as data, never through a shell. Interpreter/wrapper
// entries cannot identify an app from its interpreter and deliberately fall back.
func taskDesktopExecutable(value string) string {
	words, err := taskDesktopWords(value)
	if err != nil || len(words) == 0 {
		return ""
	}
	if path.Base(words[0]) == "env" {
		words = words[1:]
		for len(words) > 0 && strings.Contains(words[0], "=") && !strings.HasPrefix(words[0], "-") {
			words = words[1:]
		}
		if len(words) > 0 && words[0] == "--" {
			words = words[1:]
		}
	}
	if len(words) == 0 || strings.HasPrefix(words[0], "-") || strings.ContainsAny(words[0], "%$`;") {
		return ""
	}
	name := path.Base(words[0])
	for _, interpreter := range []string{"python", "perl", "ruby", "lua", "node", "java"} {
		if version, ok := strings.CutPrefix(name, interpreter); ok && version != "" {
			versionOnly := true
			for _, ch := range version {
				if (ch < '0' || ch > '9') && ch != '.' {
					versionOnly = false
					break
				}
			}
			if versionOnly {
				return ""
			}
		}
	}
	switch name {
	case "sh", "bash", "dash", "zsh", "env", "python", "python2", "python3", "perl", "ruby", "lua", "node", "java", "flatpak", "snap":
		return ""
	}
	if name == "." || name == "/" || (!path.IsAbs(words[0]) && strings.Contains(words[0], "/")) {
		return ""
	}
	return path.Clean(words[0])
}

func taskDesktopWords(value string) ([]string, error) {
	var words []string
	var word strings.Builder
	quoted, escaped := false, false
	for _, char := range value {
		if escaped {
			word.WriteRune(char)
			escaped = false
			continue
		}
		switch char {
		case '\\':
			escaped = true
		case '"':
			quoted = !quoted
		case ' ', '\t':
			if quoted {
				word.WriteRune(char)
			} else if word.Len() > 0 {
				words = append(words, word.String())
				word.Reset()
			}
		case '\n', '\r', '\x00':
			return nil, errors.New("invalid desktop Exec")
		default:
			word.WriteRune(char)
		}
	}
	if quoted || escaped {
		return nil, errors.New("incomplete desktop Exec")
	}
	if word.Len() > 0 {
		words = append(words, word.String())
	}
	return words, nil
}
