package linux

import (
	"errors"
	"testing"
)

func TestSystemStorageObservesStateFilesystem(t *testing.T) {
	reader := newReader(dependencies{readFilesystemUsage: func(path string) (filesystemUsage, error) {
		if path != "/var/lib" {
			t.Fatalf("observed %q, want filesystem holding system state", path)
		}
		return filesystemUsage{total: 100 << 30, available: 25 << 30}, nil
	}})
	usage := reader.systemStorage()
	if usage == nil || usage.TotalBytes != 100<<30 || usage.AvailableBytes != 25<<30 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestSystemStorageDoesNotInventUnavailableUsage(t *testing.T) {
	for _, test := range []struct {
		name  string
		usage filesystemUsage
		err   error
	}{
		{name: "read error", err: errors.New("unavailable")},
		{name: "zero capacity"},
		{name: "available exceeds capacity", usage: filesystemUsage{total: 1, available: 2}},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := newReader(dependencies{readFilesystemUsage: func(string) (filesystemUsage, error) { return test.usage, test.err }})
			if got := reader.systemStorage(); got != nil {
				t.Fatalf("usage = %+v, want unknown", got)
			}
		})
	}
}
