//go:build dockerintegration

package engine_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moby/moby/client"

	"github.com/zhongwater123/A-NAS/internal/appstore"
	"github.com/zhongwater123/A-NAS/internal/appstore/engine"
	"github.com/zhongwater123/A-NAS/internal/appstore/engine/enginetest"
)

// TestRealDockerRunsAServiceAsItsPlan renders a plan, starts it with the
// Docker Compose CLI and checks what Docker reports against the contract
// that the install checks and the fake engine rely on (enginetest), so the
// runtime and mount checks are known to read what a real Docker reports
// (ADR 0022).
// It needs Docker with Compose 2.30 or later and a local image with a shell
// and setfacl, named by ANAS_TEST_IMAGE; it creates and removes a folder
// laid out like the data volume, owned through a helper container.
func TestRealDockerRunsAServiceAsItsPlan(t *testing.T) {
	image := os.Getenv("ANAS_TEST_IMAGE")
	if os.Getenv("ANAS_DOCKER_INTEGRATION") != "1" || image == "" {
		t.Skip("set ANAS_DOCKER_INTEGRATION=1 and ANAS_TEST_IMAGE")
	}
	ctx := context.Background()
	root := t.TempDir()
	docker := func(args ...string) string {
		t.Helper()
		output, err := exec.Command("docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, output)
		}
		return string(output)
	}
	// Shared as A-NAS sets it up: a-nas-users holds the ACL; the app's own
	// folder belongs to the app identity.
	docker("run", "--rm", "-v", root+":/v", image, "sh", "-ec", `
		acl="u::rwx,g::---,o::---,m::rwx,g:20000:rwx"
		mkdir -p /v/spaces/shared/Media && chmod 0770 /v/spaces/shared /v/spaces/shared/Media
		setfacl -m "$acl" /v/spaces/shared /v/spaces/shared/Media
		setfacl -d -m "$acl" /v/spaces/shared /v/spaces/shared/Media
		echo song > /v/spaces/shared/Media/song.txt && chown 20100:20100 /v/spaces/shared/Media/song.txt
		install -d -m 0700 /v/apps && install -d -o 30005 -g 30005 -m 0750 /v/apps/demo /v/apps/demo/data`)
	t.Cleanup(func() {
		_ = exec.Command("docker", "run", "--rm", "-v", root+":/v", image, "rm", "-rf", "/v/spaces", "/v/apps").Run()
	})

	policy := appstore.Policy{AppDataRoot: filepath.Join(root, "apps"), DataRoot: filepath.Join(root, "spaces", "shared"), TZ: "UTC"}
	identity := appstore.Identity{Username: "app-demo", UID: 30005, GID: 30005}
	entry := appstore.Entry{App: appstore.App{ID: "demo", Title: "Demo"}, Compose: []byte(`
services:
  web:
    image: ` + image + `
    user: "1000"
    command: ["sleep", "infinity"]
    network_mode: bridge
    volumes:
      - /DATA/AppData/$AppID/data:/data
      - /DATA/Media:/media
`)}
	plan, err := appstore.Render(ctx, entry, policy, identity, nil)
	if err != nil {
		t.Fatal(err)
	}
	engineClient, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	// The table the fake engine keeps to as well; the project runs until
	// the test ends.
	enginetest.CheckProject(t, engine.DockerInspector{Client: engineClient}, engine.CLIRunner{}, plan)

	// What the process can do, seen from inside.
	name := plan.Containers[0]
	if id := docker("exec", name, "id"); !strings.Contains(id, "uid=30005") || !strings.Contains(id, "20000") {
		t.Errorf("id = %s, want the app identity in a-nas-users", id)
	}
	if status := docker("exec", name, "grep", "-E", "^(CapEff|NoNewPrivs)", "/proc/self/status"); !strings.Contains(status, "CapEff:\t0000000000000000") || !strings.Contains(status, "NoNewPrivs:\t1") {
		t.Errorf("status = %s, want no capabilities and no new privileges", status)
	}
	docker("exec", name, "sh", "-c", "cat /media/song.txt && echo data > /data/file")
	if output, err := exec.Command("docker", "exec", name, "touch", "/media/new").CombinedOutput(); err == nil || !strings.Contains(string(output), "Read-only") {
		t.Errorf("writing the read-only Shared folder: %v %s", err, output)
	}
}
