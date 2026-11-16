package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/filesystem"
	"github.com/zhongwater123/A-NAS/internal/modulemanifest"
	"github.com/zhongwater123/A-NAS/internal/modules"
	"github.com/zhongwater123/A-NAS/internal/scoring"
	"github.com/zhongwater123/A-NAS/internal/scoringapi"
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
)

// scoringPhotosFolder is the Shared folder the photo scoring module reads
// (docs/specs/photo-scoring.md); its install script creates it on a NAS.
const scoringPhotosFolder = "照片打分"

// configuredModules is the registry of removable modules (ADR 0037). On a
// NAS each is forwarded to its socket and installed while the socket exists.
// Development runs photo scoring in this process, always installed, with
// its store and photos folder on the development volume, and asks the
// account store about sessions the way the module asks the Host Agent.
func configuredModules(ctx context.Context, config Config, sessions *accounts.SessionDirectory, logger *slog.Logger) ([]modules.Module, func(), error) {
	if config.Live() {
		handler, installed := modules.Proxy(scoringapi.ID, config.ModuleSocketDir, logger)
		return []modules.Module{{ID: scoringapi.ID, Mounts: scoringapi.Mounts(), Handler: handler, Installed: installed}}, func() {}, nil
	}
	root := filepath.Join(config.DataMount, modulemanifest.StoreName(scoringapi.ID))
	photos := filepath.Join(config.DataMount, "spaces", "shared", scoringPhotosFolder)
	for _, directory := range []string{root, photos} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, nil, err
		}
	}
	store, err := scoring.Open(root, scoring.Options{PhotosDir: photos, Photos: filesystem.NewRootFileSystem(photos, false), Logger: logger})
	if err != nil {
		return nil, nil, err
	}
	handler := serviceproxy.Local(scoringapi.Start(ctx, store, sessions, version, logger))
	always := func() bool { return true }
	return []modules.Module{{ID: scoringapi.ID, Mounts: scoringapi.Mounts(), Handler: handler, Installed: always}},
		func() { _ = store.Close() }, nil
}
