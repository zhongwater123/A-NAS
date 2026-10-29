//go:build dockerintegration

package engine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/moby/client"
	"github.com/opencontainers/go-digest"

	"github.com/zhongwater123/A-NAS/internal/appstore"
)

// The tests below pull from a registry on 127.0.0.1, which Docker treats as
// insecure and reaches without verifying its certificate, so the Docker
// daemon has to run on this host. Each test names its images uniquely and
// removes them by name; nothing else of the daemon is touched.

// TestRealDockerPullFailures pins how a real Docker words each failure that
// pull.go reads: the graph-driver store and the containerd store word them
// differently, and a release may change either.
func TestRealDockerPullFailures(t *testing.T) {
	if os.Getenv("ANAS_DOCKER_INTEGRATION") != "1" {
		t.Skip("set ANAS_DOCKER_INTEGRATION=1")
	}
	registry := newTestRegistry(t)
	for _, test := range []struct {
		repository string
		// retry is the failure's retry word; empty for one that is not retried.
		retry string
	}{
		{"ratelimit", "镜像仓库限流"},
		{"layerratelimit", "镜像仓库限流"},
		{"unavailable", "镜像仓库暂时出错"},
		{"reset", "镜像仓库连接中断或超时"},
		{"layerreset", "镜像仓库连接中断或超时"},
		{"missing", ""},
		{"private", ""},
		{"corrupt", ""},
	} {
		t.Run(test.repository, func(t *testing.T) {
			project, file := registry.project(t, map[string]string{"app": test.repository})
			var output bytes.Buffer
			watch := newPullWatch(&output)
			err := CLIRunner{}.Run(context.Background(), pullArgs(project, file, "app"), watch)
			watch.Flush()
			t.Logf("Docker said:\n%s", output.String())
			if err == nil {
				t.Fatal("the pull succeeded")
			}
			retry := watch.problem.retry
			if watch.problem.permanent {
				retry = ""
			}
			if retry != test.retry {
				t.Errorf("read as retry %q permanent %v (reason %q), want retry %q", watch.problem.retry, watch.problem.permanent, watch.problem.reason, test.retry)
			}
		})
	}
}

// TestRealDockerRetriesARateLimitedPull has a registry answer 429 until the
// install says it will retry, as GHCR did on 2026-10-28; the retry pulls the
// image.
func TestRealDockerRetriesARateLimitedPull(t *testing.T) {
	if os.Getenv("ANAS_DOCKER_INTEGRATION") != "1" {
		t.Skip("set ANAS_DOCKER_INTEGRATION=1")
	}
	registry := newTestRegistry(t)
	registry.throttled.Store(true)
	project, file := registry.project(t, map[string]string{"app": "throttled"})
	store := &Store{runner: CLIRunner{}, pullDelay: 50 * time.Millisecond, pullLimit: time.Second}
	output := &retryWatch{registry: registry}
	if err := store.pullImages(context.Background(), project, file, []string{"app"}, output); err != nil {
		t.Fatalf("pull: %v\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "app：镜像仓库限流，1 秒后重试（第 1 次）") {
		t.Fatalf("output:\n%s", output.String())
	}
	if _, err := exec.Command("docker", "image", "inspect", registry.images["app"]).CombinedOutput(); err != nil {
		t.Fatalf("the retried image is not there: %v", err)
	}
}

// retryWatch lets the registry answer once the install announces a retry.
type retryWatch struct {
	bytes.Buffer
	registry *testRegistry
}

func (w *retryWatch) Write(data []byte) (int, error) {
	if bytes.Contains(data, []byte("后重试")) {
		w.registry.throttled.Store(false)
	}
	return w.Buffer.Write(data)
}

// TestRealDockerFailedPullKeepsWhatFinished installs an app whose second
// image does not exist: the install fails before any container exists, and
// the first image stays for the next install.
func TestRealDockerFailedPullKeepsWhatFinished(t *testing.T) {
	if os.Getenv("ANAS_DOCKER_INTEGRATION") != "1" {
		t.Skip("set ANAS_DOCKER_INTEGRATION=1")
	}
	ctx := context.Background()
	registry := newTestRegistry(t)
	id := fmt.Sprintf("pull-%d", time.Now().UnixNano())
	first, second := registry.image(t, "ok"), registry.image(t, "missing")
	entry := appstore.Entry{App: appstore.App{ID: id, Title: "Pull"}, Compose: []byte(`
services:
  first:
    image: ` + first + `
    network_mode: bridge
  second:
    image: ` + second + `
    network_mode: bridge
`)}
	engineClient, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engineClient.Close() })
	root := t.TempDir()
	policy := appstore.Policy{AppDataRoot: filepath.Join(root, "apps"), DataRoot: filepath.Join(root, "shared"), TZ: "UTC"}
	identity := appstore.Identity{Username: "app-" + id, UID: 30005, GID: 30005}
	store := New([]appstore.Entry{entry}, policy, filepath.Join(root, "state"), cleanupIntegrationDocker{DockerInspector{Client: engineClient}}, CLIRunner{})
	plan, err := store.Plan(ctx, id, identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Install(ctx, id, plan.Digest, identity); err != nil {
		t.Fatal(err)
	}
	store.Wait()

	apps, err := store.Apps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	job := apps[0].Job
	if job.State != appstore.JobFailed || !strings.HasPrefix(job.Error, "镜像下载失败：second（") || !strings.Contains(job.Error, "已下载完成的镜像已保留") {
		t.Fatalf("job = %+v", job)
	}
	if out, err := exec.Command("docker", "image", "inspect", first).CombinedOutput(); err != nil {
		t.Fatalf("the image that finished was removed: %v %s", err, out)
	}
	for _, kind := range []string{"container", "network"} {
		args := []string{kind, "ls", "-q", "--filter", "label=com.docker.compose.project=" + appstore.ProjectName(id)}
		if kind == "container" {
			args = slices.Insert(args, 2, "-a")
		}
		if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil || len(bytes.TrimSpace(out)) != 0 {
			t.Fatalf("a failed pull created a %s: %s %v", kind, out, err)
		}
	}
	if _, err := os.Stat(store.imagesPath(id)); !os.IsNotExist(err) {
		t.Fatalf("cleanup record: %v", err)
	}
}

// testRegistry serves one small image under every repository over TLS and
// fails in the way the repository's name says: ratelimit, unavailable,
// missing, private and reset fail the manifest; layerratelimit, layerreset
// and corrupt the layer; throttled answers 429 while throttled is set.
//
// It is written here rather than taken from go-containerregistry, whose
// in-memory registry would still need a handler around it for each of these
// failures (a TCP reset without TLS close_notify, a corrupted layer, a 429
// on HEAD) and would add a module tree to go.mod for this one test; the
// image is three fixed blobs.
type testRegistry struct {
	*httptest.Server
	manifest, config, layer []byte
	layerDigest             string
	throttled               atomic.Bool
	// images are the references each project's services pull.
	images map[string]string
}

func newTestRegistry(t *testing.T) *testRegistry {
	t.Helper()
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	// The layer is new to Docker in each test: the containerd store keeps a
	// partial download, even a corrupt one, and resumes it on the next pull
	// of the same layer.
	content := bytes.Repeat(fmt.Appendf(nil, "a-nas pull test %d\n", time.Now().UnixNano()), 64)
	if err := writer.WriteHeader(&tar.Header{Name: "hello.txt", Mode: 0o644, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	var layer bytes.Buffer
	compressor := gzip.NewWriter(&layer)
	if _, err := compressor.Write(archive.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := compressor.Close(); err != nil {
		t.Fatal(err)
	}
	r := &testRegistry{layer: layer.Bytes(), layerDigest: digest.FromBytes(layer.Bytes()).String(), images: map[string]string{}}
	var err error
	r.config, err = json.Marshal(map[string]any{
		"architecture": "amd64", "os": "linux", "config": map[string]any{"Cmd": []string{"/hello"}},
		"rootfs": map[string]any{"type": "layers", "diff_ids": []string{digest.FromBytes(archive.Bytes()).String()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.manifest, err = json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": "application/vnd.docker.distribution.manifest.v2+json",
		"config": map[string]any{"mediaType": "application/vnd.docker.container.image.v1+json", "size": len(r.config), "digest": digest.FromBytes(r.config).String()},
		"layers": []map[string]any{{"mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip", "size": len(r.layer), "digest": r.layerDigest}},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.Server = httptest.NewTLSServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.Close)
	return r
}

// image names a new repository of the registry, removed from Docker when
// the test ends.
func (r *testRegistry) image(t *testing.T, mode string) string {
	t.Helper()
	image := fmt.Sprintf("%s/%s-%d:1", r.Listener.Addr(), mode, time.Now().UnixNano())
	t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", image).Run() })
	return image
}

// project writes a Compose file with a service pulling from each named
// repository and returns a new project name and the file.
func (r *testRegistry) project(t *testing.T, services map[string]string) (string, string) {
	t.Helper()
	var compose strings.Builder
	compose.WriteString("services:\n")
	for service, mode := range services {
		r.images[service] = r.image(t, mode)
		fmt.Fprintf(&compose, "  %s:\n    image: %s\n", service, r.images[service])
	}
	file := filepath.Join(t.TempDir(), "docker-compose.yml")
	if err := os.WriteFile(file, []byte(compose.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("a-nas-pulltest-%d", time.Now().UnixNano()), file
}

func (r *testRegistry) serve(w http.ResponseWriter, request *http.Request) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	if request.URL.Path == "/v2/" {
		return
	}
	// /v2/<repository>/manifests/<tag> or /v2/<repository>/blobs/<digest>
	parts := strings.Split(strings.TrimPrefix(request.URL.Path, "/v2/"), "/")
	if len(parts) != 3 {
		http.NotFound(w, request)
		return
	}
	mode, _, _ := strings.Cut(parts[0], "-")
	manifest, layer := parts[1] == "manifests", parts[1] == "blobs" && parts[2] == r.layerDigest
	failure := func(status int, code, message string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"code": code, "message": message}}})
	}
	switch {
	case manifest && (mode == "ratelimit" || mode == "throttled" && r.throttled.Load()), layer && mode == "layerratelimit":
		failure(http.StatusTooManyRequests, "TOOMANYREQUESTS", "retry-after: 285.983µs, allowed: 44000/minute")
	case manifest && mode == "unavailable":
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "<html><body>503 Service Unavailable</body></html>")
	case manifest && mode == "missing":
		failure(http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown")
	case manifest && mode == "private":
		w.Header().Set("WWW-Authenticate", `Basic realm="a-nas pull test"`)
		failure(http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
	case manifest && mode == "reset", layer && mode == "layerreset":
		reset(w)
	case manifest:
		r.send(w, "application/vnd.docker.distribution.manifest.v2+json", r.manifest)
	case layer && mode == "corrupt":
		corrupt := slices.Clone(r.layer)
		corrupt[len(corrupt)-1] ^= 0xff
		r.send(w, "application/octet-stream", corrupt)
	case layer:
		r.send(w, "application/octet-stream", r.layer)
	case parts[1] == "blobs" && parts[2] == digest.FromBytes(r.config).String():
		r.send(w, "application/octet-stream", r.config)
	default:
		http.NotFound(w, request)
	}
}

func (r *testRegistry) send(w http.ResponseWriter, kind string, body []byte) {
	w.Header().Set("Content-Type", kind)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Docker-Content-Digest", digest.FromBytes(body).String())
	_, _ = w.Write(body)
}

// reset drops the connection with a TCP reset and no TLS close_notify, as a
// NAT or proxy that gives up on it does.
func reset(w http.ResponseWriter) {
	conn, _, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	if secure, ok := conn.(*tls.Conn); ok {
		conn = secure.NetConn()
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetLinger(0)
	}
	_ = conn.Close()
}
