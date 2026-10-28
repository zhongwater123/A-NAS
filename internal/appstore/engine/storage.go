package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"syscall"

	"github.com/moby/moby/client"
	"github.com/zhongwater123/A-NAS/internal/appstore"
)

// CheckStorage covers both system state and Docker's actual storage root;
// changing Docker's data-root must not silently bypass installation checks.
func (d DockerInspector) CheckStorage(ctx context.Context) error {
	info, err := d.Client.Info(ctx, client.InfoOptions{})
	if err != nil {
		return fmt.Errorf("%w: inspect Docker storage: %v", appstore.ErrUnavailable, err)
	}
	if !filepath.IsAbs(info.Info.DockerRootDir) {
		return fmt.Errorf("%w: Docker storage root is missing", appstore.ErrUnavailable)
	}
	return checkStoragePaths([]string{"/var/lib", filepath.Clean(info.Info.DockerRootDir)}, syscall.Statfs)
}

func checkStoragePaths(paths []string, statfs func(string, *syscall.Statfs_t) error) error {
	for _, path := range slices.Compact(paths) {
		var stats syscall.Statfs_t
		if err := statfs(path, &stats); err != nil {
			return fmt.Errorf("%w: inspect storage at %s: %v", appstore.ErrUnavailable, path, err)
		}
		if stats.Bsize <= 0 || stats.Blocks == 0 || stats.Bavail > stats.Blocks {
			return fmt.Errorf("%w: invalid storage usage at %s", appstore.ErrUnavailable, path)
		}
		total, available := uint64(stats.Blocks)*uint64(stats.Bsize), uint64(stats.Bavail)*uint64(stats.Bsize)
		// A preflight floor, not a quota or an estimate of the images' size.
		// The install re-renders its plan and therefore observes this again.
		if available < max(total/20, uint64(2<<30)) {
			return fmt.Errorf("%w: %s has %d bytes available", appstore.ErrSystemSpace, path, available)
		}
	}
	return nil
}
