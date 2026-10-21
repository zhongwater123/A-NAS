package files_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
)

// walkingSnapshots lists the live space as a snapshot, so that taking one
// measures the catalog's side alone.
type walkingSnapshots struct{ root string }

func (walkingSnapshots) Create(context.Context, string, string) error { return nil }

func (w walkingSnapshots) Objects(ctx context.Context, _ string, visit func(files.SnapshotObject) error) error {
	return files.WalkSnapshot(ctx, w.root, visit)
}

func (walkingSnapshots) Open(context.Context, string, string) (io.ReadCloser, files.SnapshotObject, error) {
	return nil, files.SnapshotObject{}, files.ErrNotFound
}

func (walkingSnapshots) Delete(context.Context, string, string) error { return nil }

func (walkingSnapshots) Prune(context.Context, []string) (files.SnapshotPrune, error) {
	return files.SnapshotPrune{}, nil
}

// TestReconcileAtScale measures listing a folder, a tree below a folder (as
// the media center and music read their libraries), passes over the whole
// space and taking a snapshot, on a space of ANAS_SCALE_ENTRIES entries
// (#167). It is skipped unless the variable is set:
//
//	ANAS_SCALE_ENTRIES=100000 go test ./internal/files/ -run AtScale -v
func TestReconcileAtScale(t *testing.T) {
	total, _ := strconv.Atoi(os.Getenv("ANAS_SCALE_ENTRIES"))
	if total <= 0 {
		t.Skip("set ANAS_SCALE_ENTRIES to the number of entries to measure with")
	}
	ctx := context.Background()
	accountStore, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = accountStore.Close() })
	accountService := accounts.NewService(accountStore, acceptingCredentials{}, accounts.Options{})
	owner, err := accountService.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	space := findPrivateSpace(t, mustSpaces(t, accountService, owner))
	catalogPath := filepath.Join(t.TempDir(), "files.db")
	catalog, err := files.OpenSQLite(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	volume := filepath.Join(t.TempDir(), "volume")
	root := filepath.Join(volume, "spaces", "private", "owner")
	recorder := &recordingFileSystem{FileSystem: files.NewRootFileSystem(volume, true)}
	service := files.NewService(catalog, volume, accountService, files.Options{
		DisableCapacityReserve: true, AllowUnverifiedVolume: true, FileSystem: recorder,
		SnapshotBackend: walkingSnapshots{root: root},
	})
	writes := countEntryWrites(t, catalogDB(t, catalogPath))

	// 库: 100 folders of 10 folders of files, 小: 20 files, 影视: 10 folders
	// of 100 files.
	create := func(relative string) {
		full := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	perFolder := max(1, (total-1100-1010-21-3)/1000)
	for outer := range 100 {
		for inner := range 10 {
			for index := range perFolder {
				create(fmt.Sprintf("库/%03d/%d/%04d.jpg", outer, inner, index))
			}
		}
	}
	for index := range 20 {
		create(fmt.Sprintf("小/%02d.txt", index))
	}
	for outer := range 10 {
		for index := range 100 {
			create(fmt.Sprintf("影视/%02d/%03d.mkv", outer, index))
		}
	}

	type step struct {
		name    string
		elapsed time.Duration
		scans   []recordedScan
		writes  int
	}
	var steps []step
	measure := func(name string, run func() error) step {
		t.Helper()
		recorder.take()
		before := writes()
		started := time.Now()
		if err := run(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		measured := step{name: name, elapsed: time.Since(started), scans: recorder.take(), writes: writes() - before}
		steps = append(steps, measured)
		return measured
	}
	tree := func(id string) func() error {
		return func() error { _, err := service.Tree(ctx, owner, space.ID, id); return err }
	}
	list := func(id string) func() error {
		return func() error { _, err := service.List(ctx, owner, space.ID, id); return err }
	}
	measure("whole space, first pass", tree(""))
	measure("whole space, unchanged", tree(""))
	measure("list the root", list(""))
	ids := map[string]string{}
	entries, err := service.List(ctx, owner, space.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		ids[entry.Name] = entry.ID
	}
	small := measure("list 小 (20 files)", list(ids["小"]))
	media := measure("tree below 影视 (1,010 entries)", tree(ids["影视"]))
	measure("snapshot", func() error {
		_, err := service.CreateSnapshot(ctx, owner, space.ID, "规模")
		return err
	})

	t.Logf("%-34s %12s %6s %16s %8s", "step", "elapsed (ms)", "scans", "entries scanned", "writes")
	for _, measured := range steps {
		scanned := 0
		for _, scan := range measured.scans {
			scanned += scan.entries
		}
		t.Logf("%-34s %12.1f %6d %16d %8d", measured.name, float64(measured.elapsed.Microseconds())/1000, len(measured.scans), scanned, measured.writes)
	}
	if len(small.scans) != 1 || small.scans[0].entries != 20 || small.writes != 0 {
		t.Fatalf("listing 小 scanned %+v and wrote %d rows, want 20 entries and none", small.scans, small.writes)
	}
	if len(media.scans) != 1 || media.scans[0].entries != 1010 || media.writes != 0 {
		t.Fatalf("Tree(影视) scanned %+v and wrote %d rows, want 1,010 entries and none", media.scans, media.writes)
	}
}
