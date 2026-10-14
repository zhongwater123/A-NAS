package appstore

import (
	"context"
	"slices"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/appid"
)

var runtimePolicy = Policy{AppDataRoot: "/srv/a-nas/data/apps", DataRoot: "/srv/a-nas/data/spaces/shared", TZ: "UTC", ReservedPorts: []uint16{8080}}

func renderCatalog(t *testing.T) map[string]Plan {
	t.Helper()
	entries, err := Catalog()
	if err != nil {
		t.Fatal(err)
	}
	plans := map[string]Plan{}
	for _, entry := range entries {
		secrets, _ := NewSecrets(entry.App.ID, nil)
		plan, err := Render(context.Background(), entry, runtimePolicy, Identity{Username: "app-" + entry.App.ID, UID: 30006, GID: 30006}, secrets)
		if err != nil {
			t.Fatalf("%s: %v", entry.App.ID, err)
		}
		plans[entry.App.ID] = plan
	}
	return plans
}

// The Shared folder's ACL names a-nas-users; containers reach it through
// that group's ID, which the App Center cannot look up on the host.
func TestSharedGIDIsTheMembersGroup(t *testing.T) {
	if appid.SharedGID != accounts.UsersGID {
		t.Fatalf("SharedGID = %d, a-nas-users is %d", appid.SharedGID, accounts.UsersGID)
	}
}

// No catalog service that may run as root mounts the Shared folder, and
// every service that does reaches it as the app identity in a-nas-users.
func TestCatalogServicesReachSharedOnlyAsTheAppIdentity(t *testing.T) {
	for id, plan := range renderCatalog(t) {
		for _, service := range plan.Services {
			if service.Shared == SharedNone {
				continue
			}
			switch service.RunAs {
			case RunAsApp:
				if service.User != "30006:30006" || !slices.Equal(service.Groups, []string{"20000"}) || !slices.Equal(service.CapDrop, []string{"ALL"}) {
					t.Errorf("%s/%s reaches Shared as user %q groups %v cap_drop %v", id, service.Service, service.User, service.Groups, service.CapDrop)
				}
			case RunAsAppAfterInit:
			default:
				t.Errorf("%s/%s mounts Shared while running as %s", id, service.Service, service.RunAs)
			}
		}
	}
}

// Shared mounts are read-only unless the app is listed as writing them.
func TestCatalogWritesSharedOnlyWhereListed(t *testing.T) {
	plans := renderCatalog(t)
	for id, plan := range plans {
		for _, mount := range plan.Mounts {
			if mount.Kind == MountShared && mount.ReadOnly == sharedWritable(id, mount.ContainerPath) {
				t.Errorf("%s mounts %s read-only = %v, listed as writable = %v", id, mount.ContainerPath, mount.ReadOnly, sharedWritable(id, mount.ContainerPath))
			}
		}
	}
	for id, targets := range sharedWrites {
		plan, ok := plans[id]
		if !ok {
			t.Errorf("%s writes Shared but is not in the catalog", id)
			continue
		}
		for _, target := range targets {
			if !slices.ContainsFunc(plan.Mounts, func(mount Mount) bool { return mount.Kind == MountShared && mount.ContainerPath == target }) {
				t.Errorf("%s writes %s, which is not a Shared mount of the app", id, target)
			}
		}
	}
}

// Reviewed services name real catalog services and run as the app identity.
func TestReviewedServicesRunAsTheAppIdentity(t *testing.T) {
	plans := renderCatalog(t)
	for id, services := range reviewedRunAs {
		plan, ok := plans[id]
		if !ok {
			t.Errorf("%s is reviewed but not in the catalog", id)
			continue
		}
		for name, runAs := range services {
			index := slices.IndexFunc(plan.Services, func(service ServiceRuntime) bool { return service.Service == name })
			if index < 0 {
				t.Errorf("%s has no service %s", id, name)
				continue
			}
			if got := plan.Services[index]; got.RunAs != runAs || got.User != "30006:30006" {
				t.Errorf("%s/%s runs as %s user %q, want %s as the app identity", id, name, got.RunAs, got.User, runAs)
			}
		}
	}
}
