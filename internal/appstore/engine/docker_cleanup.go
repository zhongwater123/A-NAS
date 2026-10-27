package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

func (d DockerInspector) CleanupImages(ctx context.Context, project string, references []string) ([]string, error) {
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
			if used, checkErr := d.imageInUse(ctx, id); checkErr != nil {
				return checkErr
			} else if used {
				continue
			}
			// Multiple tags require untagging before a non-forced ID delete.
			// Recheck each reference and container use rather than forcing a
			// deletion that could override Docker's stopped-container guard.
			image, inspectErr := d.Client.ImageInspect(ctx, id)
			if errdefs.IsNotFound(inspectErr) {
				continue
			}
			if inspectErr != nil {
				return inspectErr
			}
			if len(image.RepoTags) > 1 {
				for _, tag := range image.RepoTags[:len(image.RepoTags)-1] {
					used, checkErr := d.imageInUse(ctx, id)
					if checkErr != nil {
						return checkErr
					}
					if used {
						break
					}
					current, checkErr := d.Client.ImageInspect(ctx, tag)
					if errdefs.IsNotFound(checkErr) {
						continue
					}
					if checkErr != nil {
						return checkErr
					}
					if current.ID != id {
						return fmt.Errorf("image tag %s changed during cleanup", tag)
					}
					if _, checkErr := d.Client.ImageRemove(ctx, tag, client.ImageRemoveOptions{}); checkErr != nil && !errdefs.IsNotFound(checkErr) {
						return checkErr
					}
				}
				used, checkErr := d.imageInUse(ctx, id)
				if checkErr != nil {
					return checkErr
				}
				if used {
					continue
				}
				_, err = d.Client.ImageRemove(ctx, id, client.ImageRemoveOptions{PruneChildren: true})
				if errdefs.IsNotFound(err) {
					continue
				}
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}
