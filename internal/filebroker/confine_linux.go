//go:build linux

package filebroker

import (
	"fmt"
	"strings"

	"github.com/landlock-lsm/go-landlock/landlock"
)

// confine restricts the worker with Landlock before it serves anything: it
// may read the data volume and write only the places given (ADR 0022
// decision 4). Renames between those places, such as into the member's
// recycle bin, need the refer right. It fails rather than run unconfined on a
// kernel without Landlock ABI 3 (Linux 6.2); a missing place, such as an
// Agent空间 folder not made yet, is left out.
func confine(volumeRoot, writable string) error {
	rules := []landlock.Rule{landlock.RODirs(volumeRoot)}
	for _, place := range strings.Split(writable, ":") {
		if place != "" {
			rules = append(rules, landlock.RWDirs(place).WithRefer().IgnoreIfMissing())
		}
	}
	if err := landlock.V3.RestrictPaths(rules...); err != nil {
		return fmt.Errorf("confine the file worker with Landlock: %w", err)
	}
	return nil
}
