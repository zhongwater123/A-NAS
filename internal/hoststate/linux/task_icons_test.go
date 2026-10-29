package linux

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/zhongwater123/A-NAS/internal/taskmanager"
)

const testTaskSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="64" height="64"><rect width="64" height="64" fill="red"/></svg>`

func TestTaskDesktopExecutable(t *testing.T) {
	for _, tt := range []struct{ exec, want string }{
		{`/usr/bin/firefox %u`, "/usr/bin/firefox"},
		{`"/opt/My App/bin/app" --profile %F`, "/opt/My App/bin/app"},
		{`env MODE=desktop /usr/bin/player %U`, "/usr/bin/player"},
		{`/usr/bin/env -- /usr/bin/player`, "/usr/bin/player"},
		{`sh -c "firefox"`, ""},
		{`python3 /opt/app/main.py`, ""},
		{`lua /opt/app/main.lua`, ""},
		{`lua5.4 /opt/app/main.lua`, ""},
		{`./player`, ""},
		{`python3.12 /opt/app/main.py`, ""},
		{`python-tools %F`, "python-tools"},
		{`env -S "firefox"`, ""},
		{`"/usr/bin/firefox`, ""},
		{`%f`, ""},
	} {
		if got := taskDesktopExecutable(tt.exec); got != tt.want {
			t.Errorf("%q: got %q want %q", tt.exec, got, tt.want)
		}
	}
}

func TestTaskIconsUseInstalledDesktopAndShareAssets(t *testing.T) {
	r, proc, now := taskIconFixture(t)
	proc["proc/42/stat"] = &fstest.MapFile{Data: processStat(42, 10, 20, 3)}
	proc["proc/42/io"] = &fstest.MapFile{Data: []byte("read_bytes: 0\nwrite_bytes: 0\n")}
	share := fstest.MapFS{
		"applications/player.desktop":            {Data: []byte("[Desktop Entry]\nType=Application\nExec=/usr/bin/player %U\nIcon=player\n")},
		"icons/hicolor/scalable/apps/player.svg": {Data: []byte(testTaskSVG)},
	}
	opened := 0
	r.dependencies.openTaskIconRoots = func() []taskIconRoot { opened++; return []taskIconRoot{{prefix: "/usr/share", fs: share}} }
	r.dependencies.readTaskExe = func(pid int) (string, error) {
		if pid != 42 {
			return "", fs.ErrNotExist
		}
		return "/usr/bin/player", nil
	}
	r.dependencies.readTaskServices = func(context.Context) ([]byte, error) {
		return []byte("Id=player.service\nActiveState=active\nSubState=running\nMainPID=42\nInvocationID=instance\n"), nil
	}
	got, err := r.ReadTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Processes.Items[0].IconID == "" || got.Services.Items[0].IconID != got.Processes.Items[0].IconID || len(got.IconFiles) != 1 {
		t.Fatalf("icons: %+v", got)
	}
	// The Host Agent names the installed file; the Product Service decodes it.
	if files := got.IconFiles[got.Processes.Items[0].IconID]; len(files) != 1 || files[0] != "/usr/share/icons/hicolor/scalable/apps/player.svg" {
		t.Fatalf("icon files: %v", files)
	}
	if got.Services.Items[0].PID != 0 {
		t.Fatal("internal service MainPID leaked")
	}
	initial := opened
	*now = now.Add(2 * time.Second)
	if _, err := r.ReadTasks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if opened != initial {
		t.Fatal("cached icon caused filesystem rescan")
	}
	*now = now.Add(6 * time.Minute)
	if _, err := r.ReadTasks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if opened == initial {
		t.Fatal("stale icon metadata not refreshed")
	}
}

func TestTaskIconsExactMatchAndUnsafeFilesFallback(t *testing.T) {
	for _, tt := range []struct{ name, exec, icon, data string }{
		{"substring", "player-helper", "player", testTaskSVG},
		{"traversal", "player", "../../private", testTaskSVG},
		{"outside", "player", "/home/member/private.svg", testTaskSVG},
		{"oversized", "player", "player", strings.Repeat("x", taskIconFileLimit+1)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, _, _ := taskIconFixture(t)
			share := fstest.MapFS{"applications/player.desktop": {Data: []byte("[Desktop Entry]\nType=Application\nExec=player\nIcon=" + tt.icon + "\n")}, "icons/hicolor/scalable/apps/player.svg": {Data: []byte(tt.data)}}
			r.dependencies.openTaskIconRoots = func() []taskIconRoot { return []taskIconRoot{{prefix: "/usr/share", fs: share}} }
			r.dependencies.readTaskExe = func(int) (string, error) { return "/usr/bin/" + tt.exec, nil }
			got := taskmanager.TaskSnapshot{Processes: taskmanager.TaskGroup{Items: []taskmanager.Task{{PID: 42}}}}
			r.attachTaskIcons(context.Background(), &got, nil)
			if got.Processes.Items[0].IconID != "" || len(got.IconFiles) != 0 {
				t.Fatalf("unsafe/unmatched icon returned: %+v", got)
			}
		})
	}
}

func TestTaskIconRootRejectsSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.svg")
	if err := os.WriteFile(outside, []byte(testTaskSVG), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape.svg")); err != nil {
		t.Skip(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if _, err := readTaskIconFile(root.FS(), "escape.svg", taskIconFileLimit); err == nil {
		t.Fatal("followed symlink outside trusted root")
	}
}

func TestTaskIconsExecutableAliasAndPIDReuse(t *testing.T) {
	r, proc, _ := taskIconFixture(t)
	share := fstest.MapFS{"applications/vim.desktop": {Data: []byte("[Desktop Entry]\nType=Application\nExec=vim %F\nIcon=vim\n")}, "pixmaps/vim.svg": {Data: []byte(testTaskSVG)}}
	r.dependencies.openTaskIconRoots = func() []taskIconRoot { return []taskIconRoot{{prefix: "/usr/share", fs: share}} }
	r.dependencies.resolveTaskExecutable = func(name string) string {
		if name == "vim" {
			return "/usr/bin/vim.basic"
		}
		return ""
	}
	r.dependencies.readTaskExe = func(int) (string, error) { return "/usr/bin/vim.basic", nil }
	proc["proc/42/stat"] = &fstest.MapFile{Data: processStat(42, 10, 20, 3)}
	for _, start := range []int{10, 11} {
		proc["proc/42/stat"].Data = processStat(42, uint64(start), 20, 3)
		got := taskmanager.TaskSnapshot{Processes: taskmanager.TaskGroup{Items: []taskmanager.Task{{ID: "process:42:10", PID: 42}}}}
		r.attachTaskIcons(context.Background(), &got, nil)
		if (got.Processes.Items[0].IconID != "") != (start == 10) {
			t.Fatalf("start %d icon %q", start, got.Processes.Items[0].IconID)
		}
	}
}

func TestTaskIconsCancellationAndAssetLimit(t *testing.T) {
	r, _, _ := taskIconFixture(t)
	share := fstest.MapFS{}
	got := taskmanager.TaskSnapshot{}
	for i := 1; i <= 70; i++ {
		name := fmt.Sprintf("app%d", i)
		share["applications/"+name+".desktop"] = &fstest.MapFile{Data: []byte("[Desktop Entry]\nType=Application\nExec=" + name + "\nIcon=" + name + "\n")}
		share["pixmaps/"+name+".svg"] = &fstest.MapFile{Data: []byte(fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="64" height="64"><rect width="%d" height="64"/></svg>`, i))}
		got.Processes.Items = append(got.Processes.Items, taskmanager.Task{PID: i})
	}
	r.dependencies.openTaskIconRoots = func() []taskIconRoot { return []taskIconRoot{{prefix: "/usr/share", fs: share}} }
	r.dependencies.readTaskExe = func(pid int) (string, error) { return fmt.Sprintf("/usr/bin/app%d", pid), nil }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.attachTaskIcons(ctx, &got, nil)
	if len(got.IconFiles) != 0 {
		t.Fatal("cancelled read returned icons")
	}
	r.attachTaskIcons(context.Background(), &got, nil)
	if len(got.IconFiles) != 64 {
		t.Fatalf("asset cap: %d", len(got.IconFiles))
	}
}

type slowUnrelatedIconFS struct {
	fs.FS
	calls int
}

func (s *slowUnrelatedIconFS) Open(name string) (fs.File, error) {
	if name == "icons" {
		s.calls++
		time.Sleep(550 * time.Millisecond)
	}
	return s.FS.Open(name)
}

func TestTaskIconsKnownArtworkDoesNotScanUnrelatedThemes(t *testing.T) {
	r, _, _ := taskIconFixture(t)
	share := &slowUnrelatedIconFS{FS: fstest.MapFS{
		"applications/vim.desktop":          {Data: []byte("[Desktop Entry]\nType=Application\nExec=vim\nIcon=gvim\n")},
		"icons/hicolor/48x48/apps/gvim.svg": {Data: []byte(testTaskSVG)},
	}}
	r.dependencies.openTaskIconRoots = func() []taskIconRoot { return []taskIconRoot{{prefix: "/usr/share", fs: share}} }
	r.dependencies.readTaskExe = func(int) (string, error) { return "/usr/bin/vim", nil }
	got := taskmanager.TaskSnapshot{Processes: taskmanager.TaskGroup{Items: []taskmanager.Task{{PID: 42}}}}
	r.attachTaskIcons(context.Background(), &got, nil)
	if got.Processes.Items[0].IconID == "" {
		t.Fatal("unrelated theme scan consumed time needed to attach installed artwork")
	}
	if share.calls != 0 {
		t.Fatal("scanned unrelated themes after finding all referenced artwork")
	}
}

func taskIconFixture(t *testing.T) (*Reader, fstest.MapFS, *time.Time) {
	r, proc, now := taskFixture(t)
	r.dependencies.resolveTaskExecutable = func(name string) string {
		if strings.HasPrefix(name, "/") {
			return name
		}
		return "/usr/bin/" + name
	}
	return r, proc, now
}

func TestTaskIconsRequireFullExecutableIdentity(t *testing.T) {
	for _, tt := range []struct {
		name, desktop, actual string
		unresolved, want      bool
	}{
		{"same vendor", "/opt/vendor-a/client", "/opt/vendor-a/client", false, true},
		{"other vendor", "/opt/vendor-a/client", "/opt/vendor-b/client", false, false},
		{"same installed command", "/usr/bin/player", "/usr/bin/player", false, true},
		{"home impersonation", "/usr/bin/player", "/home/member/player", false, false},
		{"resolved command", "player", "/usr/bin/player", false, true},
		{"unresolved command", "player", "/usr/bin/player", true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, _, _ := taskIconFixture(t)
			if tt.unresolved {
				r.dependencies.resolveTaskExecutable = func(string) string { return "" }
			}
			share := fstest.MapFS{"applications/player.desktop": {Data: []byte("[Desktop Entry]\nType=Application\nExec=" + tt.desktop + "\nIcon=player\n")}, "pixmaps/player.svg": {Data: []byte(testTaskSVG)}}
			r.dependencies.openTaskIconRoots = func() []taskIconRoot { return []taskIconRoot{{prefix: "/usr/share", fs: share}} }
			r.dependencies.readTaskExe = func(int) (string, error) { return tt.actual, nil }
			got := taskmanager.TaskSnapshot{Processes: taskmanager.TaskGroup{Items: []taskmanager.Task{{PID: 42}}}}
			r.attachTaskIcons(context.Background(), &got, nil)
			if (got.Processes.Items[0].IconID != "") != tt.want {
				t.Fatalf("desktop %q actual %q got icon %q", tt.desktop, tt.actual, got.Processes.Items[0].IconID)
			}
		})
	}
}
