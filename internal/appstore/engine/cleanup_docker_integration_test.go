//go:build dockerintegration

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
	"github.com/zhongwater123/A-NAS/internal/appstore"
)

// Run only against a disposable local Docker daemon. The unique derived image,
// containers and volumes belong to this test; no global prune is performed.
func TestRealDockerBoundsLogsAndCleansAppResources(t *testing.T) {
	base := os.Getenv("ANAS_TEST_IMAGE")
	if os.Getenv("ANAS_DOCKER_INTEGRATION") != "1" || base == "" {
		t.Skip("set ANAS_DOCKER_INTEGRATION=1 and ANAS_TEST_IMAGE")
	}
	ctx := context.Background()
	engineClient, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engineClient.Close() })
	for _, rollback := range []bool{false, true} {
		t.Run(fmt.Sprintf("rollback=%t", rollback), func(t *testing.T) {
			docker := func(args ...string) string {
				t.Helper()
				out, err := cleanupDocker(args...)
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			id := fmt.Sprintf("cleanup-%d", time.Now().UnixNano())
			image := "anas-" + id + ":test"
			root := t.TempDir()
			build := filepath.Join(root, "build")
			if err := os.Mkdir(build, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(build, "Dockerfile"), []byte("FROM "+base+"\nLABEL anas.cleanup-test="+id+"\nVOLUME [\"/anonymous\"]\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			docker("build", "--network=none", "-t", image, build)
			imageID := docker("image", "inspect", "--format", "{{.Id}}", image)
			t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", imageID).Run() })
			appData := filepath.Join(root, "apps", id, "data")
			if err := os.MkdirAll(appData, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(appData, "keep"), []byte("app data"), 0o644); err != nil {
				t.Fatal(err)
			}
			policy := appstore.Policy{AppDataRoot: filepath.Join(root, "apps"), DataRoot: filepath.Join(root, "shared"), TZ: "UTC"}
			identity := appstore.Identity{Username: "app-" + id, UID: 30005, GID: 30005}
			entry := appstore.Entry{App: appstore.App{ID: id, Title: "Cleanup"}, Compose: []byte(`
services:
  web:
    image: ` + image + `
    command: ["python3", "-c", "import time; [print('x'*1024) for _ in range(45000)]; open('/tmp/logged', 'w').close(); time.sleep(300)"]
    volumes:
      - /DATA/AppData/$AppID/data:/appdata
      - retained:/named
volumes:
  retained: {}
`)}
			var anonymous, named, container, logPath string
			runner := &cleanupRecordingRunner{afterUp: func() error {
				var err error
				container, err = cleanupDocker("ps", "-aq", "--filter", "label=com.docker.compose.project="+appstore.ProjectName(id))
				if err != nil {
					return err
				}
				mountJSON, err := cleanupDocker("inspect", "--format", "{{json .Mounts}}", container)
				if err != nil {
					return err
				}
				var mounts []struct{ Name, Destination string }
				if err := json.Unmarshal([]byte(mountJSON), &mounts); err != nil {
					return err
				}
				for _, m := range mounts {
					if m.Destination == "/anonymous" {
						anonymous = m.Name
					}
					if m.Destination == "/named" {
						named = m.Name
					}
				}
				if anonymous == "" || named == "" {
					return fmt.Errorf("missing test mounts: %+v", mounts)
				}
				if _, err := cleanupDocker("exec", container, "sh", "-ec", "echo retained > /named/keep"); err != nil {
					return err
				}
				logPath, err = cleanupDocker("inspect", "--format", "{{.LogPath}}", container)
				return err
			}, fail: rollback}
			store := New([]appstore.Entry{entry}, policy, filepath.Join(root, "state"), cleanupIntegrationDocker{DockerInspector{Client: engineClient}}, runner)
			store.SetStartupCheck(100*time.Millisecond, 10*time.Millisecond)
			t.Cleanup(func() {
				_ = exec.Command("docker", "rm", "-fv", container).Run()
				for network := range strings.FieldsSeq(docker("network", "ls", "-q", "--filter", "label=com.docker.compose.project="+appstore.ProjectName(id))) {
					_ = exec.Command("docker", "network", "rm", network).Run()
				}
				for volume := range strings.FieldsSeq(docker("volume", "ls", "-q", "--filter", "label=com.docker.compose.project="+appstore.ProjectName(id))) {
					_ = exec.Command("docker", "volume", "rm", volume).Run()
				}
			})
			install := func() {
				t.Helper()
				plan, err := store.Plan(ctx, id, identity)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.Install(ctx, id, plan.Digest, identity); err != nil {
					t.Fatal(err)
				}
				store.Wait()
			}
			jobState := func(want appstore.JobState) {
				t.Helper()
				apps, err := store.Apps(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if len(apps) != 1 || apps[0].Job == nil || apps[0].Job.State != want {
					t.Fatalf("apps = %+v, want job %s", apps, want)
				}
			}
			assertClean := func(imageGone bool) {
				t.Helper()
				if networks := docker("network", "ls", "-q", "--filter", "label=com.docker.compose.project="+appstore.ProjectName(id)); networks != "" {
					t.Fatalf("project networks survived cleanup: %s", networks)
				}
				if out, err := exec.Command("docker", "volume", "inspect", anonymous).CombinedOutput(); err == nil {
					t.Fatalf("anonymous volume survived: %s", out)
				}
				docker("volume", "inspect", named)
				if got := docker("run", "--rm", "--network=none", "-v", named+":/named:ro", base, "cat", "/named/keep"); got != "retained" {
					t.Fatalf("named data = %q", got)
				}
				if got, err := os.ReadFile(filepath.Join(appData, "keep")); err != nil || string(got) != "app data" {
					t.Fatalf("app data = %q, %v", got, err)
				}
				_, err := exec.Command("docker", "image", "inspect", imageID).CombinedOutput()
				if (err != nil) != imageGone {
					t.Fatalf("image missing = %v, want %v", err, imageGone)
				}
			}
			install()
			if rollback {
				jobState(appstore.JobFailed)
				assertClean(true)
				return
			}
			jobState(appstore.JobSucceeded)
			deadline := time.Now().Add(time.Minute)
			for exec.Command("docker", "exec", container, "test", "-f", "/tmp/logged").Run() != nil {
				if time.Now().After(deadline) {
					t.Fatal("log writer did not finish")
				}
				time.Sleep(100 * time.Millisecond)
			}
			files, err := filepath.Glob(logPath + "*")
			if err != nil {
				t.Fatal(err)
			}
			var total int64
			for _, file := range files {
				info, err := os.Stat(file)
				if err != nil {
					t.Fatal(err)
				}
				total += info.Size()
				if info.Size() > 10*1024*1024+4096 {
					t.Fatalf("unbounded log %s: %d", file, info.Size())
				}
			}
			if len(files) < 2 || len(files) > 3 || total > 30*1024*1024+4096 {
				t.Fatalf("logs: %d files, %d bytes", len(files), total)
			}
			t.Logf("45 MB output bounded to %d files, %d bytes", len(files), total)
			// A stopped container outside the project protects the shared image.
			other := docker("create", "--name", id+"-other", image, "true")
			t.Cleanup(func() { _ = exec.Command("docker", "rm", "-v", other).Run() })
			uninstall := func() {
				t.Helper()
				if _, err := store.Uninstall(ctx, id); err != nil {
					t.Fatal(err)
				}
				store.Wait()
				jobState(appstore.JobSucceeded)
			}
			uninstall()
			assertClean(false)
			docker("rm", "-v", other)
			install()
			jobState(appstore.JobSucceeded)
			docker("tag", image, image+"-alias")
			t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", image+"-alias").Run() })
			if _, err := store.Uninstall(ctx, id); err != nil {
				t.Fatal(err)
			}
			store.Wait()
			jobState(appstore.JobFailed)
			assertClean(false)
			if got := docker("image", "inspect", "--format", "{{.Id}}", image+"-alias"); got != imageID {
				t.Fatalf("alias changed: %s", got)
			}
			// An administrator explicitly resolves the tag conflict; retry
			// must still find the image although all containers have gone.
			docker("image", "rm", image+"-alias")
			uninstall()
			assertClean(true)
		})
	}
}

type cleanupRecordingRunner struct {
	CLIRunner
	afterUp func() error
	fail    bool
}

func (r *cleanupRecordingRunner) Run(ctx context.Context, args []string, output io.Writer) error {
	if err := r.CLIRunner.Run(ctx, args, output); err != nil {
		return err
	}
	for _, arg := range args {
		if arg == "up" {
			if err := r.afterUp(); err != nil {
				return err
			}
			if r.fail {
				return errors.New("injected failure after actual container creation")
			}
			break
		}
	}
	return nil
}

func cleanupDocker(args ...string) (string, error) {
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker %v: %w: %s", args, err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// The local test daemon uses Docker's built-in pools. Permit its allocation
// in this fixture without changing the shared daemon's configuration; actual
// network creation, observation and removal still use Docker and Compose.
type cleanupIntegrationDocker struct{ DockerInspector }

func (cleanupIntegrationDocker) AddressPools(context.Context) ([]netip.Prefix, error) {
	return []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}, nil
}
