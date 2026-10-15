//go:build linux

package shares

import (
	"golang.org/x/sys/unix"
	"os"
)

func cloneFile(dst, src *os.File) error { return unix.IoctlFileClone(int(dst.Fd()), int(src.Fd())) }
func syncDirectory(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}
