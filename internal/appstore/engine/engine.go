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
	"strings"
	"sync"
	"time"

	"github.com/zhongwater123/A-NAS/internal/appstore"
)

const (
	// Image pulls on a slow link can take a long time; uninstall is quick.
	installTimeout   = 30 * time.Minute
	uninstallTimeout = 5 * time.Minute
	outputTailLines  = 40
)

// ContainerInfo is what conflict checks and install state need from Docker.
type ContainerInfo struct {
	Name           string
	AppID          string
	Running        bool
	PublishedPorts []uint16
}

// NetworkInfo is a Docker network and the subnets Docker gave it.
type NetworkInfo struct {
	Name    string
	Subnets []netip.Prefix
}

type Inspector interface {
	Containers(ctx context.Context) ([]ContainerInfo, error)
	// AddressPools are the daemon's default-address-pools; empty when Docker
	// uses its built-in pools.
	AddressPools(ctx context.Context) ([]netip.Prefix, error)
	// ProjectNetworks lists the networks Compose created for a project.
	ProjectNetworks(ctx context.Context, project string) ([]NetworkInfo, error)
}

// Runner executes the Compose CLI and streams its combined output.
type Runner interface {
	Run(ctx context.Context, args []string, output io.Writer) error
}

type Store struct {
	entries  map[string]appstore.Entry
	order    []string
	policy   appstore.Policy
	stateDir string
	docker   Inspector
	runner   Runner
	now      func() time.Time

	mu     sync.Mutex
	active string
	jobs   map[string]*appstore.Job
	wait   sync.WaitGroup
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
	}
	for _, entry := range entries {
		store.entries[entry.App.ID] = entry
		store.order = append(store.order, entry.App.ID)
	}
	return store
}

// Wait blocks until running jobs finish; used on shutdown and in tests.
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
			if container.Running {
				status.Running++
			}
		}
		if status.Total > 0 {
			status.State = appstore.StateInstalled
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
	plan, err := appstore.Render(ctx, entry, s.policy, identity)
	if err != nil || len(plan.Networks) == 0 {
		return plan, err
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
	if err := conflicts(id, plan, containers); err != nil {
		return appstore.Job{}, err
	}

	directory := filepath.Join(s.stateDir, "apps", id)
	composeFile := filepath.Join(directory, "docker-compose.yml")
	compose := []string{"compose", "--progress", "plain", "-p", plan.Project, "-f", composeFile}
	return s.start(id, appstore.JobInstall, installTimeout, func(ctx context.Context, output io.Writer) error {
		if err := os.MkdirAll(directory, 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(composeFile, plan.Compose, 0o640); err != nil {
			return err
		}
		if err := s.runner.Run(ctx, append(compose, "up", "--detach", "--remove-orphans"), output); err != nil {
			return err
		}
		return s.verifyNetworks(ctx, plan, compose, output)
	})
}

// verifyNetworks rolls an install back when one of its networks is outside
// the address pools, or cannot be inspected: the host would route those
// addresses into the app instead of to the LAN (ADR 0013).
func (s *Store) verifyNetworks(ctx context.Context, plan appstore.Plan, compose []string, output io.Writer) error {
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
	failure := fmt.Errorf("%w (%s); the install was rolled back", appstore.ErrNetworkOutsidePool, strings.Join(outside, ", "))
	if err := s.runner.Run(ctx, append(compose, "down", "--remove-orphans"), output); err != nil {
		return fmt.Errorf("%w; rollback failed: %v", failure, err)
	}
	return failure
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
		return appstore.Job{}, appstore.ErrNotInstalled
	}
	// App data under the app-data root is kept; only containers and the
	// project network are removed.
	args := []string{"compose", "--progress", "plain", "-p", appstore.ProjectName(id), "down", "--remove-orphans"}
	return s.start(id, appstore.JobUninstall, uninstallTimeout, func(ctx context.Context, output io.Writer) error {
		return s.runner.Run(ctx, args, output)
	})
}

// start runs one job at a time in the background; run receives the job's
// output, of which the job keeps the tail.
func (s *Store) start(id string, action appstore.JobAction, timeout time.Duration, run func(context.Context, io.Writer) error) (appstore.Job, error) {
	s.mu.Lock()
	if s.active != "" {
		s.mu.Unlock()
		return appstore.Job{}, appstore.ErrBusy
	}
	job := &appstore.Job{AppID: id, Action: action, State: appstore.JobRunning, StartedAt: s.now().UTC()}
	s.active = id
	s.jobs[id] = job
	snapshot := copyJob(job)
	s.wait.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.wait.Done()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		err := run(ctx, &jobWriter{store: s, job: job})
		s.mu.Lock()
		defer s.mu.Unlock()
		job.FinishedAt = s.now().UTC()
		job.State = appstore.JobSucceeded
		if err != nil {
			job.State = appstore.JobFailed
			job.Error = err.Error()
		}
		s.active = ""
	}()
	return snapshot, nil
}

// conflicts rejects installs whose ports or container names are taken by
// containers outside the app.
func conflicts(id string, plan appstore.Plan, containers []ContainerInfo) error {
	used := map[uint16]string{}
	names := map[string]bool{}
	for _, container := range containers {
		if container.AppID == id {
			return appstore.ErrAlreadyInstalled
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

func composePluginAvailable(ctx context.Context) bool {
	return exec.CommandContext(ctx, "docker", "compose", "version").Run() == nil
}

var _ appstore.Store = (*Store)(nil)
