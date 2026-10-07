package engine

import (
	"context"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/zhongwater123/A-NAS/internal/appstore"
)

// DockerInspector reads container names, app labels and published ports.
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
