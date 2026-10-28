package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
	"github.com/opencontainers/go-digest"
)

func (d DockerInspector) CleanupImages(ctx context.Context, project string, references []string) ([]string, error) {
	for _, reference := range references {
		id, err := digest.Parse(reference)
		if err != nil || id.Algorithm() != digest.SHA256 {
			return nil, fmt.Errorf("cleanup record contains a non-immutable image ID: %s", reference)
		}
	}
	containers, err := d.Client.ContainerList(ctx, client.ContainerListOptions{
		All: true, Filters: make(client.Filters).Add("label", composeProjectLabel+"="+project),
	})
	if err != nil {
		return nil, err
	}
	references = slices.Clone(references)
	for _, item := range containers.Items {
		references = append(references, item.ImageID)
	}
	var images []string
	for _, reference := range references {
		image, err := d.Client.ImageInspect(ctx, reference)
		if errdefs.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(image.ID, "sha256:") {
			return nil, fmt.Errorf("invalid image ID for %s", reference)
		}
		if !slices.Contains(images, image.ID) {
			images = append(images, image.ID)
		}
	}
	return images, nil
}

// ContainerRemove's volume option removes anonymous volumes only. Unlike
// compose down --volumes, it retains named volumes, including the local
// driver volumes that point at the application's data folders.
func (d DockerInspector) RemoveProjectContainers(ctx context.Context, project string) error {
	containers, err := d.Client.ContainerList(ctx, client.ContainerListOptions{
		All: true, Filters: make(client.Filters).Add("label", composeProjectLabel+"="+project),
	})
	if err != nil {
		return err
	}
	for _, item := range containers.Items {
		_, err := d.Client.ContainerStop(ctx, item.ID, client.ContainerStopOptions{Timeout: new(10)})
		if err != nil && !errdefs.IsNotFound(err) && !errdefs.IsNotModified(err) {
			return err
		}
		_, err = d.Client.ContainerRemove(ctx, item.ID, client.ContainerRemoveOptions{RemoveVolumes: true})
		if err != nil && !errdefs.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (d DockerInspector) imageInUse(ctx context.Context, id string) (bool, error) {
	containers, err := d.Client.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return false, err
	}
	for _, item := range containers.Items {
		if item.ImageID == id {
			return true, nil
		}
	}
	return false, nil
}

func (d DockerInspector) RemoveUnusedImages(ctx context.Context, images []string) error {
	for _, id := range images {
		used, err := d.imageInUse(ctx, id)
		if err != nil {
			return err
		}
		if used {
			continue
		}
		// Do not force: Docker also protects containers created after the
		// observation, including stopped ones, and images with other tags.
		_, err = d.Client.ImageRemove(ctx, id, client.ImageRemoveOptions{PruneChildren: true})
		if errdefs.IsNotFound(err) {
			continue
		}
		if errdefs.IsConflict(err) {
			// Another tag, container or dependent image still owns this ID.
			// Space reclamation is optional; never force or untag by name.
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}
