package engine_test

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/appstore"
	"github.com/zhongwater123/A-NAS/internal/appstore/engine"
)

// rateLimited is how GHCR refused one request of an Immich install on the
// Experimental NAS on 2026-10-28, as Docker's graph-driver store prints it.
const rateLimited = "toomanyrequests: retry-after: 285.983µs, allowed: 44000/minute"

var memosRunning = []engine.ContainerInfo{{Name: "memos", AppID: "memos", Running: true}}

// failPulls makes the first n pulls end with line and fail, as Compose does
// when a registry refuses; other commands succeed. The runner writes nothing
// else, so the job's output keeps every line about retries.
func failPulls(runner *fakeRunner, n int, line string) {
	failed := 0
	runner.quiet = true
	runner.say = func(args []string) string {
		if slices.Contains(args, "pull") && failed < n {
			return line
		}
		return ""
	}
	runner.fail = func(args []string) error {
		if slices.Contains(args, "pull") && failed < n {
			failed++
			return errors.New("compose command failed: exit status 1")
		}
		return nil
	}
}

// installMemos installs Memos, which has one service, and waits for the job.
func installMemos(t *testing.T, store *engine.Store) (appstore.Plan, appstore.AppStatus) {
	t.Helper()
	ctx := context.Background()
	plan, err := store.Plan(ctx, "memos", identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Install(ctx, "memos", plan.Digest, identity); err != nil {
		t.Fatal(err)
	}
	store.Wait()
	apps, err := store.Apps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return plan, find(apps, "memos")
}

func outputHas(job *appstore.Job, text string) bool {
	return job != nil && slices.ContainsFunc(job.Output, func(line string) bool { return strings.Contains(line, text) })
}

// A registry that refuses for a while, in the words of either image store,
// is waited for and asked again; the install goes on once it answers.
func TestInstallRetriesARegistryThatFailsForAWhile(t *testing.T) {
	for name, test := range map[string]struct{ line, retry string }{
		"rate limit, graph-driver store": {rateLimited, "镜像仓库限流"},
		"rate limit, containerd store": {`Error response from daemon: unknown: failed to resolve reference "ghcr.io/immich-app/immich-server:v2.7.2": ` +
			`unexpected status from HEAD request to https://ghcr.io/v2/immich-app/immich-server/manifests/v2.7.2: 429 Too Many Requests`, "镜像仓库限流"},
		"rate limit on a layer, containerd store": {"error from registry: retry-after: 285.983µs, allowed: 44000/minute", "镜像仓库限流"},
		"server error, containerd store": {`Error response from daemon: unknown: failed to resolve reference "ghcr.io/immich-app/immich-server:v2.7.2": ` +
			`unexpected status from HEAD request to https://ghcr.io/v2/immich-app/immich-server/manifests/v2.7.2: 503 Service Unavailable`, "镜像仓库暂时出错"},
		"server error, graph-driver store": {`Error response from daemon: error parsing HTTP 502 response body: invalid character '<' looking for beginning of value: "<html><body>Bad Gateway</body></html>"`, "镜像仓库暂时出错"},
		"connection reset":                 {`Error response from daemon: Get "https://ghcr.io/v2/": read tcp 10.0.0.5:41234->140.82.112.34:443: read: connection reset by peer`, "镜像仓库连接中断或超时"},
		"timeout":                          {`Error response from daemon: Get "https://ghcr.io/v2/": net/http: request canceled while waiting for connection (Client.Timeout exceeded while awaiting headers)`, "镜像仓库连接中断或超时"},
		"TLS handshake timeout":            {`Error response from daemon: Get "https://ghcr.io/v2/": net/http: TLS handshake timeout`, "镜像仓库连接中断或超时"},
	} {
		t.Run(name, func(t *testing.T) {
			docker := &fakeDocker{}
			runner := &fakeRunner{after: composeOn(docker, memosRunning)}
			failPulls(runner, 1, test.line)
			store, _ := newStore(t, docker, runner)
			store.SetPullBackoff(time.Millisecond, time.Second)
			plan, status := installMemos(t, store)

			if status.State != appstore.StateInstalled || status.Job.State != appstore.JobSucceeded {
				t.Fatalf("status = %s, job = %+v, want installed", status.State, status.Job)
			}
			if verbs := runner.verbs(); !slices.Equal(verbs, []string{"pull", "pull", "up"}) {
				t.Fatalf("compose ran %v, want a pull, its retry and up", verbs)
			}
			if want := plan.Services[0].Service + "：" + test.retry + "，1 秒后重试（第 1 次）"; !outputHas(status.Job, want) {
				t.Fatalf("output = %q, want %q", status.Job.Output, want)
			}
		})
	}
}

// GHCR asks for less than a millisecond, which would only repeat the burst
// that hit its limit: the wait is never shorter than the backoff, which
// doubles, and never shorter than a registry asks either.
func TestPullWaitsAtLeastTheBackoffAndWhatTheRegistryAsks(t *testing.T) {
	gaps := func(t *testing.T, line string, failures int, first time.Duration) []time.Duration {
		t.Helper()
		docker := &fakeDocker{}
		runner := &fakeRunner{after: composeOn(docker, memosRunning)}
		failPulls(runner, failures, line)
		store, _ := newStore(t, docker, runner)
		store.SetPullBackoff(first, time.Minute)
		if _, status := installMemos(t, store); status.Job.State != appstore.JobSucceeded {
			t.Fatalf("job = %+v", status.Job)
		}
		var gaps []time.Duration
		for i := 1; i <= failures; i++ {
			gaps = append(gaps, runner.at[i].Sub(runner.at[i-1]))
		}
		return gaps
	}
	if got := gaps(t, rateLimited, 2, 60*time.Millisecond); got[0] < 60*time.Millisecond || got[1] < 120*time.Millisecond {
		t.Errorf("waits after GHCR's 285.983µs = %v, want at least 60ms and then 120ms", got)
	}
	if got := gaps(t, "toomanyrequests: retry-after: 300ms, allowed: 44000/minute", 1, time.Millisecond); got[0] < 300*time.Millisecond {
		t.Errorf("wait after a retry-after of 300ms = %v", got[0])
	}
}

// A registry that keeps refusing ends the install after five pulls. No
// container was created, so nothing is rolled back and no image is removed:
// the images that finished downloading are there for the next install.
func TestPullGivesUpOnARegistryThatKeepsRefusing(t *testing.T) {
	docker := &fakeDocker{}
	runner := &fakeRunner{after: composeOn(docker, memosRunning)}
	failPulls(runner, 100, rateLimited)
	store, state := newStore(t, docker, runner)
	store.SetPullBackoff(time.Millisecond, time.Millisecond)
	plan, status := installMemos(t, store)

	if verbs := runner.verbs(); !slices.Equal(verbs, slices.Repeat([]string{"pull"}, 5)) {
		t.Fatalf("compose ran %v, want five pulls and nothing else", verbs)
	}
	service := plan.Services[0].Service
	want := "镜像下载失败：" + service + " 遇到镜像仓库限流，重试 4 次后仍未成功（" + rateLimited + "）；没有创建容器，已下载完成的镜像已保留，重新安装时不会重复下载"
	if status.Job.State != appstore.JobFailed || status.Job.Error != want {
		t.Fatalf("job error = %q, want %q", status.Job.Error, want)
	}
	if !outputHas(status.Job, "（第 4 次）") || outputHas(status.Job, "（第 5 次）") {
		t.Fatalf("output = %q, want four retries announced", status.Job.Output)
	}
	if status.State != appstore.StateAvailable || status.Total != 0 || docker.cleanedImages != nil {
		t.Fatalf("status = %s with %d containers, images removed %v; want available, nothing removed", status.State, status.Total, docker.cleanedImages)
	}
	if _, err := os.Stat(filepath.Join(state, "apps", "memos", "images.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a pull that created nothing left a cleanup record: %v", err)
	}
}

// Waiting cannot fix an image that does not exist, a registry that refuses
// this client, a download that is not the pinned image, or a failure it does
// not know; such a pull fails the install at once.
func TestPullDoesNotRetryWhatAWaitCannotFix(t *testing.T) {
	for name, line := range map[string]string{
		"manifest unknown, graph-driver store": "Error response from daemon: manifest for ghcr.io/immich-app/immich-server:v2.7.9 not found: manifest unknown: manifest unknown",
		"not found, containerd store": `Error response from daemon: failed to resolve reference "ghcr.io/immich-app/immich-server:v2.7.9": ` +
			"ghcr.io/immich-app/immich-server:v2.7.9: not found",
		"unauthorized": `Error response from daemon: Head "https://ghcr.io/v2/immich-app/immich-server/manifests/v2.7.2": unauthorized`,
		"access denied": "Error response from daemon: pull access denied for example/private, repository does not exist or may require 'docker login': " +
			"denied: requested access to the resource is denied",
		"forbidden, containerd store": `Error response from daemon: unknown: failed to resolve reference "ghcr.io/example/private:1": ` +
			"unexpected status from HEAD request to https://ghcr.io/v2/example/private/manifests/1: 403 Forbidden",
		"digest mismatch, containerd store": `failed commit on ref "layer-sha256:d8a841d5db21": commit failed: unexpected commit digest sha256:8af10c804deb, ` +
			"expected sha256:d8a841d5db21: failed precondition",
		"digest mismatch, graph-driver store": "filesystem layer verification failed for digest sha256:d8a841d5db21",
		"an unknown error":                    "Error response from daemon: write /var/lib/docker/tmp/GetImageBlob123: no space left on device",
		"a missing image beside a rate limit": rateLimited + "\nError response from daemon: manifest for ghcr.io/example/app:2 not found: manifest unknown: manifest unknown",
	} {
		t.Run(name, func(t *testing.T) {
			docker := &fakeDocker{}
			runner := &fakeRunner{after: composeOn(docker, memosRunning)}
			failPulls(runner, 100, line)
			store, _ := newStore(t, docker, runner)
			store.SetPullBackoff(time.Millisecond, time.Second)
			plan, status := installMemos(t, store)

			if verbs := runner.verbs(); !slices.Equal(verbs, []string{"pull"}) || outputHas(status.Job, "后重试") {
				t.Fatalf("compose ran %v, output %q; want one pull and no retry", verbs, status.Job.Output)
			}
			lines := strings.Split(line, "\n")
			want := fmt.Sprintf("镜像下载失败：%s（%s）；没有创建容器", plan.Services[0].Service, lines[len(lines)-1])
			if status.Job.State != appstore.JobFailed || !strings.HasPrefix(status.Job.Error, want) {
				t.Fatalf("job error = %q, want it to start %q", status.Job.Error, want)
			}
		})
	}
}

// A wait longer than any wait may be, or one that would end after the
// install's 30 minutes are up, ends the install at once and says how long
// it would have been.
func TestPullDoesNotWaitLongerThanItMay(t *testing.T) {
	for name, test := range map[string]struct {
		line         string
		first, limit time.Duration
		want         string
	}{
		"the registry asks for too long": {"toomanyrequests: retry-after: 20m0s, allowed: 44000/minute", time.Millisecond, time.Second, "需要等待 20 分钟再试"},
		"past the install's deadline":    {rateLimited, time.Hour, 2 * time.Hour, "需要等待 60 分钟再试"},
	} {
		t.Run(name, func(t *testing.T) {
			docker := &fakeDocker{}
			runner := &fakeRunner{}
			failPulls(runner, 100, test.line)
			store, _ := newStore(t, docker, runner)
			store.SetPullBackoff(test.first, test.limit)
			_, status := installMemos(t, store)

			if verbs := runner.verbs(); !slices.Equal(verbs, []string{"pull"}) {
				t.Fatalf("compose ran %v, want one pull", verbs)
			}
			if want := "遇到镜像仓库限流，" + test.want + "，超过了安装能等待的时间"; !strings.Contains(status.Job.Error, want) {
				t.Fatalf("job error = %q, want %q", status.Job.Error, want)
			}
		})
	}
}

// Stopping the agent while a pull waits to be retried ends the wait and the
// install at once; nothing was created, so nothing is rolled back.
func TestStoppingTheAgentEndsAPullBackoff(t *testing.T) {
	ctx := context.Background()
	docker := &fakeDocker{}
	runner := &fakeRunner{}
	failPulls(runner, 100, rateLimited)
	store, _ := newStore(t, docker, runner)
	store.SetPullBackoff(10*time.Minute, 10*time.Minute)
	plan, err := store.Plan(ctx, "memos", identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Install(ctx, "memos", plan.Digest, identity); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		apps, err := store.Apps(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if status := find(apps, "memos"); outputHas(status.Job, "10 分钟后重试（第 1 次）") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the install did not start waiting to retry")
		}
	}
	stopped := make(chan struct{})
	go func() {
		store.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop waited for the backoff")
	}

	apps, _ := store.Apps(ctx)
	job := find(apps, "memos").Job
	if verbs := runner.verbs(); !slices.Equal(verbs, []string{"pull"}) {
		t.Fatalf("compose ran %v, want only the pull", verbs)
	}
	if job.State != appstore.JobFailed || job.Error != "容器代理停止，安装被中断，还没有创建容器，请重新安装" {
		t.Fatalf("job = %+v", job)
	}
}

// Services are pulled one at a time, so a pull that fails leaves the images
// of the services before it downloaded; none of them is removed.
func TestAFailedPullKeepsTheImagesPulledBeforeIt(t *testing.T) {
	docker := &fakeDocker{pools: []netip.Prefix{netip.MustParsePrefix("10.96.64.0/19")}}
	runner := &fakeRunner{quiet: true}
	pulls := 0
	runner.say = func(args []string) string {
		if slices.Contains(args, "pull") && pulls == 1 {
			return "Error response from daemon: manifest for example/app:2 not found: manifest unknown: manifest unknown"
		}
		return ""
	}
	runner.fail = func(args []string) error {
		if !slices.Contains(args, "pull") {
			return nil
		}
		if pulls++; pulls == 2 {
			return errors.New("compose command failed: exit status 1")
		}
		return nil
	}
	store, state := newStore(t, docker, runner)
	ctx := context.Background()
	plan := linkwardenPlan(t, store)
	if len(plan.Services) < 3 {
		t.Fatalf("linkwarden has %d services, want three", len(plan.Services))
	}
	if _, err := store.Install(ctx, "linkwarden", plan.Digest, identity); err != nil {
		t.Fatal(err)
	}
	store.Wait()

	pulled := func(i int) string { return runner.calls[i][len(runner.calls[i])-1] }
	if verbs := runner.verbs(); !slices.Equal(verbs, []string{"pull", "pull"}) || pulled(0) != plan.Services[0].Service || pulled(1) != plan.Services[1].Service {
		t.Fatalf("compose ran %v, want the first two services pulled in turn and nothing after the failure", runner.calls)
	}
	apps, _ := store.Apps(ctx)
	status := find(apps, "linkwarden")
	if status.Job.State != appstore.JobFailed || !strings.HasPrefix(status.Job.Error, "镜像下载失败："+plan.Services[1].Service+"（") {
		t.Fatalf("job = %+v", status.Job)
	}
	if status.State != appstore.StateAvailable || docker.cleanedImages != nil {
		t.Fatalf("status %s, images removed %v; want available with nothing removed", status.State, docker.cleanedImages)
	}
	if _, err := os.Stat(filepath.Join(state, "apps", "linkwarden", "images.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleanup record: %v", err)
	}
}
