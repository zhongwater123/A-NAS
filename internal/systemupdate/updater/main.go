package updater

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// Where a device installed with apt keeps what the service works with
// (docs/runbooks/deploy-a-nas-packages.md).
const (
	DefaultStateDir = "/var/lib/a-nas-update"
	localSource     = "/var/local/a-nas-repository"
	sourcesFile     = "/etc/apt/sources.list.d/a-nas.sources"
	backups         = "/var/backups/a-nas"
)

// Main is one run of anas-update.service (`anas-host-agent update`): the
// request the Host Agent left, or the timer's tick.
func Main(ctx context.Context) error {
	dir := os.Getenv("STATE_DIRECTORY") // systemd's StateDirectory
	if dir == "" {
		dir = DefaultStateDir
	}
	store := Store{Dir: dir}
	request, err := store.TakeRequest()
	if err != nil {
		return err
	}
	staging := os.Getenv("ANAS_UPDATE_STAGING")
	if !filepath.IsAbs(staging) {
		staging = ""
	}
	service := &Service{
		Store:     store,
		Commands:  execCommands{},
		Staging:   staging,
		Local:     localSource,
		Sources:   sourcesFile,
		Backups:   backups,
		FreeBytes: freeBytes,
	}
	return service.Run(ctx, request)
}

// execCommands runs the host's commands with the unit's environment, which
// sets LC_ALL=C for output the service reads and DEBIAN_FRONTEND for apt.
type execCommands struct{}

func (execCommands) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func freeBytes(path string) (int64, error) {
	var stats syscall.Statfs_t
	if err := syscall.Statfs(path, &stats); err != nil {
		return 0, err
	}
	return int64(stats.Bavail) * int64(stats.Bsize), nil
}
