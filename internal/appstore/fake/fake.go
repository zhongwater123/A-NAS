// Package fake runs the real install engine against an in-memory Docker so
// the App Center can be exercised end to end without a container engine.
// The simulated Compose reads the rendered Compose file and creates what
// Docker would: a container per service, named as Docker names it, with its
// published ports, volume mounts, user and privilege limits, and the
// project's networks; the install checks then read them back (#194).
package fake

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"maps"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"

	"github.com/zhongwater123/A-NAS/internal/appid"
	"github.com/zhongwater123/A-NAS/internal/appstore"
	"github.com/zhongwater123/A-NAS/internal/appstore/engine"
)

// Policy mirrors the production defaults so fake plans show realistic paths.
var Policy = appstore.Policy{
	AppDataRoot:   "/srv/a-nas/data/apps",
	DataRoot:      "/srv/a-nas/data/spaces/shared",
	TZ:            "Asia/Shanghai",
	ReservedPorts: []uint16{8080},
}

// addressPool is the Experimental NAS's pool for app networks.
var addressPool = netip.MustParsePrefix("10.96.64.0/19")

// New returns a store whose installs take about step*4 to finish.
func New(step time.Duration) (*engine.Store, error) {
	entries, err := appstore.Catalog()
	if err != nil {
		return nil, err
	}
	state, err := os.MkdirTemp("", "a-nas-fake-apps-")
	if err != nil {
		return nil, err
	}
	docker, runner := NewDocker(step)
	store := engine.New(entries, Policy, state, docker, runner)
	store.SetStartupCheck(step, step)
	return store, nil
}

// NewDocker returns an in-memory Docker and the Compose that creates its
// projects, each Compose command taking about step per line of output.
func NewDocker(step time.Duration) (engine.Inspector, engine.Runner) {
	docker := &memoryDocker{projects: map[string]*project{}}
	return docker, &runner{docker: docker, step: step}
}

// project is what Docker reports for one Compose project.
type project struct {
	containers []engine.ContainerInfo
	mounts     []engine.MountInfo
	runtimes   []engine.RuntimeInfo
	networks   []engine.NetworkInfo
}

type memoryDocker struct {
	mu       sync.Mutex
	projects map[string]*project
	// subnets counts the /24 networks handed out from addressPool.
	subnets int
}

// CheckStorage keeps simulated installs independent of the developer's disk.
func (m *memoryDocker) CheckStorage(context.Context) error { return nil }

// Simulated installs create neither images nor volumes. Compose's simulated
// down below remains responsible for deleting their in-memory containers.
func (m *memoryDocker) CleanupImages(context.Context, string, []string) ([]string, error) {
	return nil, nil
}
func (m *memoryDocker) RemoveProjectContainers(context.Context, string) error { return nil }
func (m *memoryDocker) RemoveUnusedImages(context.Context, []string) error    { return nil }

func (m *memoryDocker) Containers(context.Context) ([]engine.ContainerInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var containers []engine.ContainerInfo
	for _, name := range slices.Sorted(maps.Keys(m.projects)) {
		containers = append(containers, m.projects[name].containers...)
	}
	return clone(containers), nil
}

// AddressPools reports the Experimental NAS pool, so apps with their own
// networks can be planned and installed.
func (m *memoryDocker) AddressPools(context.Context) ([]netip.Prefix, error) {
	return []netip.Prefix{addressPool}, nil
}

func (m *memoryDocker) ProjectNetworks(_ context.Context, name string) ([]engine.NetworkInfo, error) {
	return projectPart(m, name, func(p *project) []engine.NetworkInfo { return p.networks }), nil
}

// APIVersion reports an engine that supports volume subpaths.
func (m *memoryDocker) APIVersion(context.Context) (string, error) {
	return "1.52", nil
}

func (m *memoryDocker) ProjectMounts(_ context.Context, name string) ([]engine.MountInfo, error) {
	return projectPart(m, name, func(p *project) []engine.MountInfo { return p.mounts }), nil
}

func (m *memoryDocker) ProjectRuntimes(_ context.Context, name string) ([]engine.RuntimeInfo, error) {
	return projectPart(m, name, func(p *project) []engine.RuntimeInfo { return p.runtimes }), nil
}

// ProjectContainers reports the project's simulated containers as running.
func (m *memoryDocker) ProjectContainers(_ context.Context, name string) ([]engine.ContainerState, error) {
	containers := projectPart(m, name, func(p *project) []engine.ContainerInfo { return p.containers })
	states := make([]engine.ContainerState, 0, len(containers))
	for _, container := range containers {
		states = append(states, engine.ContainerState{Service: container.Service, State: container.State})
	}
	return states, nil
}

// projectPart copies one kind of report of a project; none when Docker has
// no such project.
func projectPart[T any](m *memoryDocker, name string, part func(*project) []T) []T {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.projects[name]; ok {
		return clone(part(p))
	}
	return nil
}

// up creates what Compose would for the rendered project, in place of what
// an earlier up created.
func (m *memoryDocker) up(rendered *types.Project) {
	m.mu.Lock()
	defer m.mu.Unlock()
	created := &project{}
	for _, key := range slices.Sorted(maps.Keys(rendered.Networks)) {
		network := rendered.Networks[key]
		if bool(network.External) {
			continue
		}
		// Each network gets the next /24 of the pool, as Docker hands them out.
		base := addressPool.Addr().As4()
		base[2] += byte(m.subnets % (1 << (24 - addressPool.Bits())))
		m.subnets++
		subnet := netip.PrefixFrom(netip.AddrFrom4(base), 24)
		created.networks = append(created.networks, engine.NetworkInfo{Name: cmp.Or(network.Name, rendered.Name+"_"+key), Subnets: []netip.Prefix{subnet}})
	}
	for _, name := range slices.Sorted(maps.Keys(rendered.Services)) {
		service := rendered.Services[name]
		container := cmp.Or(service.ContainerName, fmt.Sprintf("%s-%s-1", rendered.Name, name))
		info := engine.ContainerInfo{
			Name: container, AppID: service.Labels[appstore.AppLabel], Running: true, Service: name, State: "running",
			Runtime: service.Labels[appstore.RuntimeLabel],
		}
		for _, port := range service.Ports {
			if published, err := strconv.ParseUint(port.Published, 10, 16); err == nil {
				info.PublishedPorts = append(info.PublishedPorts, uint16(published))
			}
		}
		created.containers = append(created.containers, info)
		for _, volume := range service.Volumes {
			if volume.Type != types.VolumeTypeVolume {
				continue
			}
			mount := engine.MountInfo{Container: container, Volume: cmp.Or(rendered.Volumes[volume.Source].Name, rendered.Name+"_"+volume.Source), Target: volume.Target, ReadOnly: volume.ReadOnly}
			if volume.Volume != nil {
				mount.Subpath = volume.Volume.Subpath
			}
			created.mounts = append(created.mounts, mount)
		}
		created.runtimes = append(created.runtimes, engine.RuntimeInfo{
			Container: container, Service: name, User: service.User,
			GroupAdd: slices.Clone(service.GroupAdd), CapDrop: slices.Clone(service.CapDrop), SecurityOpt: slices.Clone(service.SecurityOpt),
		})
	}
	m.projects[rendered.Name] = created
}

func (m *memoryDocker) down(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.projects, name)
}

// Host stands in for the Host Agent with the fake app store: each app gets a
// stable identity in the app range and nothing is created.
type Host struct{}

func (Host) AppIdentity(_ context.Context, id string) (appstore.Identity, error) {
	sum := 0
	for _, character := range id {
		sum = (sum*31 + int(character)) % (appid.LastUID - appid.FirstUID + 1)
	}
	uid := appid.FirstUID + sum
	return appstore.Identity{Username: appid.Username(id), UID: uid, GID: uid}, nil
}

func (Host) PrepareApp(context.Context, string, []string, bool) error { return nil }
func (Host) ReleaseApp(context.Context, string) error                 { return nil }

// runner pretends to pull and start (or stop) the project's services.
type runner struct {
	docker *memoryDocker
	step   time.Duration
}

// ComposeVersion reports a Compose that keeps volume subpaths.
func (r *runner) ComposeVersion(context.Context) (string, error) {
	return "2.40.3", nil
}

func (r *runner) Run(ctx context.Context, args []string, output io.Writer) error {
	name, action, file := "", "", ""
	for i, arg := range args {
		switch {
		case arg == "-p" && i+1 < len(args):
			name = args[i+1]
		case arg == "-f" && i+1 < len(args):
			file = args[i+1]
		case arg == "up" || arg == "down" || arg == "logs" || arg == "pull":
			action = arg
		}
	}
	var lines []string
	var rendered *types.Project
	switch action {
	case "pull":
		service := args[len(args)-1]
		lines = []string{service + " Pulling (simulated)", service + " Pulled"}
	case "up":
		compose, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if rendered, err = loader.LoadWithContext(ctx, types.ConfigDetails{
			WorkingDir: filepath.Dir(file), ConfigFiles: []types.ConfigFile{{Filename: file, Content: compose}},
		}, func(options *loader.Options) {
			options.SetProjectName(name, true)
			options.SkipResolveEnvironment = true
		}); err != nil {
			return fmt.Errorf("compose file: %w", err)
		}
		for _, network := range slices.Sorted(maps.Keys(rendered.Networks)) {
			lines = append(lines, "Network "+cmp.Or(rendered.Networks[network].Name, name+"_"+network)+" Created")
		}
		for _, service := range slices.Sorted(maps.Keys(rendered.Services)) {
			lines = append(lines, "Container "+cmp.Or(rendered.Services[service].ContainerName, fmt.Sprintf("%s-%s-1", name, service))+" Started")
		}
	case "down":
		lines = []string{"Stopping " + name, "Removing containers", "Removing networks"}
	case "logs":
		lines = []string{"(simulated containers write no logs)"}
	default:
		return fmt.Errorf("simulated Compose cannot run %s", strings.Join(args, " "))
	}
	for _, line := range lines {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.step):
		}
		_, _ = fmt.Fprintln(output, line)
	}
	switch action {
	case "up":
		r.docker.up(rendered)
	case "down":
		r.docker.down(name)
	}
	return nil
}

// clone copies a report so that callers cannot change Docker's state; the
// slices inside its items are not changed by anyone.
func clone[T any](items []T) []T {
	return append([]T(nil), items...)
}
