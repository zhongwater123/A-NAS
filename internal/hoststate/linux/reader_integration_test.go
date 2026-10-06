package linux_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
	linuxhoststate "github.com/zhongwater123/A-NAS/internal/hoststate/linux"
)

func TestReaderReadsLocalLinuxHost(t *testing.T) {
	if os.Getenv("ANAS_LINUX_INTEGRATION") != "1" {
		t.Skip("set ANAS_LINUX_INTEGRATION=1 on an explicitly approved Linux host")
	}

	state, err := linuxhoststate.New().Read(context.Background())
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if state.System.Hostname == "" || state.System.OperatingSystem.Name == "" || state.System.OperatingSystem.Version == "" {
		t.Fatalf("Read() returned incomplete system state: %#v", state.System)
	}
	if state.System.Health != hoststate.HealthUnknown {
		t.Fatalf("system health = %q, want %q", state.System.Health, hoststate.HealthUnknown)
	}
	if len(state.Disks) == 0 {
		t.Fatal("Read() returned no disks")
	}

	systemDisks := 0
	previousID := ""
	for _, disk := range state.Disks {
		id := disk.ID.String()
		if id <= previousID {
			t.Fatalf("disk IDs are not strictly sorted: %q after %q", id, previousID)
		}
		if strings.ContainsAny(id, `/\\`) {
			t.Fatalf("disk ID %q contains a path separator", id)
		}
		if disk.Health != hoststate.HealthUnknown || disk.TemperatureCelsius != nil {
			t.Fatalf("disk health was assessed without SMART: %#v", disk)
		}
		if disk.Role == hoststate.DiskRoleSystem {
			systemDisks++
		}
		previousID = id
	}
	if systemDisks != 1 {
		t.Fatalf("system disk count = %d, want 1", systemDisks)
	}
}
