package files_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/files"
)

func TestScheduledSnapshotHonorsRetentionChangedDuringCapture(t *testing.T) {
	for _, disable := range []bool{false, true} {
		name := "increase retention"
		if disable {
			name = "disable schedule"
		}
		t.Run(name, func(t *testing.T) {
			f := newSnapshotFixture(t)
			var duringCapture func()
			backend := newMemorySnapshots(func(context.Context, func(files.SnapshotObject) error) error {
				if duringCapture != nil {
					duringCapture()
				}
				return nil
			})
			f.backend = backend
			f.open()
			settings := files.SnapshotScheduleSettings{Enabled: true, Frequency: files.FrequencyHourly, Time: "00:15", Keep: 1}
			f.setSchedule(f.admin, f.private, settings)
			f.clock.Set(at(0, 11, 15))
			f.runDue()
			first := f.snapshots(f.private)[0]
			// The user saves a new policy while the Host Agent is streaming
			// the next snapshot's manifest, before retention can delete anything.
			duringCapture = func() {
				settings.Enabled = !disable
				if !disable {
					settings.Keep = 3
				}
				f.setSchedule(f.admin, f.private, settings)
			}
			f.clock.Set(at(0, 12, 15))
			f.runDue()
			if got := f.snapshots(f.private); len(got) != 2 || !backend.exists(first.ID) {
				t.Fatalf("successful policy update lost an existing snapshot: catalog=%d, first present=%v; want 2 and true", len(got), backend.exists(first.ID))
			}
		})
	}
}

type blockedRetentionBackend struct {
	*memorySnapshots
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockedRetentionBackend) Delete(ctx context.Context, spaceID, snapshotID string) error {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
		return b.memorySnapshots.Delete(ctx, spaceID, snapshotID)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestRetentionOrdersChangesAndKeepingWithoutBlockingOtherSpaces(t *testing.T) {
	f := newSnapshotFixture(t)
	backend := &blockedRetentionBackend{
		memorySnapshots: newMemorySnapshots(func(context.Context, func(files.SnapshotObject) error) error { return nil }),
		entered:         make(chan struct{}), release: make(chan struct{}),
	}
	f.backend = backend
	f.open()
	settings := files.SnapshotScheduleSettings{Enabled: true, Frequency: files.FrequencyHourly, Time: "00:15", Keep: 1}
	f.setSchedule(f.admin, f.private, settings)
	f.clock.Set(at(0, 11, 15))
	f.runDue()
	first := f.snapshots(f.private)[0]
	f.clock.Set(at(0, 12, 15))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- f.files.RunDueSnapshots(ctx, f.accounts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	var release sync.Once
	unblock := func() { release.Do(func() { close(backend.release) }) }
	finished := false
	t.Cleanup(func() {
		cancel()
		unblock()
		if !finished {
			_ = <-done // cleanup already canceled the run after a failed assertion
		}
	})
	select {
	case <-backend.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not reach the backend")
	}
	// Independent spaces can still save their policy while this deletion
	// waits for the Host Agent. The shared space has not begun capture.
	other, otherCancel := context.WithTimeout(context.Background(), time.Second)
	defer otherCancel()
	if _, err := f.files.UpdateSnapshotSchedule(other, f.admin, f.shared.ID, settings); err != nil {
		t.Fatalf("another space's update was blocked: %v", err)
	}
	settings.Keep = 3
	update, updateCancel := context.WithTimeout(context.Background(), time.Second)
	defer updateCancel()
	if _, err := f.files.UpdateSnapshotSchedule(update, f.admin, f.private.ID, settings); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("policy update overtook an in-flight deletion: %v", err)
	}
	keep, keepCancel := context.WithTimeout(context.Background(), time.Second)
	defer keepCancel()
	if _, err := f.files.KeepSnapshot(keep, f.admin, first.ID); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("keep reported success for a snapshot being expired: %v", err)
	}
	unblock()
	err := <-done
	finished = true
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if got := f.schedule(f.admin, f.private); got.Keep != 1 {
		t.Fatalf("canceled update was persisted: keep=%d", got.Keep)
	}
	if _, err := f.files.KeepSnapshot(context.Background(), f.admin, first.ID); !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("keeping an already expired snapshot: %v", err)
	}
	f.setSchedule(f.admin, f.private, settings)
	f.clock.Set(at(0, 13, 15))
	f.runDue()
	if got := f.snapshots(f.private); len(got) != 2 {
		t.Fatalf("new policy did not apply after cleanup: got %d snapshots", len(got))
	}
}
