package engine_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/appstore"
	"github.com/zhongwater123/A-NAS/internal/appstore/engine"
)

type fakeDocker struct {
	mu         sync.Mutex
	containers []engine.ContainerInfo
	pools      []netip.Prefix
	networks   []engine.NetworkInfo
	mounts     []engine.MountInfo
	// states overrides what ProjectContainers reports.
	states []engine.ContainerState
	// apiVersion defaults to an engine that supports volume subpaths.
	apiVersion string
}

func (f *fakeDocker) Containers(context.Context) ([]engine.ContainerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]engine.ContainerInfo(nil), f.containers...), nil
}

func (f *fakeDocker) AddressPools(context.Context) ([]netip.Prefix, error) {
	return f.pools, nil
}

func (f *fakeDocker) ProjectNetworks(context.Context, string) ([]engine.NetworkInfo, error) {
	return f.networks, nil
}

func (f *fakeDocker) APIVersion(context.Context) (string, error) {
	if f.apiVersion == "" {
		return "1.52", nil
	}
	return f.apiVersion, nil
}

func (f *fakeDocker) ProjectMounts(context.Context, string) ([]engine.MountInfo, error) {
	return f.mounts, nil
}

// ProjectContainers reports the app's containers as running unless states
// says otherwise.
func (f *fakeDocker) ProjectContainers(_ context.Context, project string) ([]engine.ContainerState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.states != nil {
		return f.states, nil
	}
	var states []engine.ContainerState
	for _, container := range f.containers {
		if appstore.ProjectName(container.AppID) == project {
			states = append(states, engine.ContainerState{Service: container.Name, State: "running"})
		}
	}
	if len(states) == 0 {
		// Tests that do not model the app's containers: one that runs.
		states = []engine.ContainerState{{Service: project, State: "running"}}
	}
	return states, nil
}

type fakeRunner struct {
	mu    sync.Mutex
	calls [][]string
	err   error
	gate  chan struct{}
	after func(args []string)
	// compose defaults to a Compose that keeps volume subpaths.
	compose string
}

func (f *fakeRunner) ComposeVersion(context.Context) (string, error) {
	if f.compose == "" {
		return "2.40.3", nil
	}
	return f.compose, nil
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
	store := engine.New(entries, policy, state, docker, runner)
	store.SetStartupCheck(30*time.Millisecond, 10*time.Millisecond)
	return store, state
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

// On 2026-10-09 an app network got 172.19.0.0/16 from Docker's built-in
// pools and the NAS stopped answering every Wi-Fi client in that range.
func TestAppNetworksNeedAnAddressPool(t *testing.T) {
	docker := &fakeDocker{}
	store, _ := newStore(t, docker, &fakeRunner{})
	ctx := context.Background()

	if _, err := store.Plan(ctx, "audiobookshelf", identity); !errors.Is(err, appstore.ErrAddressPoolMissing) {
		t.Fatalf("plan without a pool error = %v, want ErrAddressPoolMissing", err)
	}
	if _, err := store.Install(ctx, "audiobookshelf", strings.Repeat("0", 64), identity); !errors.Is(err, appstore.ErrAddressPoolMissing) {
		t.Fatalf("install without a pool error = %v, want ErrAddressPoolMissing", err)
	}
	if plan, err := store.Plan(ctx, "memos", identity); err != nil || len(plan.Networks) != 0 {
		t.Fatalf("an app on the default bridge = %v networks, %v; it creates none and needs no pool", plan.Networks, err)
	}

	docker.pools = []netip.Prefix{netip.MustParsePrefix("10.96.64.0/19")}
	plan, err := store.Plan(ctx, "audiobookshelf", identity)
	if err != nil || !slices.Equal(plan.Networks, []string{"default"}) || !slices.Equal(plan.AddressPools, docker.pools) {
		t.Fatalf("plan = networks %v pools %v, %v", plan.Networks, plan.AddressPools, err)
	}
}

func TestInstallRollsBackANetworkOutsideThePool(t *testing.T) {
	docker := &fakeDocker{
		pools:    []netip.Prefix{netip.MustParsePrefix("10.96.64.0/19")},
		networks: []engine.NetworkInfo{{Name: "a-nas-audiobookshelf_default", Subnets: []netip.Prefix{netip.MustParsePrefix("172.19.0.0/16")}}},
	}
	runner := &fakeRunner{}
	store, _ := newStore(t, docker, runner)
	ctx := context.Background()

	plan, err := store.Plan(ctx, "audiobookshelf", identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Install(ctx, "audiobookshelf", plan.Digest, identity); err != nil {
		t.Fatal(err)
	}
	store.Wait()

	if len(runner.calls) != 2 || !strings.Contains(strings.Join(runner.calls[1], " "), "-p a-nas-audiobookshelf -f ") ||
		!strings.HasSuffix(strings.Join(runner.calls[1], " "), "down --remove-orphans") {
		t.Fatalf("runner calls = %v, want up then a rollback down", runner.calls)
	}
	apps, _ := store.Apps(ctx)
	job := find(apps, "audiobookshelf").Job
	if job.State != appstore.JobFailed || !strings.Contains(job.Error, "172.19.0.0/16") || !strings.Contains(job.Error, "rolled back") {
		t.Fatalf("job = %+v, want a failed install naming the network", job)
	}
}

func TestInstallKeepsANetworkInsideThePool(t *testing.T) {
	docker := &fakeDocker{
		pools:    []netip.Prefix{netip.MustParsePrefix("10.96.64.0/19")},
		networks: []engine.NetworkInfo{{Name: "a-nas-audiobookshelf_default", Subnets: []netip.Prefix{netip.MustParsePrefix("10.96.64.0/24")}}},
	}
	runner := &fakeRunner{}
	store, _ := newStore(t, docker, runner)
	ctx := context.Background()

	plan, _ := store.Plan(ctx, "audiobookshelf", identity)
	if _, err := store.Install(ctx, "audiobookshelf", plan.Digest, identity); err != nil {
		t.Fatal(err)
	}
	store.Wait()

	apps, _ := store.Apps(ctx)
	if job := find(apps, "audiobookshelf").Job; len(runner.calls) != 1 || job.State != appstore.JobSucceeded {
		t.Fatalf("calls = %v job = %+v, want one up and success", runner.calls, job)
	}
}

// On 2026-10-09 the Debian Compose 2.26.1 mounted Immich's whole app folder
// as its database directory and the whole Shared folder as its uploads.
func TestPlanRefusesADockerThatDropsVolumeSubpaths(t *testing.T) {
	for _, test := range []struct{ compose, api string }{
		{compose: "2.26.1-4", api: "1.45"},
		{compose: "v2.29.7", api: "1.52"},
		{compose: "2.40.3", api: "1.44"},
	} {
		store, _ := newStore(t, &fakeDocker{apiVersion: test.api}, &fakeRunner{compose: test.compose})
		if _, err := store.Plan(context.Background(), "memos", identity); !errors.Is(err, appstore.ErrRuntimeOutdated) {
			t.Errorf("Compose %s, API %s: plan error = %v, want ErrRuntimeOutdated", test.compose, test.api, err)
		}
	}
	store, _ := newStore(t, &fakeDocker{apiVersion: "1.45"}, &fakeRunner{compose: "v2.30.0"})
	if _, err := store.Plan(context.Background(), "memos", identity); err != nil {
		t.Fatalf("Compose 2.30.0 with API 1.45: plan error = %v", err)
	}
}

func TestInstallRollsBackAMountBeyondItsFolder(t *testing.T) {
	docker := &fakeDocker{}
	runner := &fakeRunner{}
	store, _ := newStore(t, docker, runner)
	ctx := context.Background()
	plan, err := store.Plan(ctx, "memos", identity)
	if err != nil {
		t.Fatal(err)
	}
	folder := plan.Mounts[0]
	if folder.Volume != "a-nas-memos_a-nas-appdata" || folder.Subpath == "" {
		t.Fatalf("planned mount = %+v, want a subpath of the app-data volume", folder)
	}
	docker.mounts = []engine.MountInfo{{Container: "memos", Volume: folder.Volume, Target: folder.ContainerPath}}
	if _, err := store.Install(ctx, "memos", plan.Digest, identity); err != nil {
		t.Fatal(err)
	}
	store.Wait()

	if len(runner.calls) != 2 || !strings.HasSuffix(strings.Join(runner.calls[1], " "), "down --remove-orphans") {
		t.Fatalf("runner calls = %v, want up then a rollback down", runner.calls)
	}
	apps, _ := store.Apps(ctx)
	job := find(apps, "memos").Job
	if job.State != appstore.JobFailed || !strings.Contains(job.Error, "the whole a-nas-memos_a-nas-appdata") || !strings.Contains(job.Error, "rolled back") {
		t.Fatalf("job = %+v, want a failed install naming the whole volume", job)
	}
}

func TestInstallKeepsMountsOnTheirFolders(t *testing.T) {
	docker := &fakeDocker{}
	runner := &fakeRunner{}
	store, _ := newStore(t, docker, runner)
	ctx := context.Background()
	plan, _ := store.Plan(ctx, "memos", identity)
	folder := plan.Mounts[0]
	docker.mounts = []engine.MountInfo{
		{Container: "memos", Volume: folder.Volume, Target: folder.ContainerPath, Subpath: folder.Subpath},
		{Container: "memos", Volume: "someone-elses-volume", Target: "/elsewhere"},
	}
	if _, err := store.Install(ctx, "memos", plan.Digest, identity); err != nil {
		t.Fatal(err)
	}
	store.Wait()

	apps, _ := store.Apps(ctx)
	if job := find(apps, "memos").Job; len(runner.calls) != 1 || job.State != appstore.JobSucceeded {
		t.Fatalf("calls = %v job = %+v, want one up and success", runner.calls, job)
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

// Compose returns once containers start; an app that cannot keep running,
// for example because it cannot write its folder, is a failed install that
// is rolled back, with the reason and its last logs.
func TestInstallFailsWhenAContainerKeepsRestarting(t *testing.T) {
	docker := &fakeDocker{states: []engine.ContainerState{{Service: "n8n", State: "restarting", ExitCode: 1, RestartCount: 3}}}
	runner := &fakeRunner{}
	store, _ := newStore(t, docker, runner)
	ctx := context.Background()
	plan, err := store.Plan(ctx, "n8n", identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Install(ctx, "n8n", plan.Digest, identity); err != nil {
		t.Fatal(err)
	}
	store.Wait()
	apps, _ := store.Apps(ctx)
	job := find(apps, "n8n").Job
	if job.State != appstore.JobFailed || !strings.Contains(job.Error, "容器 n8n 启动后不断重启（退出码 1）") || !strings.Contains(job.Error, "安装已回滚") {
		t.Fatalf("job = %+v", job)
	}
	var verbs []string
	for _, call := range runner.calls {
		for _, verb := range []string{"up", "logs", "down"} {
			if slices.Contains(call, verb) {
				verbs = append(verbs, verb)
			}
		}
	}
	if !slices.Equal(verbs, []string{"up", "logs", "down"}) {
		t.Fatalf("compose ran %v, want up, logs, down", verbs)
	}
}

func TestInstalledAppsSayWhyAContainerIsNotRunning(t *testing.T) {
	docker := &fakeDocker{containers: []engine.ContainerInfo{
		{Name: "a-nas-n8n-n8n-1", AppID: "n8n", Service: "n8n", State: "restarting", ExitCode: 1},
	}}
	store, _ := newStore(t, docker, &fakeRunner{})
	apps, err := store.Apps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	status := find(apps, "n8n")
	if status.Running != 0 || len(status.Problems) != 1 || status.Problems[0] != (appstore.ContainerProblem{Service: "n8n", State: "restarting", ExitCode: 1}) {
		t.Fatalf("status = %+v", status)
	}
}

// An app's logins are drawn once, kept only for the agent, and shown for the
// installed app; an app installed before they were drawn shows the
// manifest's own, marked so.
func TestInstalledAppsShowTheLoginsTheyUse(t *testing.T) {
	docker := &fakeDocker{containers: []engine.ContainerInfo{
		{Name: "a-nas-openlist-openlist-1", AppID: "openlist", Service: "openlist", Running: true},
		{Name: "a-nas-photoprism-photoprism-1", AppID: "photoprism", Service: "photoprism", Running: true},
	}}
	store, state := newStore(t, docker, &fakeRunner{})
	ctx := context.Background()

	plan, err := store.Plan(ctx, "openlist", identity)
	if err != nil || len(plan.Credentials) != 1 || plan.Credentials[0].Upstream {
		t.Fatalf("Plan() credentials = %+v, %v", plan.Credentials, err)
	}
	again, err := store.Plan(ctx, "openlist", identity)
	if err != nil || again.Digest != plan.Digest || again.Credentials[0].Secret != plan.Credentials[0].Secret {
		t.Fatalf("a second plan drew new logins: %v", err)
	}
	info, err := os.Stat(filepath.Join(state, "secrets", "openlist.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("stored secrets = %v, %v", info, err)
	}

	apps, err := store.Apps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := find(apps, "openlist").Credentials; len(got) != 1 || got[0] != plan.Credentials[0] {
		t.Fatalf("openlist credentials = %+v", got)
	}
	legacy := find(apps, "photoprism").Credentials
	if len(legacy) != 1 || !legacy[0].Upstream || legacy[0].Secret != "casaos" {
		t.Fatalf("photoprism credentials = %+v", legacy)
	}
	if got := find(apps, "memos").Credentials; got != nil {
		t.Fatalf("an app that is not installed shows %+v", got)
	}
}
