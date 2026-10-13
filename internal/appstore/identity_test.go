package appstore

import (
	"context"
	"regexp"
	"testing"
)

// Images that run as a fixed non-root user get the app identity, so they
// can write the folders made for it.
func TestFixedUserImagesRunAsTheAppIdentity(t *testing.T) {
	entries, err := Catalog()
	if err != nil {
		t.Fatal(err)
	}
	policy := Policy{AppDataRoot: "/srv/a-nas/data/apps", DataRoot: "/srv/a-nas/data/spaces/shared", TZ: "UTC", ReservedPorts: []uint16{8080}}
	listed := map[string]bool{}
	for _, entry := range entries {
		listed[entry.App.ID] = true
		if !runAsAppIdentity[entry.App.ID] {
			continue
		}
		plan, err := Render(context.Background(), entry, policy, Identity{Username: "app-" + entry.App.ID, UID: 30006, GID: 30006})
		if err != nil {
			t.Fatalf("%s: %v", entry.App.ID, err)
		}
		if !regexp.MustCompile(`user: "?30006:30006"?`).Match(plan.Compose) {
			t.Errorf("%s does not run as the app identity:\n%s", entry.App.ID, plan.Compose)
		}
	}
	for id := range runAsAppIdentity {
		if !listed[id] {
			t.Errorf("%s runs as the app identity but is not in the catalog", id)
		}
	}
}
