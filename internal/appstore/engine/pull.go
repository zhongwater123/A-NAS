package engine

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// On 2026-10-28 GHCR answered one request of an Immich install with 429 and
// the install failed: Docker does not retry a rate limit, and Compose's up
// pulls the missing images in parallel and cancels every pull when one
// fails, which drops the layers of the images still downloading. An install
// therefore pulls before up, one service at a time, and retries what a
// registry or the network fails at only for a while (investigation
// 2026-10-28-registry-rate-limit-failed-install).
const (
	pullAttempts     = 5
	defaultPullDelay = 10 * time.Second
	// defaultPullLimit bounds the doubling delay and how long a registry
	// may ask the install to wait.
	defaultPullLimit = 2 * time.Minute
	// reasonRunes bounds the registry's words quoted in a job's error.
	reasonRunes = 240
)

// errPull starts the error of an install whose images did not download.
var errPull = errors.New("镜像下载失败")

// SetPullBackoff changes how long a failed pull waits before its first retry
// and how long any wait may be; tests shorten them.
func (s *Store) SetPullBackoff(first, limit time.Duration) {
	s.pullDelay, s.pullLimit = first, limit
}

// pullImages downloads the images of the services that are not present yet.
// What finished downloading stays when a later service fails, so the next
// install starts from there; a failed pull created no container, so there is
// nothing to roll back and no download whose ownership is proven.
func (s *Store) pullImages(ctx context.Context, compose []string, services []string, output io.Writer) error {
	for _, service := range services {
		if err := s.pull(ctx, slices.Concat(compose, []string{"pull", "--policy", "missing", service}), service, output); err != nil {
			return err
		}
	}
	return nil
}

// pull runs one service's pull, retrying failures that a wait can fix with a
// delay that doubles from pullDelay up to pullLimit and is never shorter
// than the registry asked for. A wait that would pass the limit or the
// install's deadline ends the install instead.
func (s *Store) pull(ctx context.Context, args []string, service string, output io.Writer) error {
	delay := s.pullDelay
	for attempt := 1; ; attempt++ {
		watch := &pullWatch{output: output}
		err := s.runner.Run(ctx, args, watch)
		watch.flush()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		problem := watch.problem
		reason := cmp.Or(problem.reason, err.Error())
		if problem.permanent || problem.retry == "" {
			return fmt.Errorf("%w：%s（%s）", errPull, service, reason)
		}
		if attempt == pullAttempts {
			return fmt.Errorf("%w：%s 遇到%s，重试 %d 次后仍未成功（%s）", errPull, service, problem.retry, attempt-1, reason)
		}
		wait := max(delay, problem.retryAfter)
		limit := s.pullLimit
		if deadline, ok := ctx.Deadline(); ok {
			limit = min(limit, time.Until(deadline))
		}
		if wait > limit {
			return fmt.Errorf("%w：%s 遇到%s，需要等待 %s再试，超过了安装能等待的时间（%s）", errPull, service, problem.retry, waitText(wait), reason)
		}
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
	}
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

// What a failed pull's output says. Docker words a failure differently per
// image store: the graph-driver store prints the registry's error code
// ("toomanyrequests: retry-after: 285.983µs, …", "manifest unknown"), the
// containerd store the HTTP status ("429 Too Many Requests") or only the
// message ("error from registry: retry-after: …", "…: not found"). The
// texts are pinned against a real Docker by TestRealDockerPullFailures.
var (
	// A retry cannot fix an image or tag that does not exist, a registry
	// that refuses this client, or a download that is not the pinned image.
	permanentPull = regexp.MustCompile(`(?i)manifest unknown|name unknown|: not found|no matching manifest|pull access denied|unauthorized|authentication required|no basic auth credentials|denied|403 forbidden|invalid reference format|digest mismatch|unexpected commit digest|verification failed|d(?:id|oes) not match`)
	rateLimit     = regexp.MustCompile(`(?i)toomanyrequests|too many requests|retry-after`)
	retryAfter    = regexp.MustCompile(`(?i)retry-after:?\s*((?:[0-9.]+[a-zµμ]*)+)`)
	serverError   = regexp.MustCompile(`(?i)\b5\d\d (?:internal server error|bad gateway|service unavailable|gateway time-?out)\b|\bhttp 5\d\d\b|\bstatus(?: code)?:? 5\d\d\b`)
	networkError  = regexp.MustCompile(`(?i)connection reset|i/o timeout|tls handshake timeout|timeout awaiting response headers|client\.timeout exceeded|unexpected eof`)
)

// pullProblem is what went wrong in a pull, read from its output.
type pullProblem struct {
	// retry words a failure that a wait may fix; empty for others.
	retry string
	// permanent wins over any retryable failure in the same output.
	permanent  bool
	retryAfter time.Duration
	// reason is the last line that said what failed.
	reason string
}

func (p *pullProblem) read(line string) {
	line = strings.TrimSpace(line)
	matched := true
	switch {
	case permanentPull.MatchString(line):
		p.permanent = true
	case rateLimit.MatchString(line):
		p.retry = "镜像仓库限流"
		if match := retryAfter.FindStringSubmatch(line); match != nil {
			p.retryAfter = max(p.retryAfter, parseRetryAfter(match[1]))
		}
	case serverError.MatchString(line):
		p.retry = cmp.Or(p.retry, "镜像仓库暂时出错")
	case networkError.MatchString(line):
		p.retry = cmp.Or(p.retry, "镜像仓库连接中断或超时")
	default:
		matched = strings.Contains(strings.ToLower(line), "error")
	}
	if matched {
		p.reason = line
		if utf8.RuneCountInString(line) > reasonRunes {
			p.reason = string([]rune(line)[:reasonRunes]) + "…"
		}
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

// pullWatch passes a pull's output on and reads each of its lines.
type pullWatch struct {
	output  io.Writer
	partial []byte
	problem pullProblem
}

// Compose writes progress lines; one longer than this is read in pieces.
const maxPullLine = 64 << 10

func (w *pullWatch) Write(data []byte) (int, error) {
	w.partial = append(w.partial, data...)
	for {
		line, rest, found := bytes.Cut(w.partial, []byte("\n"))
		if !found {
			break
		}
		w.problem.read(string(line))
		w.partial = append(w.partial[:0], rest...)
	}
	if len(w.partial) > maxPullLine {
		w.flush()
	}
	return w.output.Write(data)
}

// flush reads a last line that did not end in a newline.
func (w *pullWatch) flush() {
	if len(w.partial) > 0 {
		w.problem.read(string(w.partial))
		w.partial = w.partial[:0]
	}
}
