package engine

import (
	"errors"
	"syscall"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/appstore"
)

func TestStorageChecksBothFilesystemsAndRefusesUnknownSpace(t *testing.T) {
	for _, test := range []struct {
		name, failingPath string
		total, available  uint64
		readErr, want     error
	}{
		{name: "both sufficient", total: 100 << 30, available: 20 << 30},
		{name: "system below reserve", failingPath: "/var/lib", total: 100 << 30, available: 4 << 30, want: appstore.ErrSystemSpace},
		{name: "Docker below reserve", failingPath: "/docker", total: 100 << 30, available: 4 << 30, want: appstore.ErrSystemSpace},
		{name: "small disk below minimum", failingPath: "/docker", total: 20 << 30, available: (2 << 30) - 1, want: appstore.ErrSystemSpace},
		{name: "exact reserve", total: 100 << 30, available: 5 << 30},
		{name: "read failed", failingPath: "/docker", readErr: errors.New("cannot stat"), want: appstore.ErrUnavailable},
		{name: "invalid observation", failingPath: "/docker", total: 0, want: appstore.ErrUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			var observed []string
			err := checkStoragePaths([]string{"/var/lib", "/docker"}, func(path string, stats *syscall.Statfs_t) error {
				observed = append(observed, path)
				stats.Bsize, stats.Blocks, stats.Bavail = 1, 100<<30, 20<<30
				if test.failingPath == "" || path == test.failingPath {
					stats.Blocks, stats.Bavail = test.total, test.available
					return test.readErr
				}
				return nil
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if test.want == nil && len(observed) != 2 {
				t.Fatalf("observed = %v", observed)
			}
		})
	}
}
