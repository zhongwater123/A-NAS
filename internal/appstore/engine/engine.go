// Package engine installs and removes catalog apps on the local Docker Engine
// by running the Docker Compose CLI. It runs only inside the container agent.
package engine

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"

	"github.com/zhongwater123/A-NAS/internal/appstore"
)

const (
	// Image pulls on a slow link can take a long time; uninstall is quick.
	installTimeout   = 30 * time.Minute
	uninstallTimeout = 5 * time.Minute
	outputTailLines  = 40

	// Compose before 2.30 sends volume subpaths through the legacy bind API,
	// which drops them; the engine API gained subpaths in 1.45 (ADR 0015).
	minComposeVersion = "v2.30.0"
	minEngineAPI      = "v1.45"
)

// ContainerInfo is what conflict checks and install state need from Docker.
type ContainerInfo struct {
	Name           string
	AppID          string
	Running        bool
	PublishedPorts []uint16
	// Service is the Compose service; State and ExitCode tell an installed
	// app's status why a container is not running.
	Service  string
	State    string
	ExitCode int
	// Runtime is the container's io.a-nas.runtime label, empty on containers
	// created before runtime rules were recorded.
	Runtime string
}

// NetworkInfo is a Docker network and the subnets Docker gave it.
type NetworkInfo struct {
	Name    string
	Subnets []netip.Prefix
}

// MountInfo is a volume a container mounts and the path below the volume
// root it was given; Subpath is empty when the whole volume is mounted.
type MountInfo struct {
	Container string
	Volume    string
	Target    string
	Subpath   string
	ReadOnly  bool
}

// RuntimeInfo is the identity and privilege limits Docker gave a container.
type RuntimeInfo struct {
	Container   string
	Service     string
	User        string
	GroupAdd    []string
	CapDrop     []string
	SecurityOpt []string
}

type Inspector interface {
	// CleanupImages inspects this project's containers and saved immutable
	// image IDs before any container is removed; mutable tags are rejected.
	CleanupImages(ctx context.Context, project string, references []string) ([]string, error)
	RemoveProjectContainers(ctx context.Context, project string) error
	RemoveUnusedImages(ctx context.Context, images []string) error
	// CheckStorage refuses installs when the system state or Docker storage
	// filesystem is below the installation reserve, or cannot be observed.
	CheckStorage(ctx context.Context) error
	Containers(ctx context.Context) ([]ContainerInfo, error)
	// AddressPools are the daemon's default-address-pools; empty when Docker
	// uses its built-in pools.
	AddressPools(ctx context.Context) ([]netip.Prefix, error)
	// ProjectNetworks lists the networks Compose created for a project.
	ProjectNetworks(ctx context.Context, project string) ([]NetworkInfo, error)
	// APIVersion is the highest engine API version the daemon supports.
	APIVersion(ctx context.Context) (string, error)
	// ProjectMounts lists the volumes the project's containers mount.
	ProjectMounts(ctx context.Context, project string) ([]MountInfo, error)
	// ProjectContainers reads the state of the project's containers.
	ProjectContainers(ctx context.Context, project string) ([]ContainerState, error)
	// ProjectRuntimes reads the user, groups and privilege limits of the
	// project's containers.
	ProjectRuntimes(ctx context.Context, project string) ([]RuntimeInfo, error)
}

// Runner executes the Compose CLI and streams its combined output.
type Runner interface {
	Run(ctx context.Context, args []string, output io.Writer) error
	// ComposeVersion is the version of the Compose CLI that Run uses.
	ComposeVersion(ctx context.Context) (string, error)
}

type Store struct {
	entries  map[string]appstore.Entry
	order    []string
	policy   appstore.Policy
	stateDir string
	docker   Inspector
	runner   Runner
	now      func() time.Time
	// startupWindow is how long an install watches its containers before it
	// counts as a success, checking every startupPoll.
	startupWindow time.Duration
	startupPoll   time.Duration

	// secretsMu serialises generating and storing app secrets.
	secretsMu sync.Mutex

	mu     sync.Mutex
	active string
	// cancel ends the context of the active job; stopped refuses new jobs.
	cancel  context.CancelCauseFunc
	stopped bool
	jobs    map[string]*appstore.Job
	wait    sync.WaitGroup
}

func New(entries []appstore.Entry, policy appstore.Policy, stateDir string, docker Inspector, runner Runner) *Store {
	store := &Store{
		entries:  make(map[string]appstore.Entry, len(entries)),
		policy:   policy,
		stateDir: stateDir,
		docker:   docker,
		runner:   runner,
		now:      time.Now,
		jobs:     map[string]*appstore.Job{},

		startupWindow: defaultStartupWindow,
		startupPoll:   defaultStartupPoll,
	}
	for _, entry := range entries {
		store.entries[entry.App.ID] = entry
		store.order = append(store.order, entry.App.ID)
	}
	return store
}

// Wait blocks until running jobs finish; used in tests.
func (s *Store) Wait() {
	s.wait.Wait()
}

func (s *Store) Apps(ctx context.Context) ([]appstore.AppStatus, error) {
	containers, err := s.docker.Containers(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", appstore.ErrUnavailable, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	statuses := make([]appstore.AppStatus, 0, len(s.order))
	for _, id := range s.order {
		status := appstore.AppStatus{App: s.entries[id].App, State: appstore.StateAvailable}
		for _, container := range containers {
			if container.AppID != id {
				continue
			}
			status.Total++
			if container.Runtime != appstore.RuntimePolicy {
				status.RuntimeOutdated = true
			}
			if container.Running {
				status.Running++
			} else {
				status.Problems = append(status.Problems, appstore.ContainerProblem{
					Service: container.Service, State: container.State, ExitCode: container.ExitCode,
				})
			}
		}
		if status.Total > 0 && failedInstall(s.jobs[id]) {
			// Containers outlived their failed install: they are reported,
			// never shown as an installed app.
			status.State = appstore.StateFailed
		} else if status.Total > 0 {
			status.State = appstore.StateInstalled
			// Installed before secrets were generated: it still uses the
			// manifest's own logins.
			if secrets, err := s.readSecrets(id); err == nil && secrets == nil {
				status.Credentials = appstore.UpstreamCredentials(id)
			} else if err == nil {
				status.Credentials = appstore.Credentials(id, secrets)
			}
		}
		if s.jobs[id] != nil && s.jobs[id].State == appstore.JobFailed {
			pending, err := s.hasImages(id)
			if err != nil {
				return nil, fmt.Errorf("%w: %v", appstore.ErrUnavailable, err)
			}
			if pending {
				status.State = appstore.StateFailed
				status.Credentials = nil
			}
		}
		if job := s.jobs[id]; job != nil {
			copied := copyJob(job)
			status.Job = &copied
			if job.State == appstore.JobRunning {
				status.State = map[appstore.JobAction]appstore.InstallState{
					appstore.JobInstall: appstore.StateInstalling, appstore.JobUninstall: appstore.StateUninstalling,
				}[job.Action]
			}
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

func (s *Store) Icon(_ context.Context, id string) ([]byte, string, error) {
	entry, ok := s.entries[id]
	if !ok || entry.Icon == nil {
		return nil, "", appstore.ErrNotFound
	}
	return entry.Icon, entry.IconType, nil
}

func (s *Store) Plan(ctx context.Context, id string, identity appstore.Identity) (appstore.Plan, error) {
	entry, ok := s.entries[id]
	if !ok {
		return appstore.Plan{}, appstore.ErrNotFound
	}
	if err := s.docker.CheckStorage(ctx); err != nil {
		return appstore.Plan{}, err
	}
	secrets, err := s.appSecrets(id)
	if err != nil {
		return appstore.Plan{}, fmt.Errorf("%w: app secrets: %v", appstore.ErrUnavailable, err)
	}
	plan, err := appstore.Render(ctx, entry, s.policy, identity, secrets)
	if err != nil {
		return plan, err
	}
	if err := s.checkRuntime(ctx); err != nil {
		return appstore.Plan{}, err
	}
	if len(plan.Networks) == 0 {
		return plan, nil
	}
	// Docker's built-in pools (172.17-172.31, 192.168) overlap common LAN
	// ranges; an app network there makes the host route those clients into the
	// app (ADR 0013). Refuse until the daemon has an A-NAS pool.
	pools, err := s.docker.AddressPools(ctx)
	if err != nil {
		return appstore.Plan{}, fmt.Errorf("%w: %v", appstore.ErrUnavailable, err)
	}
	if len(pools) == 0 {
		return appstore.Plan{}, appstore.ErrAddressPoolMissing
	}
	plan.AddressPools = pools
	return plan, nil
}

func (s *Store) Install(ctx context.Context, id, digest string, identity appstore.Identity) (appstore.Job, error) {
	plan, err := s.Plan(ctx, id, identity)
	if err != nil {
		return appstore.Job{}, err
	}
	// Re-rendering here means a changed catalog or policy cannot run a plan
	// the owner never saw.
	if plan.Digest != digest {
		return appstore.Job{}, appstore.ErrPlanChanged
	}
	containers, err := s.docker.Containers(ctx)
	if err != nil {
		return appstore.Job{}, fmt.Errorf("%w: %v", appstore.ErrUnavailable, err)
	}
	s.mu.Lock()
	replace := s.jobs[id] != nil && s.jobs[id].State == appstore.JobFailed
	s.mu.Unlock()
	if err := conflicts(id, plan, containers, replace); err != nil {
		return appstore.Job{}, err
	}
	leftovers := slices.ContainsFunc(containers, func(container ContainerInfo) bool { return container.AppID == id })
	pending, err := s.hasImages(id)
	if err != nil {
		return appstore.Job{}, fmt.Errorf("%w: %v", appstore.ErrUnavailable, err)
	}
	leftovers = leftovers || pending

	directory := filepath.Join(s.stateDir, "apps", id)
	composeFile := filepath.Join(directory, "docker-compose.yml")
	compose := []string{"compose", "--progress", "plain", "-p", plan.Project, "-f", composeFile}
	return s.start(id, appstore.JobInstall, installTimeout, func(ctx context.Context, output io.Writer) error {
		// What a failed install left behind goes first, so that the checks
		// below see only this install's containers.
		if leftovers {
			if err := s.cleanup(ctx, id, takeDown(id), output); err != nil {
				return fmt.Errorf("remove the containers of the failed install: %w", err)
			}
		}
		if err := os.MkdirAll(directory, 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(composeFile, plan.Compose, 0o640); err != nil {
			return err
		}
		// Mark cleanup as pending without granting deletion authority to
		// mutable tags. Recovery discovers IDs from actual containers; a
		// download that never created a container has no proven ownership.
		if err := s.saveImages(id, nil); err != nil {
			return err
		}
		// Compose can fail after creating some of the containers, for
		// example on a port a host process holds or a dependency that is
		// slow to turn healthy: those are rolled back like any other.
		failure := s.runner.Run(ctx, append(compose, "up", "--detach", "--remove-orphans"), output)
		if failure != nil {
			failure = &StartupError{Problem: "Docker Compose 没有完成启动（" + failure.Error() + "）"}
		}
		if failure == nil {
			failure = s.mountProblems(ctx, plan)
		}
		if failure == nil {
			failure = s.runtimeProblems(ctx, plan)
		}
		if failure == nil {
			failure = s.networkProblems(ctx, plan)
		}
		if failure == nil {
			failure = s.startupProblem(ctx, plan.Project)
		}
		if failure == nil {
			// Tags can be moved by later pulls or administrators. Remember the
			// images that actually ran, so uninstall cannot follow a changed tag.
			images, err := s.docker.CleanupImages(ctx, plan.Project, nil)
			if err == nil {
				err = s.saveImages(id, images)
			}
			if err == nil {
				return nil
			}
			// The app passed all runtime checks. Keep it running; the pending
			// record survives and cleanup rediscovers IDs from its containers.
			_, _ = fmt.Fprintf(output, "app is running; image bookkeeping deferred until cleanup: %v\n", err)
			return nil
		}
		if errors.Is(context.Cause(ctx), errStopping) {
			failure = errStopping
		} else if errors.Is(failure, appstore.ErrAppNotRunning) && ctx.Err() == nil {
			// The app's own words on why it stopped go into the output.
			_ = s.runner.Run(ctx, append(compose, "logs", "--no-color", "--tail", "20"), output)
		}
		// Docker did not start the app, keep it inside its plan or keep it
		// running, or the agent is stopping: roll it back, even when the
		// install was cancelled or ran out of time.
		rollback, cancel := context.WithTimeout(context.WithoutCancel(ctx), uninstallTimeout)
		defer cancel()
		if err := s.cleanup(rollback, id, append(compose, "down", "--remove-orphans"), output); err != nil {
			return fmt.Errorf("%w; the install could not be rolled back: %v", failure, err)
		}
		switch {
		case errors.Is(failure, errStopping):
			return fmt.Errorf("%w，已回滚，请重新安装", failure)
		case errors.Is(failure, appstore.ErrAppNotRunning):
			return fmt.Errorf("%w，安装已回滚；最近的日志见下方输出", failure)
		}
		return fmt.Errorf("%w; the install was rolled back", failure)
	})
}

// failedInstall reports whether an app's latest job is an install that failed.
func failedInstall(job *appstore.Job) bool {
	return job != nil && job.Action == appstore.JobInstall && job.State == appstore.JobFailed
}

// checkRuntime refuses to plan on a Docker that would mount whole volumes,
// giving an app all of its app data or the whole Shared folder (ADR 0015).
func (s *Store) checkRuntime(ctx context.Context) error {
	compose, err := s.runner.ComposeVersion(ctx)
	if err != nil {
		return fmt.Errorf("%w: %v", appstore.ErrUnavailable, err)
	}
	api, err := s.docker.APIVersion(ctx)
	if err != nil {
		return fmt.Errorf("%w: %v", appstore.ErrUnavailable, err)
	}
	if !atLeast(compose, minComposeVersion) {
		return fmt.Errorf("%w: Docker Compose %s, need %s or later", appstore.ErrRuntimeOutdated, compose, strings.TrimPrefix(minComposeVersion, "v"))
	}
	if !atLeast(api, minEngineAPI) {
		return fmt.Errorf("%w: Docker Engine API %s, need %s or later", appstore.ErrRuntimeOutdated, api, strings.TrimPrefix(minEngineAPI, "v"))
	}
	return nil
}

// atLeast compares versions as Docker and distributions print them, with or
// without the leading v; a distribution revision such as 2.26.1-4 sorts below
// 2.26.1, which is safe for a minimum.
func atLeast(version, minimum string) bool {
	version = "v" + strings.TrimPrefix(strings.TrimSpace(version), "v")
	return semver.IsValid(version) && semver.Compare(version, minimum) >= 0
}

// mountProblems reports an A-NAS volume that a container mounts beyond the
// folder in the plan or writable where the plan has it read-only, or mounts
// that cannot be inspected.
func (s *Store) mountProblems(ctx context.Context, plan appstore.Plan) error {
	planned := map[string]appstore.Mount{}
	volumes := map[string]bool{}
	for _, mount := range plan.Mounts {
		if mount.Volume != "" {
			planned[mount.ContainerPath] = mount
			volumes[mount.Volume] = true
		}
	}
	if len(volumes) == 0 {
		return nil
	}
	var problems []string
	mounts, err := s.docker.ProjectMounts(ctx, plan.Project)
	if err != nil {
		problems = append(problems, "cannot inspect app mounts: "+err.Error())
	}
	for _, mount := range mounts {
		if !volumes[mount.Volume] {
			continue
		}
		want, ok := planned[mount.Target]
		if !ok || want.Volume != mount.Volume || want.Subpath != mount.Subpath {
			got := "the whole " + mount.Volume
			if mount.Subpath != "" {
				got = mount.Volume + "/" + mount.Subpath
			}
			problems = append(problems, fmt.Sprintf("%s mounts %s at %s", mount.Container, got, mount.Target))
		} else if want.ReadOnly && !mount.ReadOnly {
			problems = append(problems, fmt.Sprintf("%s can write %s, which its plan mounts read-only", mount.Container, mount.Target))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%w (%s)", appstore.ErrMountOutsideFolder, strings.Join(problems, ", "))
}

// runtimeProblems reports a container whose user, groups, capabilities or
// no-new-privileges flag differ from its service in the plan, or containers
// that cannot be inspected: a Compose that dropped one of them would let the
// app reach data its plan does not show (ADR 0022).
func (s *Store) runtimeProblems(ctx context.Context, plan appstore.Plan) error {
	planned := map[string]appstore.ServiceRuntime{}
	for _, service := range plan.Services {
		planned[service.Service] = service
	}
	var problems []string
	runtimes, err := s.docker.ProjectRuntimes(ctx, plan.Project)
	if err != nil {
		problems = append(problems, "cannot inspect app containers: "+err.Error())
	}
	for _, runtime := range runtimes {
		want, ok := planned[runtime.Service]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("%s runs service %q, which is not in the plan", runtime.Container, runtime.Service))
		case want.RunAs == appstore.RunAsApp && runtime.User != want.User:
			problems = append(problems, fmt.Sprintf("%s runs as user %q, not %q", runtime.Container, runtime.User, want.User))
		case !sameSet(runtime.GroupAdd, want.Groups):
			problems = append(problems, fmt.Sprintf("%s has groups %v, not %v", runtime.Container, runtime.GroupAdd, want.Groups))
		case !dropsAll(runtime.CapDrop, want.CapDrop):
			problems = append(problems, fmt.Sprintf("%s drops capabilities %v, not %v", runtime.Container, runtime.CapDrop, want.CapDrop))
		case !noNewPrivileges(runtime.SecurityOpt):
			problems = append(problems, fmt.Sprintf("%s can gain privileges (security options %v)", runtime.Container, runtime.SecurityOpt))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%w (%s)", appstore.ErrRuntimeMismatch, strings.Join(problems, ", "))
}

func sameSet(got, want []string) bool {
	return len(got) == len(want) && !slices.ContainsFunc(want, func(item string) bool { return !slices.Contains(got, item) })
}

// dropsAll reports whether every capability in want is dropped; Docker may
// report them with or without the CAP_ prefix.
func dropsAll(got, want []string) bool {
	normal := func(capability string) string {
		return strings.TrimPrefix(strings.ToUpper(capability), "CAP_")
	}
	for _, capability := range want {
		if !slices.ContainsFunc(got, func(item string) bool { return normal(item) == normal(capability) }) {
			return false
		}
	}
	return true
}

func noNewPrivileges(options []string) bool {
	return slices.ContainsFunc(options, func(option string) bool {
		return option == "no-new-privileges" || option == "no-new-privileges:true" || option == "no-new-privileges=true"
	})
}

// networkProblems reports a network outside the address pools, or networks
// that cannot be inspected: the host would route those addresses into the
// app instead of to the LAN (ADR 0013).
func (s *Store) networkProblems(ctx context.Context, plan appstore.Plan) error {
	if len(plan.Networks) == 0 {
		return nil
	}
	var outside []string
	networks, err := s.docker.ProjectNetworks(ctx, plan.Project)
	if err != nil {
		outside = append(outside, "cannot inspect app networks: "+err.Error())
	}
	for _, network := range networks {
		for _, subnet := range network.Subnets {
			if !withinPools(subnet, plan.AddressPools) {
				outside = append(outside, network.Name+" "+subnet.String())
			}
		}
	}
	if len(outside) == 0 {
		return nil
	}
	return fmt.Errorf("%w (%s)", appstore.ErrNetworkOutsidePool, strings.Join(outside, ", "))
}

func withinPools(subnet netip.Prefix, pools []netip.Prefix) bool {
	for _, pool := range pools {
		if pool.Bits() <= subnet.Bits() && pool.Contains(subnet.Masked().Addr()) {
			return true
		}
	}
	return false
}

func (s *Store) Uninstall(ctx context.Context, id string) (appstore.Job, error) {
	if _, ok := s.entries[id]; !ok {
		return appstore.Job{}, appstore.ErrNotFound
	}
	containers, err := s.docker.Containers(ctx)
	if err != nil {
		return appstore.Job{}, fmt.Errorf("%w: %v", appstore.ErrUnavailable, err)
	}
	installed := false
	for _, container := range containers {
		installed = installed || container.AppID == id
	}
	if !installed {
		pending, err := s.hasImages(id)
		if err != nil {
			return appstore.Job{}, fmt.Errorf("%w: %v", appstore.ErrUnavailable, err)
		}
		if !pending {
			return appstore.Job{}, appstore.ErrNotInstalled
		}
	}
	return s.start(id, appstore.JobUninstall, uninstallTimeout, func(ctx context.Context, output io.Writer) error {
		return s.cleanup(ctx, id, takeDown(id), output)
	})
}

// takeDown is the Compose command that removes an app's containers and
// project network; app data under the app-data root is kept.
func takeDown(id string) []string {
	return []string{"compose", "--progress", "plain", "-p", appstore.ProjectName(id), "down", "--remove-orphans"}
}

// start runs one job at a time in the background; run receives the job's
// output, of which the job keeps the tail. The job is recorded before it
// runs, so that a restart of the agent finds it (Recover).
func (s *Store) start(id string, action appstore.JobAction, timeout time.Duration, run func(context.Context, io.Writer) error) (appstore.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return appstore.Job{}, fmt.Errorf("%w: the container agent is stopping", appstore.ErrUnavailable)
	}
	if s.active != "" {
		return appstore.Job{}, appstore.ErrBusy
	}
	job := &appstore.Job{AppID: id, Action: action, State: appstore.JobRunning, StartedAt: s.now().UTC()}
	if err := s.saveJob(job); err != nil {
		return appstore.Job{}, fmt.Errorf("%w: record the job: %v", appstore.ErrUnavailable, err)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	s.active, s.cancel = id, cancel
	s.jobs[id] = job

	s.wait.Go(func() {
		timed, stop := context.WithTimeout(ctx, timeout)
		err := run(timed, &jobWriter{store: s, job: job})
		stop()
		s.mu.Lock()
		defer s.mu.Unlock()
		s.end(job, err)
		s.active, s.cancel = "", nil
		cancel(nil)
	})
	return copyJob(job), nil
}

// conflicts rejects installs whose ports or container names are taken by
// containers outside the app, and installs of an installed app unless
// replace allows the containers of a failed install.
func conflicts(id string, plan appstore.Plan, containers []ContainerInfo, replace bool) error {
	used := map[uint16]string{}
	names := map[string]bool{}
	for _, container := range containers {
		if container.AppID == id {
			if !replace {
				return appstore.ErrAlreadyInstalled
			}
			continue
		}
		names[container.Name] = true
		for _, port := range container.PublishedPorts {
			used[port] = container.Name
		}
	}
	for _, port := range plan.Ports {
		if owner, taken := used[port.HostPort]; taken {
			return fmt.Errorf("%w: %d is used by %s", appstore.ErrPortInUse, port.HostPort, owner)
		}
	}
	for _, name := range plan.Containers {
		if names[name] {
			return fmt.Errorf("%w: %s", appstore.ErrNameInUse, name)
		}
	}
	return nil
}

// jobWriter keeps the last lines of Compose output on the job.
type jobWriter struct {
	store   *Store
	job     *appstore.Job
	partial string
}

func (w *jobWriter) Write(data []byte) (int, error) {
	w.store.mu.Lock()
	defer w.store.mu.Unlock()
	scanner := bufio.NewScanner(strings.NewReader(w.partial + string(data)))
	w.partial = ""
	complete := strings.HasSuffix(string(data), "\n")
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if !complete && len(lines) > 0 {
		w.partial = lines[len(lines)-1]
		lines = lines[:len(lines)-1]
	}
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			w.job.Output = append(w.job.Output, line)
		}
	}
	if extra := len(w.job.Output) - outputTailLines; extra > 0 {
		w.job.Output = append([]string(nil), w.job.Output[extra:]...)
	}
	return len(data), nil
}

func copyJob(job *appstore.Job) appstore.Job {
	copied := *job
	copied.Output = append([]string(nil), job.Output...)
	return copied
}

// CLIRunner runs `docker compose`, falling back to the standalone
// `docker-compose` binary that some distributions ship.
type CLIRunner struct {
	Environment []string
}

func (r CLIRunner) Run(ctx context.Context, args []string, output io.Writer) error {
	name, arguments := "docker", args
	if _, err := exec.LookPath("docker"); err != nil || !composePluginAvailable(ctx) {
		name, arguments = "docker-compose", args[1:]
	}
	command := exec.CommandContext(ctx, name, arguments...)
	command.Env = append(os.Environ(), r.Environment...)
	command.Stdout = output
	command.Stderr = output
	command.WaitDelay = 10 * time.Second
	if err := command.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return errors.New("compose command timed out")
		}
		return fmt.Errorf("compose command failed: %w", err)
	}
	return nil
}

// ComposeVersion reads the version of the Compose CLI that Run picks.
func (r CLIRunner) ComposeVersion(ctx context.Context) (string, error) {
	name, arguments := "docker", []string{"compose", "version", "--short"}
	if _, err := exec.LookPath("docker"); err != nil || !composePluginAvailable(ctx) {
		name, arguments = "docker-compose", arguments[1:]
	}
	command := exec.CommandContext(ctx, name, arguments...)
	command.Env = append(os.Environ(), r.Environment...)
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("read the Compose version: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

func composePluginAvailable(ctx context.Context) bool {
	return exec.CommandContext(ctx, "docker", "compose", "version").Run() == nil
}

var _ appstore.Store = (*Store)(nil)
