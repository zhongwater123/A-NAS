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
	"testing/synctest"
	"time"

	"github.com/zhongwater123/A-NAS/internal/appstore"
	"github.com/zhongwater123/A-NAS/internal/appstore/engine"
)

// rateLimited is how GHCR refused one request of an Immich install on the
// Experimental NAS on 2026-10-28, as Docker's graph-driver store prints it.
const rateLimited = "toomanyrequests: retry-after: 285.983µs, allowed: 44000/minute"

// manifestUnknown is a tag the registry does not have.
const manifestUnknown = "Error response from daemon: manifest for ghcr.io/example/app:2 not found: manifest unknown: manifest unknown"

var memosRunning = []engine.ContainerInfo{{Name: "memos", AppID: "memos", Running: true}}

var appPool = []netip.Prefix{netip.MustParsePrefix("10.96.64.0/19")}

// failPulls makes the first n pulls end with line and fail, as Compose does
// when a registry refuses; other commands succeed. The runner writes nothing
// else, so the job's output keeps every line about retries. It counts the
// pulls of an app of one service, which run one after another.
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

// failServices makes the pulls of the named services end with line and
// fail every time.
func failServices(runner *fakeRunner, line string, services ...string) {
	pulls := func(args []string) bool {
		return slices.Contains(args, "pull") && slices.Contains(services, args[len(args)-1])
	}
	runner.quiet = true
	runner.say = func(args []string) string {
		if pulls(args) {
			return line
		}
		return ""
	}
	runner.fail = func(args []string) error {
		if pulls(args) {
			return errors.New("compose command failed: exit status 1")
		}
		return nil
	}
}

// installMemos installs Memos, which has one service, and waits for the job.
func installMemos(t *testing.T, store *engine.Store) (appstore.Plan, appstore.AppStatus) {
	t.Helper()
	return installApp(t, store, "memos")
}

func installApp(t *testing.T, store *engine.Store, id string) (appstore.Plan, appstore.AppStatus) {
	t.Helper()
	ctx := context.Background()
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
	return plan, find(apps, id)
}

func outputHas(job *appstore.Job, text string) bool {
	return job != nil && slices.ContainsFunc(job.Output, func(line string) bool { return strings.Contains(line, text) })
}

// A registry that refuses for a while, in the words of either image store,
// as a line of Compose's JSON progress or as plain text, is waited for and
// asked again; the install goes on once it answers.
func TestInstallRetriesARegistryThatFailsForAWhile(t *testing.T) {
	const (
		limited     = "镜像仓库限流"
		server      = "镜像仓库暂时出错"
		network     = "镜像仓库连接中断或超时"
		ghcr        = "https://ghcr.io/v2/immich-app/immich-server/manifests/v2.7.2"
		unavailable = `Error response from daemon: unknown: failed to resolve reference "ghcr.io/immich-app/immich-server:v2.7.2": unexpected status from HEAD request to ` + ghcr
	)
	for name, test := range map[string]struct{ line, retry string }{
		"rate limit, graph-driver store":          {rateLimited, limited},
		"rate limit, containerd store":            {unavailable + ": 429 Too Many Requests", limited},
		"rate limit on a layer, containerd store": {"error from registry: retry-after: 285.983µs, allowed: 44000/minute", limited},
		"rate limit as a Compose error event":     {`{"id":"memos","text":"Error","status":"toomanyrequests: retry-after: 285.983µs, allowed: 44000/minute"}`, limited},
		"server error, containerd store":          {unavailable + ": 503 Service Unavailable", server},
		"server error without a reason phrase":    {unavailable + ": 520 ", server},
		"server error in Compose's closing event": {`{"error":true,"message":"` + strings.ReplaceAll(unavailable, `"`, `\"`) + `: 502 Bad Gateway"}`, server},
		"server error, graph-driver store":        {`Error response from daemon: error parsing HTTP 502 response body: invalid character '<' looking for beginning of value: "<html></html>"`, server},
		"server error code, graph-driver store":   {"Error response from daemon: unavailable: the registry is down for maintenance", server},
		"connection reset":                        {`Error response from daemon: Get "https://ghcr.io/v2/": read tcp 10.0.0.5:41234->140.82.112.34:443: read: connection reset by peer`, network},
		"connect timed out":                       {`Error response from daemon: Get "https://ghcr.io/v2/": dial tcp 140.82.112.34:443: connect: connection timed out`, network},
		"broken pipe":                             {`failed to copy: httpReadSeeker: failed open: write tcp 10.0.0.5:41234->140.82.112.34:443: write: broken pipe`, network},
		"client timeout":                          {`Error response from daemon: Get "https://ghcr.io/v2/": net/http: request canceled while waiting for connection (Client.Timeout exceeded while awaiting headers)`, network},
		"TLS handshake timeout":                   {`Error response from daemon: Get "https://ghcr.io/v2/": net/http: TLS handshake timeout`, network},
		"the daemon's deadline":                   {`Error response from daemon: Head "` + ghcr + `": context deadline exceeded`, network},
		"HTTP/2 stream reset":                     {`failed to copy: httpReadSeeker: failed open: stream error: stream ID 7; INTERNAL_ERROR; received from peer`, network},
		"HTTP/2 GOAWAY":                           {`Error response from daemon: Get "` + ghcr + `": http2: server sent GOAWAY and closed the connection; LastStreamID=1999, ErrCode=NO_ERROR`, network},
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
// doubles, and never shorter than a registry asks either, even beyond the
// backoff's limit.
func TestPullWaitsAtLeastTheBackoffAndWhatTheRegistryAsks(t *testing.T) {
	gaps := func(t *testing.T, line string, failures int, first, limit time.Duration) []time.Duration {
		t.Helper()
		docker := &fakeDocker{}
		runner := &fakeRunner{after: composeOn(docker, memosRunning)}
		failPulls(runner, failures, line)
		store, _ := newStore(t, docker, runner)
		store.SetPullBackoff(first, limit)
		if _, status := installMemos(t, store); status.Job.State != appstore.JobSucceeded {
			t.Fatalf("job = %+v", status.Job)
		}
		var gaps []time.Duration
		for i := 1; i <= failures; i++ {
			gaps = append(gaps, runner.at[i].Sub(runner.at[i-1]))
		}
		return gaps
	}
	if got := gaps(t, rateLimited, 2, 60*time.Millisecond, time.Minute); got[0] < 60*time.Millisecond || got[1] < 120*time.Millisecond {
		t.Errorf("waits after GHCR's 285.983µs = %v, want at least 60ms and then 120ms", got)
	}
	if got := gaps(t, "toomanyrequests: retry-after: 300ms, allowed: 44000/minute", 1, time.Millisecond, 10*time.Millisecond); got[0] < 300*time.Millisecond {
		t.Errorf("wait after a retry-after of 300ms, beyond the 10ms limit = %v", got[0])
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
	failure := "镜像下载失败：" + service + " 遇到镜像仓库限流，重试 4 次后仍未成功（" + rateLimited + "）"
	if want := failure + "；没有创建容器，已下载完成的镜像已保留，重新安装时不会重复下载"; status.Job.State != appstore.JobFailed || status.Job.Error != want {
		t.Fatalf("job error = %q, want %q", status.Job.Error, want)
	}
	if !outputHas(status.Job, "（第 4 次）") || outputHas(status.Job, "（第 5 次）") || !outputHas(status.Job, failure) {
		t.Fatalf("output = %q, want four retries announced and the failure", status.Job.Output)
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
// not know; such a pull fails the install at once and says why, even when a
// retryable failure is in the same output.
func TestPullDoesNotRetryWhatAWaitCannotFix(t *testing.T) {
	for name, test := range map[string]struct{ line, reason string }{
		"manifest unknown, graph-driver store": {manifestUnknown, manifestUnknown},
		"not found, containerd store": {`Error response from daemon: failed to resolve reference "ghcr.io/example/app:2": ghcr.io/example/app:2: not found`,
			`Error response from daemon: failed to resolve reference "ghcr.io/example/app:2": ghcr.io/example/app:2: not found`},
		"not found as a Compose error event": {`{"id":"memos","text":"Error","status":"failed to resolve reference \"ghcr.io/example/app:2\": not found"}`,
			`failed to resolve reference "ghcr.io/example/app:2": not found`},
		"unauthorized": {`Error response from daemon: Head "https://ghcr.io/v2/example/app/manifests/2": unauthorized`,
			`Error response from daemon: Head "https://ghcr.io/v2/example/app/manifests/2": unauthorized`},
		"access denied": {"Error response from daemon: pull access denied for example/private, repository does not exist or may require 'docker login': denied: requested access to the resource is denied",
			"Error response from daemon: pull access denied for example/private, repository does not exist or may require 'docker login': denied: requested access to the resource is denied"},
		"forbidden, containerd store": {"unexpected status from HEAD request to https://ghcr.io/v2/example/private/manifests/1: 403 Forbidden",
			"unexpected status from HEAD request to https://ghcr.io/v2/example/private/manifests/1: 403 Forbidden"},
		"digest mismatch, containerd store": {`failed commit on ref "layer-sha256:d8a8": commit failed: unexpected commit digest sha256:8af1, expected sha256:d8a8: failed precondition`,
			`failed commit on ref "layer-sha256:d8a8": commit failed: unexpected commit digest sha256:8af1, expected sha256:d8a8: failed precondition`},
		"digest mismatch, graph-driver store":  {"filesystem layer verification failed for digest sha256:d8a8", "filesystem layer verification failed for digest sha256:d8a8"},
		"an unknown error":                     {"Error response from daemon: no space left on device", "Error response from daemon: no space left on device"},
		"a missing image after a rate limit":   {rateLimited + "\n" + manifestUnknown, manifestUnknown},
		"a rate limit after the missing image": {manifestUnknown + "\n" + rateLimited, manifestUnknown},
		"the missing image in a closing event": {`{"error":true,"message":"` + strings.ReplaceAll(manifestUnknown, `"`, `\"`) + `"}` + "\n" + rateLimited, manifestUnknown},
		"a 404 from a registry on port 502": {"Error response from daemon: unknown: unexpected status from HEAD request to https://registry.example:502/v2/app/manifests/1: 404 Not Found",
			"Error response from daemon: unknown: unexpected status from HEAD request to https://registry.example:502/v2/app/manifests/1: 404 Not Found"},
		"connection refused, not retried by us": {`Error response from daemon: Get "https://ghcr.io/v2/": dial tcp 140.82.112.34:443: connect: connection refused`, `Error response from daemon: Get "https://ghcr.io/v2/": dial tcp 140.82.112.34:443: connect: connection refused`},
	} {
		t.Run(name, func(t *testing.T) {
			docker := &fakeDocker{}
			runner := &fakeRunner{after: composeOn(docker, memosRunning)}
			failPulls(runner, 100, test.line)
			store, _ := newStore(t, docker, runner)
			store.SetPullBackoff(time.Millisecond, time.Second)
			plan, status := installMemos(t, store)

			if verbs := runner.verbs(); !slices.Equal(verbs, []string{"pull"}) || outputHas(status.Job, "后重试") {
				t.Fatalf("compose ran %v, output %q; want one pull and no retry", verbs, status.Job.Output)
			}
			want := fmt.Sprintf("镜像下载失败：%s（%s）；没有创建容器", plan.Services[0].Service, test.reason)
			if status.Job.State != appstore.JobFailed || !strings.HasPrefix(status.Job.Error, want) {
				t.Fatalf("job error = %q, want it to start %q", status.Job.Error, want)
			}
		})
	}
}

// A wait that would end after the install's 30 minutes are up, whether the
// registry or the backoff asks for it, ends the install at once and says
// how long it would have been.
func TestPullDoesNotWaitPastTheInstallsDeadline(t *testing.T) {
	for name, test := range map[string]struct {
		line         string
		first, limit time.Duration
		want         string
	}{
		"the registry asks for too long": {"toomanyrequests: retry-after: 40m0s, allowed: 44000/minute", time.Millisecond, time.Second, "需要等待 40 分钟再试"},
		"the backoff is too long":        {rateLimited, time.Hour, 2 * time.Hour, "需要等待 60 分钟再试"},
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
			if want := "遇到镜像仓库限流，" + test.want + "，超过了安装剩余的时间"; !strings.Contains(status.Job.Error, want) {
				t.Fatalf("job error = %q, want %q", status.Job.Error, want)
			}
		})
	}
}

// A pull still running when the install's 30 minutes are up ends the
// install and says so; nothing was created, so nothing is rolled back.
func TestAnInstallThatRunsOutOfTimeWhilePulling(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runner := &fakeRunner{
			quiet: true,
			hold:  func(args []string) bool { return slices.Contains(args, "pull") },
			held:  make(chan struct{}, 1),
		}
		store, _ := newStore(t, &fakeDocker{}, runner)
		_, status := installMemos(t, store)

		want := "镜像下载失败：超过了 30 分钟的安装时限；没有创建容器，已下载完成的镜像已保留，重新安装时不会重复下载"
		if status.Job.State != appstore.JobFailed || status.Job.Error != want {
			t.Fatalf("job error = %q, want %q", status.Job.Error, want)
		}
		if verbs := runner.verbs(); !slices.Equal(verbs, []string{"pull"}) {
			t.Fatalf("compose ran %v, want only the pull", verbs)
		}
	})
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

// Each service pulls in a Compose process of its own, three at a time, so
// an app of several images still downloads them side by side.
func TestPullsRunAFewAtATime(t *testing.T) {
	docker := &fakeDocker{pools: appPool}
	runner := &fakeRunner{quiet: true, after: func(args []string) {
		if slices.Contains(args, "pull") {
			time.Sleep(50 * time.Millisecond)
		}
	}}
	store, _ := newStore(t, docker, runner)
	plan, status := installApp(t, store, "immich")

	if len(plan.Services) < 4 {
		t.Fatalf("immich has %d services, want at least four", len(plan.Services))
	}
	if status.Job.State != appstore.JobSucceeded || !slices.Equal(runner.verbs(), pullsThen(plan, "up")) {
		t.Fatalf("job = %+v, compose ran %v", status.Job, runner.verbs())
	}
	if runner.most != 3 {
		t.Fatalf("%d Compose commands ran at once, want three pulls", runner.most)
	}
}

// A service that fails does not stop the others: they finish downloading,
// and none of the images is removed. The install names the first failure
// and that others failed too; the output says why each one did.
func TestAFailedPullLetsTheOtherServicesFinish(t *testing.T) {
	docker := &fakeDocker{pools: appPool}
	runner := &fakeRunner{}
	store, state := newStore(t, docker, runner)
	plan := linkwardenPlan(t, store)
	if len(plan.Services) != 3 {
		t.Fatalf("linkwarden has %d services, want three", len(plan.Services))
	}
	first, second := plan.Services[1].Service, plan.Services[2].Service
	failServices(runner, manifestUnknown, first, second)
	_, status := installApp(t, store, "linkwarden")

	var pulled []string
	for _, call := range runner.calls {
		pulled = append(pulled, call[len(call)-1])
	}
	slices.Sort(pulled)
	services := []string{plan.Services[0].Service, first, second}
	slices.Sort(services)
	if !slices.Equal(runner.verbs(), []string{"pull", "pull", "pull"}) || !slices.Equal(pulled, services) {
		t.Fatalf("compose ran %v, want every service pulled and nothing after", runner.calls)
	}
	want := "镜像下载失败：" + first + "（" + manifestUnknown + "）；另有 1 个服务的镜像也没有下载成功，原因见输出；没有创建容器"
	if status.Job.State != appstore.JobFailed || !strings.HasPrefix(status.Job.Error, want) {
		t.Fatalf("job error = %q, want it to start %q", status.Job.Error, want)
	}
	if !outputHas(status.Job, "镜像下载失败："+second+"（") {
		t.Fatalf("output = %q, want the second failure", status.Job.Output)
	}
	if status.State != appstore.StateAvailable || docker.cleanedImages != nil {
		t.Fatalf("status %s, images removed %v; want available with nothing removed", status.State, docker.cleanedImages)
	}
	if _, err := os.Stat(filepath.Join(state, "apps", "linkwarden", "images.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleanup record: %v", err)
	}
}
