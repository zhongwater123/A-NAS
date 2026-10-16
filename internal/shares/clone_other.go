//go:build !linux

package shares

import (
	"errors"
	"os"
)

func cloneFile(dst, src *os.File) error { return errors.New("atomic source cloning is unavailable") }

// Production capture is Linux-only; development platforms do not expose a
// portable directory durability primitive through os.File.
func syncDirectory(string) error { return nil }
