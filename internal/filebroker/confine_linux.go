//go:build linux

package filebroker

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/landlock-lsm/go-landlock/landlock"
	"golang.org/x/sys/unix"
)

// confine restricts the worker with Landlock before it serves anything: it
// may read the data volume and write only the places given (ADR 0024
// decision 4). Renames between those places, such as into the member's
// recycle bin, need the refer right. It fails rather than run unconfined on a
// kernel without Landlock ABI 3 (Linux 6.2); a missing place, such as an
// Agent空间 folder not made yet, is left out.
func confine(volumeRoot, writable string) error {
	volume, err := unix.Openat2(unix.AT_FDCWD, volumeRoot, &unix.OpenHow{
		Flags: unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS,
	})
	if err != nil {
		return fmt.Errorf("open the confinement volume: %w", err)
	}
	defer func() { _ = unix.Close(volume) }()
	var directories []int
	defer func() {
		for _, fd := range directories {
			_ = unix.Close(fd)
		}
	}()
	// go-landlock opens path rules itself. Give it our held descriptors via
	// procfs, never the mutable paths: checking a path and then asking the
	// library to reopen it would leave a symlink replacement window.
	rules := []landlock.Rule{landlock.RODirs(fmt.Sprintf("/proc/self/fd/%d", volume))}
	for place := range strings.SplitSeq(writable, ":") {
		if place == "" {
			continue
		}
		relative, err := filepath.Rel(volumeRoot, place)
		if err != nil || relative == "." || !filepath.IsLocal(relative) {
			return fmt.Errorf("writable place is not below the confinement volume")
		}
		fd, err := unix.Openat2(volume, relative, &unix.OpenHow{
			Flags:   unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC,
			Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS,
		})
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			return fmt.Errorf("open a confinement grant without following links: %w", err)
		}
		directories = append(directories, fd)
		rules = append(rules, landlock.RWDirs(fmt.Sprintf("/proc/self/fd/%d", fd)).WithRefer())
	}
	if err := landlock.V3.RestrictPaths(rules...); err != nil {
		return fmt.Errorf("confine the file worker with Landlock: %w", err)
	}
	return nil
}
