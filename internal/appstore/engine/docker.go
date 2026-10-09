package engine

import (
	"context"
	"net/netip"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/zhongwater123/A-NAS/internal/appstore"
)

// DockerInspector reads container names, app labels and published ports, the
// daemon's address pools and the subnets of app networks.
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
		info := ContainerInfo{AppID: item.Labels[appstore.AppLabel], Running: item.State == container.StateRunning}
		if len(item.Names) > 0 {
			info.Name = strings.TrimPrefix(item.Names[0], "/")
		}
		for _, port := range item.Ports {
			if port.PublicPort != 0 {
				info.PublishedPorts = append(info.PublishedPorts, port.PublicPort)
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

// composeProjectLabel is the label Compose puts on every network it creates.
const composeProjectLabel = "com.docker.compose.project"
