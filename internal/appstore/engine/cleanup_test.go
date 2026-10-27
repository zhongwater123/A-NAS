package engine_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/appstore"
	"github.com/zhongwater123/A-NAS/internal/appstore/engine"
)

func installedForCleanup(t *testing.T) (*engine.Store, string, *fakeDocker, *fakeRunner, appstore.Plan) {
	t.Helper()
	docker := &fakeDocker{}
	runner := &fakeRunner{after: composeOn(docker, []engine.ContainerInfo{{Name: "memos", AppID: "memos", Running: true, Runtime: appstore.RuntimePolicy}})}
	store, state := newStore(t, docker, runner)
	plan, err := store.Plan(context.Background(), "memos", identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Install(context.Background(), "memos", plan.Digest, identity); err != nil {
		t.Fatal(err)
	}
	store.Wait()
	return store, state, docker, runner, plan
}

func TestUninstallRetriesImagesAfterContainersHaveGoneAndAgentRestarted(t *testing.T) {
	store, state, docker, runner, _ := installedForCleanup(t)
	docker.imageErr = errors.New("Docker image removal failed")
	if _, err := store.Uninstall(context.Background(), "memos"); err != nil {
		t.Fatal(err)
	}
	store.Wait()
	apps, err := store.Apps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	status := find(apps, "memos")
	if status.State != appstore.StateFailed || status.Total != 0 || status.Job.Action != appstore.JobUninstall || status.Job.State != appstore.JobFailed {
		t.Fatalf("status = %+v, want retryable cleanup with no containers", status)
	}
	restarted := newStoreIn(t, state, docker, runner)
	if err := restarted.Recover(); err != nil {
		t.Fatal(err)
	}
	docker.imageErr = nil
	if _, err := restarted.Uninstall(context.Background(), "memos"); err != nil {
		t.Fatal(err)
	}
	restarted.Wait()
	if !slices.Equal(docker.cleanedImages, []string{"sha256:installed"}) {
		t.Fatalf("cleaned %v, want installed image ID", docker.cleanedImages)
	}
	if _, err := os.Stat(filepath.Join(state, "apps", "memos", "images.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("image record still exists: %v", err)
	}
	apps, err = restarted.Apps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status := find(apps, "memos"); status.State != appstore.StateAvailable || status.Job.State != appstore.JobSucceeded {
		t.Fatalf("status = %+v", status)
	}
}

func TestUninstallRecoveryKeepsImagesAfterContainerRemoval(t *testing.T) {
	store, state, docker, runner, _ := installedForCleanup(t)
	runner.gate = make(chan struct{})
	if _, err := store.Uninstall(context.Background(), "memos"); err != nil {
		t.Fatal(err)
	}
	defer func() { close(runner.gate); store.Wait() }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		containers, err := docker.Containers(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(containers) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("uninstall did not remove containers")
		}
		time.Sleep(time.Millisecond)
	}
	// The process died after deleting containers but before completing down.
	restarted := newStoreIn(t, state, docker, &fakeRunner{})
	if err := restarted.Recover(); err != nil {
		t.Fatal(err)
	}
	restarted.Wait()
	if !slices.Equal(docker.cleanedImages, []string{"sha256:installed"}) {
		t.Fatalf("lost images on restart: %v", docker.cleanedImages)
	}
}

func TestUnreadableImageRecordDoesNotDiscardRemainingContainers(t *testing.T) {
	store, state, docker, _, _ := installedForCleanup(t)
	if err := os.WriteFile(filepath.Join(state, "apps", "memos", "images.json"), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Uninstall(context.Background(), "memos"); err != nil {
		t.Fatal(err)
	}
	store.Wait()
	containers, err := docker.Containers(context.Background())
	if err != nil || len(containers) != 1 {
		t.Fatalf("containers = %v, %v", containers, err)
	}
}
