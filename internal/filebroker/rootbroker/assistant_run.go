package rootbroker

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/zhongwater123/A-NAS/internal/filebroker/wire"

	"github.com/zhongwater123/A-NAS/internal/accountid"
)

// A member runs one of their scheduled tasks now ("立即运行") in a round of
// their cron unit, where every scheduled task runs, never in their instance:
// Hermes runs a task only inside its gateway, and asked for a run by
// `hermes serve` it starts a gateway of its own inside the instance, which
// stays there, takes over the member's due tasks and records its first run
// as failed (#469).
//
// The broker's part is the part that needs root. Behind the member's live
// session (ADR 0024 decision 10) it notes the task as an empty file named
// after it in AssistantRuns/<username>, which only root may enter, and starts
// the member's cron unit. The round takes what waits there as its own
// (ai/assistant-spike/run-requests.sh), queues each task with Hermes's own
// trigger_job and runs it as it runs a task that is due
// (ai/assistant-spike/cron_round.py). A request that comes while a round runs
// stays for the next one, which the waker starts.

var (
	// assistantTaskPattern is the id Hermes gives a job; matching it keeps a
	// request from being a path, or one of Hermes's lookups by job name.
	assistantTaskPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)
	errTooManyRuns       = errors.New("too many tasks wait to run")
)

const (
	// maxAssistantRuns bounds the tasks of one member waiting for a round.
	maxAssistantRuns = 32
	// assistantRoundStart bounds the request to systemd; the round itself
	// goes on without the broker.
	assistantRoundStart = 10 * time.Second
)

// serveAssistantRun notes that the member behind the session asked to run a
// scheduled task now and starts a round of their tasks.
func (s *Server) serveAssistantRun(req wire.Request) wire.Response {
	ctx := context.Background()
	identity, refused := s.identify(ctx, req.Token)
	if refused != nil {
		return wire.Response{Error: refused}
	}
	if s.config.AssistantSockets == "" || s.config.AssistantRuns == "" {
		return wire.Response{Error: &wire.Error{Code: wire.CodeUnavailable, Message: "the assistant is not enabled"}}
	}
	if !accountid.ValidAssistantAccount(identity.Username) {
		return wire.Response{Error: &wire.Error{Code: wire.CodeForbidden, Message: "this account has no assistant"}}
	}
	if !assistantTaskPattern.MatchString(req.Task) {
		return wire.Response{Error: &wire.Error{Code: wire.CodeInvalidRequest, Message: "invalid task"}}
	}
	if err := s.noteAssistantRun(identity.Username, req.Task); err != nil {
		if errors.Is(err, errTooManyRuns) {
			return wire.Response{Error: &wire.Error{Code: wire.CodeUnavailable, Message: err.Error()}}
		}
		s.config.Logger.Error("assistant task not noted", "username", identity.Username, "task", req.Task, "error", err)
		return wire.Response{Error: &wire.Error{Code: wire.CodeUnavailable, Message: "the task cannot be run right now"}}
	}
	start, cancel := context.WithTimeout(ctx, assistantRoundStart)
	defer cancel()
	if err := s.config.StartAssistantRound(start, identity.Username); err != nil {
		// Nothing will run it: an account without the units, or a disabled
		// one whose units are masked.
		_ = os.Remove(filepath.Join(s.config.AssistantRuns, identity.Username, req.Task))
		s.config.Logger.Error("assistant round not started", "username", identity.Username, "task", req.Task, "error", err)
		return wire.Response{Error: &wire.Error{Code: wire.CodeUnavailable, Message: "the task cannot be run right now"}}
	}
	s.config.Logger.Info("assistant task asked to run now", "username", identity.Username, "task", req.Task)
	return wire.Response{}
}

// noteAssistantRun leaves the member's request where the next round finds
// it. A round takes the whole directory by renaming it, so a request that
// finds the directory gone makes it again: it is then the next round's.
func (s *Server) noteAssistantRun(username, task string) error {
	if err := os.MkdirAll(s.config.AssistantRuns, 0o700); err != nil {
		return err
	}
	waiting := filepath.Join(s.config.AssistantRuns, username)
	for attempt := 0; ; attempt++ {
		if err := os.Mkdir(waiting, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		entries, err := os.ReadDir(waiting)
		if err == nil && len(entries) >= maxAssistantRuns {
			return errTooManyRuns
		}
		if err == nil {
			var file *os.File
			if file, err = os.OpenFile(filepath.Join(waiting, task), os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600); err == nil {
				return file.Close()
			}
		}
		if !errors.Is(err, fs.ErrNotExist) || attempt > 0 {
			return err
		}
	}
}

// startAssistantRound asks systemd to start the member's cron unit and does
// not wait for the round, which lasts as long as its tasks.
func startAssistantRound(ctx context.Context, username string) error {
	unit := "anas-assistant-cron@" + username + ".service"
	output, err := exec.CommandContext(ctx, "systemctl", "start", "--no-block", unit).CombinedOutput()
	if err != nil {
		return fmt.Errorf("start %s: %w: %s", unit, err, strings.TrimSpace(string(output)))
	}
	return nil
}
