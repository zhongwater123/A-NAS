package fake_test

import (
	"context"
	"errors"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/containers"
	"github.com/zhongwater123/A-NAS/internal/containers/fake"
)

func state(t *testing.T, manager *fake.Manager, id string) containers.Container {
	t.Helper()
	snapshot, err := manager.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, container := range snapshot.Containers {
		if container.ID == id {
			return container
		}
	}
	t.Fatalf("container %s is not in the snapshot", id)
	return containers.Container{}
}

// Each action leaves a container in the state Docker would, with usage only
// while it runs.
func TestActionsChangeTheStateAsDockerWould(t *testing.T) {
	manager := fake.New()
	id := fake.ID("homeassistant")
	for _, step := range []struct {
		action containers.Action
		want   containers.State
		usage  bool
	}{
		{containers.ActionStart, containers.StateRunning, true},
		{containers.ActionStop, containers.StateExited, false},
		{containers.ActionRestart, containers.StateRunning, true},
	} {
		if err := manager.Act(context.Background(), id, step.action); err != nil {
			t.Fatalf("%s: %v", step.action, err)
		}
		if got := state(t, manager, id); got.State != step.want || (got.Usage != nil) != step.usage {
			t.Fatalf("after %s: state %s, usage %v", step.action, got.State, got.Usage)
		}
	}
}

// A snapshot is a copy: changing it changes nothing in the manager.
func TestSnapshotsAreCopies(t *testing.T) {
	manager := fake.New()
	snapshot, err := manager.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first := snapshot.Containers[0]
	snapshot.Containers[0].State = containers.StateDead
	snapshot.Containers[0].Ports[0].HostPort = 1
	snapshot.Containers[0].Usage.CPUPercent = 99
	snapshot.Images[0].Tags[0] = "changed"
	again, err := manager.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Containers[0]; got.State != first.State || got.Ports[0].HostPort != 8096 || got.Usage.CPUPercent != 12.4 || again.Images[0].Tags[0] == "changed" {
		t.Fatalf("the snapshot shares state with the manager: %+v", got)
	}
	for _, container := range again.Containers {
		if !containers.ValidID(container.ID) || !container.State.Valid() {
			t.Fatalf("container %s has ID %q and state %q", container.Name, container.ID, container.State)
		}
	}
}

// Logs end with the newest lines and keep to the requested tail.
func TestLogsKeepToTheTail(t *testing.T) {
	manager := fake.New()
	all, err := manager.Logs(context.Background(), fake.ID("jellyfin"), 0)
	if err != nil || len(all) < 3 {
		t.Fatalf("logs = %d lines, %v", len(all), err)
	}
	tail, err := manager.Logs(context.Background(), fake.ID("jellyfin"), 2)
	if err != nil || len(tail) != 2 || tail[1] != all[len(all)-1] {
		t.Fatalf("tail 2 = %+v, %v", tail, err)
	}
	for i := 1; i < len(all); i++ {
		if all[i].Time.Before(all[i-1].Time) {
			t.Fatalf("line %d is older than the line before it", i)
		}
	}
}

// Requests the manager cannot serve fail with the errors the container API
// maps to its codes.
func TestRefusalsUseTheDomainErrors(t *testing.T) {
	ctx := context.Background()
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	unknown := fake.ID("unknown")
	for _, test := range []struct {
		name string
		err  error
		want error
	}{
		{"short ID", fake.New().Act(ctx, "jellyfin", containers.ActionStart), containers.ErrInvalidID},
		{"unknown action", fake.New().Act(ctx, fake.ID("jellyfin"), "remove"), containers.ErrInvalidAction},
		{"unknown container", fake.New().Act(ctx, unknown, containers.ActionStart), containers.ErrNotFound},
		{"logs of an unknown container", logsError(ctx, fake.New(), unknown), containers.ErrNotFound},
		{"logs by name", logsError(ctx, fake.New(), "jellyfin"), containers.ErrInvalidID},
		{"engine offline", snapshotError(ctx, fake.NewUnavailable()), containers.ErrUnavailable},
		{"action while offline", fake.NewUnavailable().Act(ctx, fake.ID("jellyfin"), containers.ActionStop), containers.ErrUnavailable},
		{"canceled", snapshotError(canceled, fake.New()), context.Canceled},
	} {
		if !errors.Is(test.err, test.want) {
			t.Errorf("%s: %v, want %v", test.name, test.err, test.want)
		}
	}
}

func logsError(ctx context.Context, manager *fake.Manager, id string) error {
	_, err := manager.Logs(ctx, id, 10)
	return err
}

func snapshotError(ctx context.Context, manager *fake.Manager) error {
	_, err := manager.Snapshot(ctx)
	return err
}
