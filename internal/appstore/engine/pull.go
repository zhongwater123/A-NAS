package engine

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// On 2026-10-28 GHCR answered one request of an Immich install with 429 and
// the install failed. up pulled the images and gave up at once (Docker's
// containerd store retries for about a second, the graph-driver store not
// at all), and Compose interrupts a project's other pulls when one fails;
// the interrupted image is not kept. An install therefore pulls before up,
// each service in a Compose process of its own, and retries what a registry
// or the network fails at only for a while (investigation
// 2026-10-28-registry-rate-limit-failed-install).
const (
	pullAttempts = 5
	// pullParallel services pull at once. Docker's graph-driver store
	// downloads at most three layers at a time anyway; more would only send
	// a rate-limited registry more requests at once.
	pullParallel     = 3
	defaultPullDelay = 10 * time.Second
	// defaultPullLimit bounds the doubling delay. A registry may ask for a
	// longer wait, which is kept while the install has the time.
	defaultPullLimit = 2 * time.Minute
	// reasonRunes bounds the registry's words quoted in a job's error.
	reasonRunes = 240
)

// errPull starts the error of an install whose images did not download.
var errPull = errors.New("镜像下载失败")

// SetPullBackoff changes how long a failed pull waits before its first retry
// and how long the doubling may grow; tests shorten them.
func (s *Store) SetPullBackoff(first, limit time.Duration) {
	s.pullDelay, s.pullLimit = first, limit
}

// pullArgs pulls one service unless its image is present, with Compose's
// JSON progress (Compose 2.29 and later), whose events say which lines are
// errors.
func pullArgs(project, file, service string) []string {
	return []string{"compose", "--progress", "json", "-p", project, "-f", file, "pull", "--policy", "missing", service}
}

// pullImages downloads the images of the services that are not present yet,
// pullParallel at a time. A failed service does not stop the others: what
// finishes downloading stays for the next install. A pull creates no
// container, so a failure leaves nothing to roll back and no download whose
// ownership is proven. The first failure in service order is returned.
func (s *Store) pullImages(ctx context.Context, project, file string, services []string, output io.Writer) error {
	failures := make([]error, len(services))
	// The container agent keeps to the standard library where it can
	// (depguard); a buffered channel bounds the pulls.
	slots := make(chan struct{}, pullParallel)
	var pulls sync.WaitGroup
	for i, service := range services {
		slots <- struct{}{}
		pulls.Go(func() {
			defer func() { <-slots }()
			failures[i] = s.pull(ctx, service, pullArgs(project, file, service), output)
		})
	}
	pulls.Wait()
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	var failed []error
	for _, failure := range failures {
		if failure != nil {
			failed = append(failed, failure)
		}
	}
	switch len(failed) {
	case 0:
		return nil
	case 1:
		return failed[0]
	}
	return fmt.Errorf("%w；另有 %d 个服务的镜像也没有下载成功，原因见输出", failed[0], len(failed)-1)
}

// pull runs one service's pull, retrying failures that a wait can fix with a
// delay that doubles from pullDelay up to pullLimit and is never shorter
// than the registry asked for. A wait that would end after the install's
// deadline ends the install instead.
func (s *Store) pull(ctx context.Context, service string, args []string, output io.Writer) error {
	delay := s.pullDelay
	for attempt := 1; ; attempt++ {
		watch := newPullWatch(output)
		err := s.runner.Run(ctx, args, watch)
		watch.Flush()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		problem := watch.problem
		reason := cmp.Or(problem.reason, err.Error())
		var failure error
		switch wait := max(delay, problem.retryAfter); {
		case problem.permanent || problem.retry == "":
			failure = fmt.Errorf("%w：%s（%s）", errPull, service, reason)
		case attempt == pullAttempts:
			failure = fmt.Errorf("%w：%s 遇到%s，重试 %d 次后仍未成功（%s）", errPull, service, problem.retry, attempt-1, reason)
		case wait > timeLeft(ctx):
			failure = fmt.Errorf("%w：%s 遇到%s，需要等待 %s再试，超过了安装剩余的时间（%s）", errPull, service, problem.retry, waitText(wait), reason)
		default:
			// The App Center shows this as the job's latest line while it waits.
			_, _ = fmt.Fprintf(output, "%s：%s，%s后重试（第 %d 次）\n", service, problem.retry, waitText(wait), attempt)
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return context.Cause(ctx)
			case <-timer.C:
			}
			delay = min(delay*2, s.pullLimit)
			continue
		}
		// Several services may fail; each says why in the output.
		_, _ = fmt.Fprintln(output, failure)
		return failure
	}
}

// timeLeft is how long the install may still take.
func timeLeft(ctx context.Context) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		return time.Until(deadline)
	}
	return installTimeout
}

// waitText words a wait in whole seconds, or minutes from ten minutes on,
// rounded up.
func waitText(wait time.Duration) string {
	seconds := int64(math.Ceil(wait.Seconds()))
	if seconds >= 600 {
		return fmt.Sprintf("%d 分钟", (seconds+59)/60)
	}
	return fmt.Sprintf("%d 秒", seconds)
}

// What Docker says a pull failed at. Each image store words a failure its
// own way: the graph-driver store prints the registry's error code
// ("toomanyrequests: retry-after: 285.983µs, …", "manifest unknown",
// "unavailable: …"), the containerd store the HTTP status ("429 Too Many
// Requests") or only the message ("error from registry: retry-after: …",
// "…: not found"). TestRealDockerPullFailures pins them against a real
// Docker.
var (
	// A retry cannot fix an image or tag that does not exist, a registry
	// that refuses this client, or a download that is not the pinned image.
	permanentPull = regexp.MustCompile(`(?i)manifest unknown|name unknown|: not found|no matching manifest|pull access denied|unauthorized|authentication required|no basic auth credentials|denied|403 forbidden|invalid reference format|digest mismatch|unexpected commit digest|verification failed|d(?:id|oes) not match`)
	rateLimit     = regexp.MustCompile(`(?i)toomanyrequests|too many requests|retry-after`)
	retryAfter    = regexp.MustCompile(`(?i)retry-after:?\s*((?:[0-9.]+[a-zµμ]*)+)`)
	// A 5xx by its reason phrase, by its number where Docker names the
	// status (after ": ", unlike a port in the URL before it), or by the
	// registry error code UNAVAILABLE.
	serverError = regexp.MustCompile(`(?i)internal server error|bad gateway|service unavailable|gateway time-?out|\bhttp 5\d\d\b|\bstatus(?: code)?:? 5\d\d\b|unexpected (?:http )?status.*:\s+5\d\d\b|(?:^|[\s":])unavailable: `)
	// A connection that was reset or dropped, or timed out, including the
	// HTTP/2 stream resets and GOAWAY of registries served over HTTP/2.
	networkError = regexp.MustCompile(`(?i)connection reset|broken pipe|connection timed out|i/o timeout|tls handshake timeout|timeout awaiting response headers|client\.timeout exceeded|context deadline exceeded|unexpected eof|http2: server sent goaway|http2: client connection lost|stream error: stream id \d+; (?:internal_error|refused_stream|enhance_your_calm)`)
)

// pullProblem is what went wrong in a pull, read from its errors.
type pullProblem struct {
	// retry words a failure that a wait may fix; empty for others.
	retry string
	// permanent wins over any retryable failure in the same output.
	permanent  bool
	retryAfter time.Duration
	// reason is the last error of the gravest kind seen: permanent before
	// retryable before any other.
	reason string
	rank   int
}

// read takes one message; failure says Compose reported it as an error.
func (p *pullProblem) read(message string, failure bool) {
	message = strings.TrimSpace(message)
	rank := 0
	switch {
	case permanentPull.MatchString(message):
		p.permanent, rank = true, 3
	case rateLimit.MatchString(message):
		p.retry, rank = "镜像仓库限流", 2
		if match := retryAfter.FindStringSubmatch(message); match != nil {
			p.retryAfter = max(p.retryAfter, parseRetryAfter(match[1]))
		}
	case serverError.MatchString(message):
		p.retry, rank = cmp.Or(p.retry, "镜像仓库暂时出错"), 2
	case networkError.MatchString(message):
		p.retry, rank = cmp.Or(p.retry, "镜像仓库连接中断或超时"), 2
	case failure || strings.Contains(strings.ToLower(message), "error"):
		rank = 1
	}
	if rank == 0 || rank < p.rank {
		return
	}
	p.rank, p.reason = rank, message
	if utf8.RuneCountInString(message) > reasonRunes {
		p.reason = string([]rune(message)[:reasonRunes]) + "…"
	}
}

// parseRetryAfter reads a Go duration, as GHCR prints it, or seconds, as
// the HTTP header carries it; zero when it is neither.
func parseRetryAfter(value string) time.Duration {
	if wait, err := time.ParseDuration(value); err == nil {
		return max(wait, 0)
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil && seconds > 0 && seconds < math.MaxInt64/float64(time.Second) {
		return time.Duration(seconds * float64(time.Second))
	}
	return 0
}

// composeEvent is a line of Compose's JSON progress. A service that failed
// reports {"text":"Error","status":…}; newer releases end a failed command
// with {"error":true,"message":…}, older ones print that as plain text.
type composeEvent struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	Status  string `json:"status"`
	Error   bool   `json:"error"`
	Message string `json:"message"`
}

// pullWatch passes a pull's progress on as lines, each progress line once,
// and reads its errors.
type pullWatch struct {
	lineWriter
	output io.Writer
	// shown is the last line passed on for each layer or service.
	shown   map[string]string
	problem pullProblem
}

func newPullWatch(output io.Writer) *pullWatch {
	w := &pullWatch{output: output, shown: map[string]string{}}
	w.each = w.line
	return w
}

func (w *pullWatch) line(line string) {
	var event composeEvent
	if !strings.HasPrefix(line, "{") || json.Unmarshal([]byte(line), &event) != nil {
		w.problem.read(line, false)
		w.show("", line)
		return
	}
	switch {
	case event.Error:
		w.problem.read(event.Message, true)
		w.show("", event.Message)
	case strings.EqualFold(event.Text, "error"):
		w.problem.read(event.Status, true)
		w.show(event.ID, event.ID+" Error "+event.Status)
	default:
		w.show(event.ID, strings.Join([]string{event.ID, event.Text, event.Status}, " "))
	}
}

// show passes a line on whole, so that the lines of pulls running at once
// do not mix, unless it repeats the last line of the same id.
func (w *pullWatch) show(id, line string) {
	line = strings.Join(strings.Fields(line), " ")
	if line == "" || (id != "" && w.shown[id] == line) {
		return
	}
	if id != "" {
		w.shown[id] = line
	}
	_, _ = io.WriteString(w.output, line+"\n")
}

// lineWriter passes each line written to it to each, without its line
// break; Flush passes on a last line that has none. A line longer than
// maxLine is passed on in pieces, so that output without line breaks
// cannot grow without bound.
type lineWriter struct {
	each    func(string)
	partial []byte
}

const maxLine = 1 << 20

func (w *lineWriter) Write(data []byte) (int, error) {
	written := len(data)
	for {
		end := bytes.IndexByte(data, '\n')
		if end < 0 {
			break
		}
		line := data[:end]
		if len(w.partial) > 0 {
			line = append(w.partial, line...)
		}
		w.each(string(bytes.TrimSuffix(line, []byte("\r"))))
		w.partial = w.partial[:0]
		data = data[end+1:]
	}
	w.partial = append(w.partial, data...)
	if len(w.partial) >= maxLine {
		w.Flush()
	}
	return written, nil
}

func (w *lineWriter) Flush() {
	if len(w.partial) > 0 {
		line := string(w.partial)
		w.partial = w.partial[:0]
		w.each(line)
	}
}
