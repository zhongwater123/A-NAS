package updater

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

// Main is one run of anas-update.service (`anas-host-agent update`): the
// request the Host Agent left, or the timer's tick. The path unit starts a
// fresh run for any request left while it worked.
func Main(ctx context.Context) error {
	dir := os.Getenv("STATE_DIRECTORY") // systemd's StateDirectory
	if dir == "" {
		dir = DefaultStateDir
	}
	store := Store{Dir: dir}
	request, ok, err := takeRequest(store, time.Now().Add(-staleRequest))
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
	if IsImage(imageOSRelease) {
		return NewImageService(*service).Run(ctx, request)
	}
	return service.Run(ctx, request)
}

// updateSource is the directory of this architecture's update files under
// ANAS_UPDATE_URL, the address in site.conf that a device downloads them
// from (docs/decisions/2026-11-13-device-image-defaults.md); "" reads what a
// push leaves. Every file is checked against the archive key's signature,
// so http serves as well as https.
func updateSource(address string) (string, error) {
	if address == "" {
		return "", nil
	}
	parsed, err := url.Parse(address)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("ANAS_UPDATE_URL is not an http or https address of a directory: %q", address)
	}
	architecture := systemdArchitecture()
	if architecture == "" {
		return "", errors.New("no update files are published for " + runtime.GOARCH)
	}
	return strings.TrimSuffix(parsed.String(), "/") + "/" + architecture, nil
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
