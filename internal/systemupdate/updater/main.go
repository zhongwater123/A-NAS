package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// Where a device installed with apt keeps what the service works with
// (docs/runbooks/deploy-a-nas-packages.md).
const (
	DefaultStateDir = "/var/lib/a-nas-update"
	localSource     = "/var/local/a-nas-repository"
	sourcesFile     = "/etc/apt/sources.list.d/a-nas.sources"
	backups         = "/var/backups/a-nas"
	aptArchives     = "/var/cache/apt/archives"
)

// staleRequest is how long a request waits for the run that takes it: the
// Host Agent starts the unit as it leaves one, so an older one belongs to a
// start that failed.
const staleRequest = 10 * time.Minute

// Main is one run of anas-update.service (`anas-host-agent update`): the
// request the Host Agent left, or the timer's tick, and then any request
// left while it worked.
func Main(ctx context.Context) error {
	dir := os.Getenv("STATE_DIRECTORY") // systemd's StateDirectory
	if dir == "" {
		dir = DefaultStateDir
	}
	store := Store{Dir: dir}
	started := time.Now()
	request, ok, err := takeRequest(store, started.Add(-staleRequest))
	if err != nil {
		return err
	}
	if !ok {
		request = Request{Kind: RequestTick}
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
		Archives:  aptArchives,
		FreeBytes: freeBytes,
	}
	if err := service.Run(ctx, request); err != nil {
		return err
	}
	// The Host Agent's start joins a run already starting, such as the
	// timer's, which has looked for a request before this one was left: it
	// is carried out now, while the page still shows a run.
	for {
		request, ok, err := takeRequest(store, started)
		if err != nil || !ok {
			return err
		}
		if err := service.Run(ctx, request); err != nil {
			return err
		}
	}
}

// takeRequest takes the request left for a run; one that waited too long is
// dropped, and said so in the unit's journal.
func takeRequest(store Store, notBefore time.Time) (Request, bool, error) {
	request, ok, err := store.TakeRequest(notBefore)
	if errors.Is(err, ErrStaleRequest) {
		fmt.Fprintln(os.Stderr, "A-NAS system update: dropped", err)
		return Request{}, false, nil
	}
	return request, ok, err
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
