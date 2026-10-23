//go:build dockerintegration

package engine

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moby/moby/client"

	"github.com/zhongwater123/A-NAS/internal/appstore"
)

// TestRealDockerRunsAServiceAsItsPlan renders a plan, starts it with the
// Docker Compose CLI and checks it the way an install does, so the runtime
// and mount checks are known to read what a real Docker reports (ADR 0022).
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
	composeFile := filepath.Join(t.TempDir(), "docker-compose.yml")
	if err := os.WriteFile(composeFile, plan.Compose, 0o600); err != nil {
		t.Fatal(err)
	}
	runner := CLIRunner{}
	compose := []string{"compose", "-p", plan.Project, "-f", composeFile}
	t.Cleanup(func() { _ = runner.Run(context.Background(), append(compose, "down", "--volumes"), io.Discard) })
	if err := runner.Run(ctx, append(compose, "up", "--detach"), io.Discard); err != nil {
		t.Fatal(err)
	}

	engineClient, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	store := New(nil, policy, t.TempDir(), DockerInspector{Client: engineClient}, runner)
	if err := store.mountProblems(ctx, plan); err != nil {
		t.Errorf("mount check: %v", err)
	}
	if err := store.runtimeProblems(ctx, plan); err != nil {
		t.Errorf("runtime check: %v", err)
	}
	containers, err := store.docker.Containers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, container := range containers {
		if container.AppID == "demo" && container.Runtime != appstore.RuntimePolicy {
			t.Errorf("container %s runtime label = %q", container.Name, container.Runtime)
		}
	}

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
