package taskicon

import (
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/clocktest"
	"github.com/zhongwater123/A-NAS/internal/taskmanager"
)

const testSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="64" height="64"><rect width="64" height="64" fill="red"/></svg>`

func TestResolveFallsBackToTheNextFileAndPointsRowsAtTheIcon(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"/usr/share/icons/hicolor/scalable/apps/player.svg": "not an image",
		"/usr/share/pixmaps/player.svg":                     testSVG,
	}
	reads := map[string]int{}
	loader := &Loader{ReadFile: func(name string) ([]byte, error) {
		reads[name]++
		data, ok := files[name]
		if !ok {
			return nil, fs.ErrNotExist
		}
		return []byte(data), nil
	}}
	player, missing := strings.Repeat("a", 64), strings.Repeat("b", 64)
	snapshot := func() taskmanager.TaskSnapshot {
		return taskmanager.TaskSnapshot{
			IconFiles: map[string][]string{
				player:  {"/usr/share/icons/hicolor/scalable/apps/player.svg", "/usr/share/pixmaps/player.svg"},
				missing: {"/usr/share/pixmaps/gone.png"},
			},
			Processes: taskmanager.TaskGroup{Items: []taskmanager.Task{{IconID: player}, {IconID: missing}, {}}},
			Services:  taskmanager.TaskGroup{Items: []taskmanager.Task{{IconID: player}}},
		}
	}
	got := snapshot()
	loader.Resolve(&got)
	want, source := Normalize([]byte(testSVG))
	if got.IconFiles != nil || len(got.Icons) != 1 || got.Icons[want] != source {
		t.Fatalf("Resolve() icons = %v, files = %v", got.Icons, got.IconFiles)
	}
	if rows := got.Processes.Items; rows[0].IconID != want || rows[1].IconID != "" || rows[2].IconID != "" || got.Services.Items[0].IconID != want {
		t.Fatalf("Resolve() rows = %+v, %+v", got.Processes.Items, got.Services.Items)
	}
	// Results, including failures, are kept: the next refresh reads nothing.
	again := snapshot()
	loader.Resolve(&again)
	for name, count := range reads {
		if count != 1 {
			t.Fatalf("%s read %d times, want once", name, count)
		}
	}
}

func TestResolveReadsFilesAgainAfterTheCacheAge(t *testing.T) {
	t.Parallel()
	clock := clocktest.New(time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC))
	reads := 0
	loader := &Loader{Now: clock.Now, ReadFile: func(string) ([]byte, error) {
		reads++
		return []byte(testSVG), nil
	}}
	id := strings.Repeat("a", 64)
	resolve := func() {
		snapshot := taskmanager.TaskSnapshot{IconFiles: map[string][]string{id: {"/usr/share/pixmaps/player.svg"}}}
		loader.Resolve(&snapshot)
	}
	resolve()
	clock.Advance(cacheAge - time.Second)
	resolve()
	if reads != 1 {
		t.Fatalf("read %d times within the cache age, want once", reads)
	}
	clock.Advance(time.Second)
	resolve()
	if reads != 2 {
		t.Fatalf("read %d times after the cache age, want twice", reads)
	}
}

func TestInstalledFilesAreReadOnlyBelowTheIconRoots(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"/home/member/private.svg", "/usr/share/../../etc/shadow", "usr/share/x.svg", "/usr/sharex/x.svg"} {
		if _, err := readInstalled(nil, name); err == nil {
			t.Fatalf("readInstalled(%q) succeeded", name)
		}
	}
}
