package fake_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/appid"
	"github.com/zhongwater123/A-NAS/internal/appstore"
	"github.com/zhongwater123/A-NAS/internal/appstore/engine/enginetest"
	"github.com/zhongwater123/A-NAS/internal/appstore/fake"
)

func identity(id string) appstore.Identity {
	return appstore.Identity{Username: appid.Username(id), UID: appid.FirstUID + 7, GID: appid.FirstUID + 7}
}

// Every catalog app, started by the simulated Compose, is reported as the
// install checks expect of Docker: the contract the real Docker meets too
// (#194).
func TestEveryCatalogAppKeepsToTheInspectorContract(t *testing.T) {
	store, err := fake.New(0)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := appstore.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	docker, runner := fake.NewDocker(0)
	multiService := false
	for _, entry := range entries {
		plan, err := store.Plan(context.Background(), entry.App.ID, identity(entry.App.ID))
		if err != nil {
			t.Fatalf("plan %s: %v", entry.App.ID, err)
		}
		multiService = multiService || len(plan.Containers) > 1
		t.Run(entry.App.ID, func(t *testing.T) { enginetest.CheckProject(t, docker, runner, plan) })
	}
	if !multiService {
		t.Fatal("no catalog app has more than one service")
	}
}

// An app of several services installs through every check of the engine,
// which reads back what the simulated Compose created, and is reported with
// all of its containers running.
func TestAnAppOfSeveralServicesInstallsAndRuns(t *testing.T) {
	ctx := context.Background()
	store, err := fake.New(time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := store.Plan(ctx, "immich", identity("immich"))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Containers) < 2 || len(plan.Networks) == 0 {
		t.Fatalf("immich plans %v on %v; the test needs several services and a network", plan.Containers, plan.Networks)
	}
	if _, err := store.Install(ctx, "immich", plan.Digest, identity("immich")); err != nil {
		t.Fatal(err)
	}
	store.Wait()
	apps, err := store.Apps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, app := range apps {
		if app.ID != "immich" {
			continue
		}
		if app.Job == nil || app.Job.State != appstore.JobSucceeded {
			t.Fatalf("install job = %+v", app.Job)
		}
		if app.State != appstore.StateInstalled || app.Running != len(plan.Containers) || app.Total != len(plan.Containers) {
			t.Fatalf("immich = %s, %d of %d running; want installed with all %d", app.State, app.Running, app.Total, len(plan.Containers))
		}
		return
	}
	t.Fatal("immich is not listed")
}

// The simulated containers publish their ports, so an install whose port
// another app holds is refused as on a NAS: filedrop and pyload both
// publish 8000.
func TestAnInstallOnATakenPortIsRefused(t *testing.T) {
	ctx := context.Background()
	store, err := fake.New(time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	install := func(id string) error {
		plan, err := store.Plan(ctx, id, identity(id))
		if err != nil {
			return err
		}
		_, err = store.Install(ctx, id, plan.Digest, identity(id))
		store.Wait()
		return err
	}
	if err := install("filedrop"); err != nil {
		t.Fatal(err)
	}
	if err := install("pyload"); !errors.Is(err, appstore.ErrPortInUse) || !strings.Contains(err.Error(), "8000") {
		t.Fatalf("installing pyload beside filedrop = %v, want ErrPortInUse on 8000", err)
	}
}
