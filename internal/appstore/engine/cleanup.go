package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/zhongwater123/A-NAS/internal/appstore"
	"github.com/zhongwater123/A-NAS/internal/atomicfile"
)

func (s *Store) imagesPath(id string) string {
	return filepath.Join(s.stateDir, "apps", id, "images.json")
}

func (s *Store) hasImages(id string) (bool, error) {
	_, err := os.Stat(s.imagesPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// saveImages atomically records what cleanup must remember after containers
// disappear. Sync before deleting resources; a process restart must not lose
// the only remaining references to an incomplete uninstall's images.
func (s *Store) saveImages(id string, images []string) error {
	data, err := json.Marshal(images)
	if err != nil {
		return err
	}
	path := s.imagesPath(id)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return atomicfile.Write(path, data, 0o600)
}

// cleanup is shared by uninstall, failed-install rollback, retry and restart
// recovery. Named volumes and bind-mounted user data are never removed.
func (s *Store) cleanup(ctx context.Context, id string, down []string, output io.Writer) error {
	var references []string
	data, err := os.ReadFile(s.imagesPath(id))
	if err == nil {
		err = json.Unmarshal(data, &references)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read app image record: %w", err)
	}
	images, err := s.docker.CleanupImages(ctx, appstore.ProjectName(id), references)
	if err != nil {
		return fmt.Errorf("inspect app images: %w", err)
	}
	if err := s.saveImages(id, images); err != nil {
		return fmt.Errorf("record app images before cleanup: %w", err)
	}
	if err := s.docker.RemoveProjectContainers(ctx, appstore.ProjectName(id)); err != nil {
		return fmt.Errorf("remove app containers and anonymous volumes: %w", err)
	}
	// Compose still owns project networks. Its ordinary down keeps named
	// volumes; the Engine already removed each container's anonymous ones.
	if err := s.runner.Run(ctx, down, output); err != nil {
		return err
	}
	if err := s.docker.RemoveUnusedImages(ctx, images); err != nil {
		return fmt.Errorf("remove unused app images: %w", err)
	}
	if err := os.Remove(s.imagesPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_, _ = fmt.Fprintln(output, "已清理应用容器、匿名卷和未使用的镜像；命名卷与应用数据已保留")
	return nil
}
