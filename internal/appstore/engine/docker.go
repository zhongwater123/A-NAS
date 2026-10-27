package engine

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/zhongwater123/A-NAS/internal/appstore"
)

// DockerInspector reads container names, app labels and published ports, the
// daemon's address pools and API version, and the networks and volume
// mounts of app projects.
type DockerInspector struct {
	Client *client.Client
}

func (d DockerInspector) Containers(ctx context.Context) ([]ContainerInfo, error) {
	list, err := d.Client.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, err
	}
	infos := make([]ContainerInfo, 0, len(list.Items))
	for _, item := range list.Items {
		info := ContainerInfo{
			AppID: item.Labels[appstore.AppLabel], Running: item.State == container.StateRunning,
			Service: item.Labels[composeServiceLabel], State: string(item.State), ExitCode: exitCode(item.Status),
			Runtime: item.Labels[appstore.RuntimeLabel],
		}
		if len(item.Names) > 0 {
			info.Name = strings.TrimPrefix(item.Names[0], "/")
		}
		for _, port := range item.Ports {
			if port.PublicPort != 0 {
				info.PublishedPorts = append(info.PublishedPorts, port.PublicPort)
			}
		}
		// List omits bindings when a container is stopped or only created.
		// Keep its configured ports reserved so two installed apps cannot
		// silently share a URL after one of them is restarted.
		inspected, err := d.Client.ContainerInspect(ctx, item.ID, client.ContainerInspectOptions{})
		if err != nil {
			return nil, err
		}
		if inspected.Container.HostConfig == nil {
			return nil, fmt.Errorf("container %s has no host configuration", item.ID)
		}
		for _, bindings := range inspected.Container.HostConfig.PortBindings {
			for _, binding := range bindings {
				if binding.HostPort == "" {
					continue
				}
				port, err := strconv.ParseUint(binding.HostPort, 10, 16)
				if err != nil {
					return nil, fmt.Errorf("container %s has invalid host port: %w", item.ID, err)
				}
				if port != 0 && !slices.Contains(info.PublishedPorts, uint16(port)) {
					info.PublishedPorts = append(info.PublishedPorts, uint16(port))
				}
			}
		}
		infos = append(infos, info)
	}
	return infos, nil
}

func (d DockerInspector) AddressPools(ctx context.Context) ([]netip.Prefix, error) {
	result, err := d.Client.Info(ctx, client.InfoOptions{})
	if err != nil {
		return nil, err
	}
	pools := make([]netip.Prefix, 0, len(result.Info.DefaultAddressPools))
	for _, pool := range result.Info.DefaultAddressPools {
		pools = append(pools, pool.Base)
	}
	return pools, nil
}

func (d DockerInspector) ProjectNetworks(ctx context.Context, project string) ([]NetworkInfo, error) {
	list, err := d.Client.NetworkList(ctx, client.NetworkListOptions{
		Filters: make(client.Filters).Add("label", composeProjectLabel+"="+project),
	})
	if err != nil {
		return nil, err
	}
	networks := make([]NetworkInfo, 0, len(list.Items))
	for _, item := range list.Items {
		network := NetworkInfo{Name: item.Name}
		for _, config := range item.IPAM.Config {
			if config.Subnet.IsValid() {
				network.Subnets = append(network.Subnets, config.Subnet)
			}
		}
		networks = append(networks, network)
	}
	return networks, nil
}

func (d DockerInspector) APIVersion(ctx context.Context) (string, error) {
	version, err := d.Client.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		return "", err
	}
	return version.APIVersion, nil
}

// ProjectMounts reads each project container's volumes. The subpath comes
// from the mount spec: a Compose that falls back to the legacy bind API
// leaves no spec, so the container gets the whole volume and Subpath is empty.
func (d DockerInspector) ProjectMounts(ctx context.Context, project string) ([]MountInfo, error) {
	list, err := d.Client.ContainerList(ctx, client.ContainerListOptions{
		All: true, Filters: make(client.Filters).Add("label", composeProjectLabel+"="+project),
	})
	if err != nil {
		return nil, err
	}
	var mounts []MountInfo
	for _, item := range list.Items {
		inspected, err := d.Client.ContainerInspect(ctx, item.ID, client.ContainerInspectOptions{})
		if err != nil {
			return nil, err
		}
		subpaths := map[string]string{}
		if inspected.Container.HostConfig != nil {
			for _, spec := range inspected.Container.HostConfig.Mounts {
				if spec.VolumeOptions != nil {
					subpaths[spec.Target] = spec.VolumeOptions.Subpath
				}
			}
		}
		name := strings.TrimPrefix(inspected.Container.Name, "/")
		for _, point := range inspected.Container.Mounts {
			if point.Type == mount.TypeVolume {
				mounts = append(mounts, MountInfo{Container: name, Volume: point.Name, Target: point.Destination, Subpath: subpaths[point.Destination], ReadOnly: !point.RW})
			}
		}
	}
	return mounts, nil
}

// ProjectContainers reads each project container's state, exit code,
// restart count and health.
func (d DockerInspector) ProjectContainers(ctx context.Context, project string) ([]ContainerState, error) {
	list, err := d.Client.ContainerList(ctx, client.ContainerListOptions{
		All: true, Filters: make(client.Filters).Add("label", composeProjectLabel+"="+project),
	})
	if err != nil {
		return nil, err
	}
	states := make([]ContainerState, 0, len(list.Items))
	for _, item := range list.Items {
		inspected, err := d.Client.ContainerInspect(ctx, item.ID, client.ContainerInspectOptions{})
		if err != nil {
			return nil, err
		}
		state := ContainerState{Service: item.Labels[composeServiceLabel], State: string(item.State), RestartCount: inspected.Container.RestartCount}
		if state.Service == "" {
			state.Service = strings.TrimPrefix(inspected.Container.Name, "/")
		}
		if current := inspected.Container.State; current != nil {
			state.ExitCode = current.ExitCode
			if current.Health != nil {
				state.Health = string(current.Health.Status)
			}
		}
		states = append(states, state)
	}
	return states, nil
}

// ProjectRuntimes reads each project container's configured user, added
// groups, dropped capabilities and security options.
func (d DockerInspector) ProjectRuntimes(ctx context.Context, project string) ([]RuntimeInfo, error) {
	list, err := d.Client.ContainerList(ctx, client.ContainerListOptions{
		All: true, Filters: make(client.Filters).Add("label", composeProjectLabel+"="+project),
	})
	if err != nil {
		return nil, err
	}
	runtimes := make([]RuntimeInfo, 0, len(list.Items))
	for _, item := range list.Items {
		inspected, err := d.Client.ContainerInspect(ctx, item.ID, client.ContainerInspectOptions{})
		if err != nil {
			return nil, err
		}
		runtime := RuntimeInfo{Container: strings.TrimPrefix(inspected.Container.Name, "/"), Service: item.Labels[composeServiceLabel]}
		if config := inspected.Container.Config; config != nil {
			runtime.User = config.User
		}
		if host := inspected.Container.HostConfig; host != nil {
			runtime.GroupAdd = append(runtime.GroupAdd, host.GroupAdd...)
			runtime.CapDrop = append(runtime.CapDrop, host.CapDrop...)
			runtime.SecurityOpt = append(runtime.SecurityOpt, host.SecurityOpt...)
		}
		runtimes = append(runtimes, runtime)
	}
	return runtimes, nil
}

// exitCode reads the code from a container status such as "Exited (1) 2
// minutes ago" or "Restarting (1) 5 seconds ago"; 0 when there is none.
func exitCode(status string) int {
	open, end := strings.Index(status, "("), strings.Index(status, ")")
	if open < 0 || end < open {
		return 0
	}
	code, err := strconv.Atoi(status[open+1 : end])
	if err != nil {
		return 0
	}
	return code
}

// composeProjectLabel is the label Compose puts on every container and
// network it creates; composeServiceLabel names the container's service.
const (
	composeProjectLabel = "com.docker.compose.project"
	composeServiceLabel = "com.docker.compose.service"
)
