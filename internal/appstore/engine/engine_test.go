package engine_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/appstore"
	"github.com/zhongwater123/A-NAS/internal/appstore/engine"
)

type fakeDocker struct {
	mu         sync.Mutex
	containers []engine.ContainerInfo
}

func (f *fakeDocker) Containers(context.Context) ([]engine.ContainerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]engine.ContainerInfo(nil), f.containers...), nil
}

type fakeRunner struct {
	mu    sync.Mutex
	calls [][]string
	err   error
	gate  chan struct{}
	after func(args []string)
}

func (f *fakeRunner) Run(_ context.Context, args []string, output io.Writer) error {
	f.mu.Lock()
	f.calls = append(f.calls, args)
	f.mu.Unlock()
	if f.gate != nil {
		<-f.gate
	}
	for i := 0; i < 45; i++ {
		fmt.Fprintf(output, "step %d\n", i)
	}
	if f.after != nil {
		f.after(args)
	}
	return f.err
}

func newStore(t *testing.T, docker *fakeDocker, runner *fakeRunner) (*engine.Store, string) {
	t.Helper()
	entries, err := appstore.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	policy := appstore.Policy{AppDataRoot: "/srv/a-nas/data/apps", DataRoot: "/srv/a-nas/data/spaces/shared", TZ: "UTC", ReservedPorts: []uint16{8080}}
	return engine.New(entries, policy, state, docker, runner), state
}

func TestInstallRunsTheConfirmedComposeFile(t *testing.T) {
	docker := &fakeDocker{}
	runner := &fakeRunner{after: func([]string) {
		docker.mu.Lock()
		docker.containers = append(docker.containers, engine.ContainerInfo{Name: "uptimekuma", AppID: "uptimekuma", Running: true, PublishedPorts: []uint16{3001}})
		docker.mu.Unlock()
	}}
	store, state := newStore(t, docker, runner)
	ctx := context.Background()

	plan, err := store.Plan(ctx, "uptimekuma", identity)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.Install(ctx, "uptimekuma", plan.Digest, identity)
	if err != nil || job.State != appstore.JobRunning {
		t.Fatalf("Install() = %+v, %v", job, err)
	}
	store.Wait()

	written, err := os.ReadFile(filepath.Join(state, "apps", "uptimekuma", "docker-compose.yml"))
	if err != nil || string(written) != string(plan.Compose) {
		t.Fatalf("compose file was not the confirmed plan: %v", err)
	}
	if got := strings.Join(runner.calls[0], " "); !strings.Contains(got, "-p a-nas-uptimekuma") || !strings.Contains(got, "up --detach") {
		t.Fatalf("runner args = %s", got)
	}
	apps, _ := store.Apps(ctx)
	status := find(apps, "uptimekuma")
	if status.State != appstore.StateInstalled || status.Running != 1 || status.Job.State != appstore.JobSucceeded {
		t.Fatalf("status = %+v job = %+v", status, status.Job)
	}
	if len(status.Job.Output) != 40 || status.Job.Output[39] != "step 44" {
		t.Fatalf("output tail = %v", status.Job.Output)
	}
}

func TestInstallRefusesChangedPlansConflictsAndConcurrentJobs(t *testing.T) {
	docker := &fakeDocker{containers: []engine.ContainerInfo{{Name: "someone-else", PublishedPorts: []uint16{4533}}}}
	runner := &fakeRunner{gate: make(chan struct{})}
	store, _ := newStore(t, docker, runner)
	ctx := context.Background()

	if _, err := store.Install(ctx, "memos", strings.Repeat("0", 64), identity); !errors.Is(err, appstore.ErrPlanChanged) {
		t.Fatalf("stale digest error = %v", err)
	}
	navidrome, _ := store.Plan(ctx, "navidrome", identity)
	if _, err := store.Install(ctx, "navidrome", navidrome.Digest, identity); !errors.Is(err, appstore.ErrPortInUse) {
		t.Fatalf("port conflict error = %v", err)
	}
	if _, err := store.Install(ctx, "missing", "x", identity); !errors.Is(err, appstore.ErrNotFound) {
		t.Fatalf("missing app error = %v", err)
	}

	memos, _ := store.Plan(ctx, "memos", identity)
	if _, err := store.Install(ctx, "memos", memos.Digest, identity); err != nil {
		t.Fatal(err)
	}
	apps, _ := store.Apps(ctx)
	if find(apps, "memos").State != appstore.StateInstalling {
		t.Fatalf("memos state = %s", find(apps, "memos").State)
	}
	uptime, _ := store.Plan(ctx, "uptimekuma", identity)
	if _, err := store.Install(ctx, "uptimekuma", uptime.Digest, identity); !errors.Is(err, appstore.ErrBusy) {
		t.Fatalf("concurrent install error = %v", err)
	}
	close(runner.gate)
	store.Wait()
}

func TestUninstallKeepsDataAndReportsFailures(t *testing.T) {
	docker := &fakeDocker{}
	runner := &fakeRunner{err: errors.New("compose command failed: exit status 1")}
	store, _ := newStore(t, docker, runner)
	ctx := context.Background()

	if _, err := store.Uninstall(ctx, "memos"); !errors.Is(err, appstore.ErrNotInstalled) {
		t.Fatalf("uninstall missing error = %v", err)
	}
	docker.containers = []engine.ContainerInfo{{Name: "memos", AppID: "memos"}}
	if _, err := store.Uninstall(ctx, "memos"); err != nil {
		t.Fatal(err)
	}
	store.Wait()
	args := strings.Join(runner.calls[0], " ")
	if !strings.Contains(args, "-p a-nas-memos down") || strings.Contains(args, "--volumes") {
		t.Fatalf("uninstall args = %s", args)
	}
	apps, _ := store.Apps(ctx)
	if job := find(apps, "memos").Job; job.State != appstore.JobFailed || !strings.Contains(job.Error, "exit status 1") {
		t.Fatalf("job = %+v", job)
	}
}

func find(apps []appstore.AppStatus, id string) appstore.AppStatus {
	for _, app := range apps {
		if app.ID == id {
			return app
		}
	}
	return appstore.AppStatus{}
}

var identity = appstore.Identity{Username: "app-test", UID: 30001, GID: 30001}
