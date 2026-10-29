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
	cleanupErr       error
	inspectImagesErr error
	imageErr         error
	cleanedImages    []string
	storageErr       error
	mu               sync.Mutex
	containers       []engine.ContainerInfo
	pools            []netip.Prefix
	networks         []engine.NetworkInfo
	mounts           []engine.MountInfo
	runtimes         []engine.RuntimeInfo
	// states overrides what ProjectContainers reports.
	states []engine.ContainerState
	// apiVersion defaults to an engine that supports volume subpaths.
	apiVersion string
}

func (f *fakeDocker) CheckStorage(context.Context) error { return f.storageErr }

func (f *fakeDocker) CleanupImages(_ context.Context, project string, references []string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inspectImagesErr != nil {
		return nil, f.inspectImagesErr
	}
	if slices.ContainsFunc(f.containers, func(item engine.ContainerInfo) bool { return appstore.ProjectName(item.AppID) == project }) {
		return []string{"sha256:installed"}, nil
	}
	return slices.Clone(references), nil
}

func (f *fakeDocker) RemoveProjectContainers(_ context.Context, project string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cleanupErr != nil {
		return f.cleanupErr
	}
	f.containers = slices.DeleteFunc(f.containers, func(item engine.ContainerInfo) bool { return appstore.ProjectName(item.AppID) == project })
	return nil
}

func (f *fakeDocker) RemoveUnusedImages(_ context.Context, images []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleanedImages = slices.Clone(images)
	return f.imageErr
}

func TestInstallRechecksSystemSpaceBeforeRunningCompose(t *testing.T) {
	docker, runner := &fakeDocker{}, &fakeRunner{}
	store, _ := newStore(t, docker, runner)
	plan, err := store.Plan(context.Background(), "memos", identity)
	if err != nil {
		t.Fatal(err)
	}
	docker.storageErr = appstore.ErrSystemSpace
	if _, err := store.Plan(context.Background(), "memos", identity); !errors.Is(err, appstore.ErrSystemSpace) {
		t.Fatalf("plan error = %v", err)
	}
	if _, err := store.Install(context.Background(), "memos", plan.Digest, identity); !errors.Is(err, appstore.ErrSystemSpace) {
		t.Fatalf("install error = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("Compose ran with insufficient storage: %v", runner.calls)
	}
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

func (f *fakeDocker) ProjectRuntimes(context.Context, string) ([]engine.RuntimeInfo, error) {
	return f.runtimes, nil
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
	// fail, when set, gives each call's error in place of err.
	fail func(args []string) error
	// hold makes the calls it picks report on held and then wait until
	// their context ends, as Compose does until it is killed.
	hold func(args []string) bool
	held chan struct{}
	// compose defaults to a Compose that keeps volume subpaths.
	compose string
}

func (f *fakeRunner) ComposeVersion(context.Context) (string, error) {
	if f.compose == "" {
		return "2.40.3", nil
	}
	return f.compose, nil
}

func (f *fakeRunner) Run(ctx context.Context, args []string, output io.Writer) error {
	f.mu.Lock()
	f.calls = append(f.calls, args)
	f.mu.Unlock()
	if f.gate != nil {
		<-f.gate
	}
	for i := range 45 {
		_, _ = fmt.Fprintf(output, "step %d\n", i)
	}
	if f.after != nil {
		f.after(args)
	}
	if f.hold != nil && f.hold(args) {
		f.held <- struct{}{}
		<-ctx.Done()
		return fmt.Errorf("compose command failed: %w", ctx.Err())
	}
	if f.fail != nil {
		return f.fail(args)
	}
	return f.err
}

// verbs lists the Compose commands the runner was given.
func (f *fakeRunner) verbs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var verbs []string
	for _, call := range f.calls {
		for _, verb := range []string{"up", "logs", "down"} {
			if slices.Contains(call, verb) {
				verbs = append(verbs, verb)
			}
		}
	}
	return verbs
}

func newStore(t *testing.T, docker *fakeDocker, runner *fakeRunner) (*engine.Store, string) {
	t.Helper()
	state := t.TempDir()
	return newStoreIn(t, state, docker, runner), state
}

// newStoreIn starts an agent on an existing state directory, as a restart does.
func newStoreIn(t *testing.T, state string, docker *fakeDocker, runner *fakeRunner) *engine.Store {
	t.Helper()
	entries, err := appstore.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	policy := appstore.Policy{AppDataRoot: "/srv/a-nas/data/apps", DataRoot: "/srv/a-nas/data/spaces/shared", TZ: "UTC", ReservedPorts: []uint16{8080}}
	store := engine.New(entries, policy, state, docker, runner)
	store.SetStartupCheck(30*time.Millisecond, 10*time.Millisecond)
	return store
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

// A Compose that drops the user, the Shared group, the dropped capabilities
// or no-new-privileges would let the app reach data its plan does not show,
// so the install is rolled back.
func TestInstallRollsBackAContainerThatDoesNotRunAsPlanned(t *testing.T) {
	ctx := context.Background()
	planned := func(t *testing.T, store *engine.Store) (appstore.Plan, engine.RuntimeInfo) {
		plan, err := store.Plan(ctx, "navidrome", identity)
		if err != nil {
			t.Fatal(err)
		}
		service := plan.Services[0]
		if service.RunAs != appstore.RunAsApp || service.Shared != appstore.SharedRead {
			t.Fatalf("navidrome service = %+v, want the app identity reading Shared", service)
		}
		return plan, engine.RuntimeInfo{
			Container: "navidrome", Service: service.Service, User: service.User,
			GroupAdd: service.Groups, CapDrop: []string{"CAP_ALL"}, SecurityOpt: []string{"no-new-privileges:true"},
		}
	}
	for name, change := range map[string]func(*engine.RuntimeInfo){
		"image user":          func(runtime *engine.RuntimeInfo) { runtime.User = "" },
		"no Shared group":     func(runtime *engine.RuntimeInfo) { runtime.GroupAdd = nil },
		"capabilities kept":   func(runtime *engine.RuntimeInfo) { runtime.CapDrop = nil },
		"new privileges":      func(runtime *engine.RuntimeInfo) { runtime.SecurityOpt = nil },
		"service not planned": func(runtime *engine.RuntimeInfo) { runtime.Service = "sidecar" },
	} {
		t.Run(name, func(t *testing.T) {
			docker := &fakeDocker{}
			runner := &fakeRunner{}
			store, _ := newStore(t, docker, runner)
			plan, runtime := planned(t, store)
			change(&runtime)
			docker.runtimes = []engine.RuntimeInfo{runtime}
			if _, err := store.Install(ctx, "navidrome", plan.Digest, identity); err != nil {
				t.Fatal(err)
			}
			store.Wait()
			apps, _ := store.Apps(ctx)
			job := find(apps, "navidrome").Job
			if job.State != appstore.JobFailed || !strings.Contains(job.Error, appstore.ErrRuntimeMismatch.Error()) || len(runner.calls) != 2 {
				t.Fatalf("job = %+v calls = %v, want a rolled-back install", job, runner.calls)
			}
		})
	}

	docker := &fakeDocker{}
	runner := &fakeRunner{}
	store, _ := newStore(t, docker, runner)
	plan, runtime := planned(t, store)
	docker.runtimes = []engine.RuntimeInfo{runtime}
	if _, err := store.Install(ctx, "navidrome", plan.Digest, identity); err != nil {
		t.Fatal(err)
	}
	store.Wait()
	apps, _ := store.Apps(ctx)
	if job := find(apps, "navidrome").Job; job.State != appstore.JobSucceeded || len(runner.calls) != 1 {
		t.Fatalf("job = %+v calls = %v, want success", job, runner.calls)
	}
}

func TestInstallRollsBackAWritableReadOnlyFolder(t *testing.T) {
	docker := &fakeDocker{}
	runner := &fakeRunner{}
	store, _ := newStore(t, docker, runner)
	ctx := context.Background()
	plan, err := store.Plan(ctx, "navidrome", identity)
	if err != nil {
		t.Fatal(err)
	}
	index := slices.IndexFunc(plan.Mounts, func(mount appstore.Mount) bool { return mount.Kind == appstore.MountShared })
	music := plan.Mounts[index]
	if !music.ReadOnly {
		t.Fatalf("Music mount = %+v, want read-only", music)
	}
	docker.mounts = []engine.MountInfo{{Container: "navidrome", Volume: music.Volume, Target: music.ContainerPath, Subpath: music.Subpath}}
	if _, err := store.Install(ctx, "navidrome", plan.Digest, identity); err != nil {
		t.Fatal(err)
	}
	store.Wait()
	apps, _ := store.Apps(ctx)
	if job := find(apps, "navidrome").Job; job.State != appstore.JobFailed || !strings.Contains(job.Error, "plan mounts read-only") {
		t.Fatalf("job = %+v, want a rolled-back install", job)
	}
}

// Containers created under older runtime rules keep running as they were;
// the app reports it so the owner can reinstall it.
func TestAppsReportContainersFromOlderRuntimeRules(t *testing.T) {
	docker := &fakeDocker{containers: []engine.ContainerInfo{
		{Name: "memos", AppID: "memos", Running: true},
		{Name: "navidrome", AppID: "navidrome", Running: true, Runtime: appstore.RuntimePolicy},
	}}
	store, _ := newStore(t, docker, &fakeRunner{})
	apps, err := store.Apps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !find(apps, "memos").RuntimeOutdated || find(apps, "navidrome").RuntimeOutdated || find(apps, "n8n").RuntimeOutdated {
		t.Fatalf("outdated: memos %v navidrome %v n8n %v", find(apps, "memos").RuntimeOutdated, find(apps, "navidrome").RuntimeOutdated, find(apps, "n8n").RuntimeOutdated)
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
	if verbs := runner.verbs(); !slices.Equal(verbs, []string{"up", "logs", "down"}) {
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

// linkwardenPartway is what Compose leaves when Linkwarden's postgres is not
// healthy in time on the NAS: up exits 1 with postgres and meilisearch
// running and linkwarden created but never started (#169).
var linkwardenPartway = []engine.ContainerInfo{
	{Name: "linkwarden-postgres", AppID: "linkwarden", Service: "linkwarden-postgres", Running: true},
	{Name: "linkwarden-meilisearch", AppID: "linkwarden", Service: "linkwarden-meilisearch", Running: true},
	{Name: "linkwarden", AppID: "linkwarden", Service: "linkwarden", State: "created"},
}

// composeOn has up create containers and down remove the app's.
func composeOn(docker *fakeDocker, created []engine.ContainerInfo) func([]string) {
	return func(args []string) {
		docker.mu.Lock()
		defer docker.mu.Unlock()
		switch {
		case slices.Contains(args, "up"):
			docker.containers = append(docker.containers, created...)
		case slices.Contains(args, "down"):
			docker.containers = slices.DeleteFunc(docker.containers, func(container engine.ContainerInfo) bool {
				return slices.ContainsFunc(created, func(app engine.ContainerInfo) bool { return app.AppID == container.AppID })
			})
		}
	}
}

func failUp(args []string) error {
	if slices.Contains(args, "up") {
		return errors.New("compose command failed: exit status 1")
	}
	return nil
}

func linkwardenPlan(t *testing.T, store *engine.Store) appstore.Plan {
	t.Helper()
	plan, err := store.Plan(context.Background(), "linkwarden", identity)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// A Compose that fails after creating some containers is rolled back like a
// failed check: the app is not installed and says why.
func TestInstallRollsBackWhenComposeFailsPartway(t *testing.T) {
	docker := &fakeDocker{pools: []netip.Prefix{netip.MustParsePrefix("10.96.64.0/19")}}
	runner := &fakeRunner{after: composeOn(docker, linkwardenPartway), fail: failUp}
	store, _ := newStore(t, docker, runner)
	ctx := context.Background()

	if _, err := store.Install(ctx, "linkwarden", linkwardenPlan(t, store).Digest, identity); err != nil {
		t.Fatal(err)
	}
	store.Wait()

	if verbs := runner.verbs(); !slices.Equal(verbs, []string{"up", "logs", "down"}) {
		t.Fatalf("compose ran %v, want up, logs, down", verbs)
	}
	apps, _ := store.Apps(ctx)
	status := find(apps, "linkwarden")
	if status.State != appstore.StateAvailable || status.Total != 0 {
		t.Fatalf("status = %s with %d containers, want available with none", status.State, status.Total)
	}
	if job := status.Job; job.State != appstore.JobFailed || !strings.Contains(job.Error, "Docker Compose 没有完成启动（compose command failed: exit status 1）") ||
		!strings.Contains(job.Error, "安装已回滚") {
		t.Fatalf("job = %+v, want a failed install that was rolled back", job)
	}
}

// When the rollback fails too, the containers that are left are a failed
// install, not an installed app; installing again removes them first, and
// they never block it.
func TestContainersOfAFailedInstallAreNotAnInstalledApp(t *testing.T) {
	docker := &fakeDocker{pools: []netip.Prefix{netip.MustParsePrefix("10.96.64.0/19")}}
	docker.cleanupErr = errors.New("cannot connect to Docker")
	broken := true
	runner := &fakeRunner{after: func(args []string) {
		if !broken || !slices.Contains(args, "down") {
			composeOn(docker, linkwardenPartway)(args)
		}
	}}
	runner.fail = func(args []string) error {
		if broken && slices.Contains(args, "down") {
			return errors.New("compose command failed: Cannot connect to the Docker daemon")
		}
		if broken {
			return failUp(args)
		}
		return nil
	}
	store, _ := newStore(t, docker, runner)
	ctx := context.Background()
	plan := linkwardenPlan(t, store)

	if _, err := store.Install(ctx, "linkwarden", plan.Digest, identity); err != nil {
		t.Fatal(err)
	}
	store.Wait()
	apps, _ := store.Apps(ctx)
	status := find(apps, "linkwarden")
	if status.State != appstore.StateFailed || status.Running != 2 || status.Total != 3 || status.Credentials != nil {
		t.Fatalf("status = %s %d/%d credentials %v, want failed with the 3 containers left", status.State, status.Running, status.Total, status.Credentials)
	}
	if !strings.Contains(status.Job.Error, "could not be rolled back") {
		t.Fatalf("job error = %q", status.Job.Error)
	}

	broken = false
	docker.cleanupErr = nil
	runner.calls = nil
	if _, err := store.Install(ctx, "linkwarden", plan.Digest, identity); err != nil {
		t.Fatalf("installing again over a failed install: %v", err)
	}
	store.Wait()
	if len(runner.calls) < 2 || slices.Contains(runner.calls[0], "-f") || !slices.Contains(runner.calls[0], "down") || !slices.Contains(runner.calls[1], "up") {
		t.Fatalf("runner calls = %v, want the leftovers taken down before up", runner.calls)
	}
	apps, _ = store.Apps(ctx)
	if status := find(apps, "linkwarden"); status.State != appstore.StateInstalled || status.Total != 3 || status.Job.State != appstore.JobSucceeded {
		t.Fatalf("status = %s with %d containers, job %+v, want installed", status.State, status.Total, status.Job)
	}
	if _, err := store.Install(ctx, "linkwarden", plan.Digest, identity); !errors.Is(err, appstore.ErrAlreadyInstalled) {
		t.Fatalf("installing an installed app: %v, want ErrAlreadyInstalled", err)
	}
}

// Stopping the agent, as a deploy, apt upgrade or Docker restart does,
// cancels a running install and rolls it back before the agent exits; the
// agent started again still reports why the install failed.
func TestStoppingTheAgentRollsBackARunningInstall(t *testing.T) {
	docker := &fakeDocker{pools: []netip.Prefix{netip.MustParsePrefix("10.96.64.0/19")}}
	runner := &fakeRunner{
		after: composeOn(docker, linkwardenPartway),
		hold:  func(args []string) bool { return slices.Contains(args, "up") },
		held:  make(chan struct{}, 1),
	}
	store, state := newStore(t, docker, runner)
	ctx := context.Background()
	if _, err := store.Install(ctx, "linkwarden", linkwardenPlan(t, store).Digest, identity); err != nil {
		t.Fatal(err)
	}
	<-runner.held
	store.Stop()

	if verbs := runner.verbs(); !slices.Equal(verbs, []string{"up", "down"}) {
		t.Fatalf("compose ran %v, want up cancelled and then down", verbs)
	}
	memos, _ := store.Plan(ctx, "memos", identity)
	if _, err := store.Install(ctx, "memos", memos.Digest, identity); !errors.Is(err, appstore.ErrUnavailable) {
		t.Fatalf("install on a stopped agent: %v, want ErrUnavailable", err)
	}
	info, err := os.Stat(filepath.Join(state, "jobs", "linkwarden.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("job record = %v, %v", info, err)
	}

	restarted := newStoreIn(t, state, docker, &fakeRunner{})
	if err := restarted.Recover(); err != nil {
		t.Fatal(err)
	}
	restarted.Wait()
	apps, _ := restarted.Apps(ctx)
	status := find(apps, "linkwarden")
	if status.State != appstore.StateAvailable || status.Job == nil || status.Job.State != appstore.JobFailed ||
		!strings.Contains(status.Job.Error, "容器代理停止，安装被中断，已回滚") {
		t.Fatalf("after a restart: status %s job %+v", status.State, status.Job)
	}
}

// An agent killed in the middle of an install (SIGKILL at the stop timeout,
// a crash) finds the install still recorded as running when it starts again
// and removes its containers, which were never checked against the plan.
func TestRecoverRollsBackAnInstallTheAgentDiedIn(t *testing.T) {
	docker := &fakeDocker{pools: []netip.Prefix{netip.MustParsePrefix("10.96.64.0/19")}}
	dead := &fakeRunner{gate: make(chan struct{})}
	store, state := newStore(t, docker, dead)
	ctx := context.Background()
	if _, err := store.Install(ctx, "linkwarden", linkwardenPlan(t, store).Digest, identity); err != nil {
		t.Fatal(err)
	}
	// Compose got this far before the agent died.
	docker.mu.Lock()
	docker.containers = slices.Clone(linkwardenPartway)
	docker.mu.Unlock()
	defer func() {
		close(dead.gate)
		store.Wait()
	}()

	runner := &fakeRunner{gate: make(chan struct{}), after: composeOn(docker, linkwardenPartway)}
	restarted := newStoreIn(t, state, docker, runner)
	if err := restarted.Recover(); err != nil {
		t.Fatal(err)
	}
	apps, _ := restarted.Apps(ctx)
	if state := find(apps, "linkwarden").State; state != appstore.StateInstalling {
		t.Fatalf("while rolling back: %s, want installing", state)
	}
	memos, _ := restarted.Plan(ctx, "memos", identity)
	if _, err := restarted.Install(ctx, "memos", memos.Digest, identity); !errors.Is(err, appstore.ErrBusy) {
		t.Fatalf("install while rolling back: %v, want ErrBusy", err)
	}
	close(runner.gate)
	restarted.Wait()

	if len(runner.calls) != 1 || strings.Join(runner.calls[0], " ") != "compose --progress plain -p a-nas-linkwarden down --remove-orphans" {
		t.Fatalf("runner calls = %v, want the project taken down", runner.calls)
	}
	apps, _ = restarted.Apps(ctx)
	status := find(apps, "linkwarden")
	if status.State != appstore.StateAvailable || status.Job.State != appstore.JobFailed ||
		!strings.Contains(status.Job.Error, "容器代理在安装过程中重启") || !strings.Contains(status.Job.Error, "安装已回滚") {
		t.Fatalf("after recovery: status %s job %+v", status.State, status.Job)
	}
}

// An uninstall the agent died in is finished when it starts again.
func TestRecoverFinishesAnUninstallTheAgentDiedIn(t *testing.T) {
	memos := []engine.ContainerInfo{{Name: "memos", AppID: "memos", Running: true}}
	docker := &fakeDocker{containers: slices.Clone(memos)}
	dead := &fakeRunner{gate: make(chan struct{})}
	store, state := newStore(t, docker, dead)
	if _, err := store.Uninstall(context.Background(), "memos"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		close(dead.gate)
		store.Wait()
	}()

	runner := &fakeRunner{after: composeOn(docker, memos)}
	restarted := newStoreIn(t, state, docker, runner)
	if err := restarted.Recover(); err != nil {
		t.Fatal(err)
	}
	restarted.Wait()
	apps, _ := restarted.Apps(context.Background())
	status := find(apps, "memos")
	if verbs := runner.verbs(); !slices.Equal(verbs, []string{"down"}) || status.State != appstore.StateAvailable ||
		status.Job.Action != appstore.JobUninstall || status.Job.State != appstore.JobSucceeded {
		t.Fatalf("compose ran %v; status %s job %+v", verbs, status.State, status.Job)
	}
}
