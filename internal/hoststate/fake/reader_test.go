package fake_test

import (
	"context"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
)

func TestHealthyReaderReturnsDeterministicDebianState(t *testing.T) {
	reader := fake.NewHealthy()

	state, err := reader.Read(context.Background())
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	if got, want := state.System.OperatingSystem.Name, "Debian"; got != want {
		t.Fatalf("OperatingSystem.Name = %q, want %q", got, want)
	}
	if got, want := state.System.OperatingSystem.Version, "13"; got != want {
		t.Fatalf("OperatingSystem.Version = %q, want %q", got, want)
	}
	if got, want := state.System.Architecture, "amd64"; got != want {
		t.Fatalf("Architecture = %q, want %q", got, want)
	}
	if got, want := state.System.Health, hoststate.HealthHealthy; got != want {
		t.Fatalf("System.Health = %q, want %q", got, want)
	}
	if got, want := len(state.Disks), 2; got != want {
		t.Fatalf("len(Disks) = %d, want %d", got, want)
	}
	if got, want := state.Disks[0].ID.String(), "disk:fake-data-01"; got != want {
		t.Fatalf("Disks[0].ID = %q, want %q", got, want)
	}
	if got, want := state.Disks[1].ID.String(), "disk:fake-system-01"; got != want {
		t.Fatalf("Disks[1].ID = %q, want %q", got, want)
	}
	if got, want := state.Disks[0].Role, hoststate.DiskRoleUnassigned; got != want {
		t.Fatalf("data disk role = %q, want %q", got, want)
	}
}

func TestReaderReturnsDeepCopy(t *testing.T) {
	reader := fake.NewHealthy()

	first, err := reader.Read(context.Background())
	if err != nil {
		t.Fatalf("first Read() error = %v", err)
	}
	first.Disks[0].Model = "changed"
	*first.Disks[0].TemperatureCelsius = 99

	second, err := reader.Read(context.Background())
	if err != nil {
		t.Fatalf("second Read() error = %v", err)
	}
	if got, want := second.Disks[0].Model, "A-NAS Fake HDD"; got != want {
		t.Fatalf("second disk model = %q, want %q", got, want)
	}
	if got, want := *second.Disks[0].TemperatureCelsius, 31; got != want {
		t.Fatalf("second disk temperature = %d, want %d", got, want)
	}
}

func TestWarningReaderExposesWarningState(t *testing.T) {
	state, err := fake.NewWarning().Read(context.Background())
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if got, want := state.System.Health, hoststate.HealthWarning; got != want {
		t.Fatalf("System.Health = %q, want %q", got, want)
	}
	if got, want := state.Disks[0].Health, hoststate.HealthWarning; got != want {
		t.Fatalf("Disks[0].Health = %q, want %q", got, want)
	}
}

func TestUnavailableReaderReturnsError(t *testing.T) {
	if _, err := fake.NewUnavailable().Read(context.Background()); err == nil {
		t.Fatal("Read() succeeded, want error")
	}
}
