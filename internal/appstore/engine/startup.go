package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/zhongwater123/A-NAS/internal/appstore"
)

// An install is only a success when its containers keep running. Compose
// returns as soon as they start, so an app that cannot write its folder or
// misses a setting would otherwise show as installed while it restarts
// (Demo test report, n8n).
const (
	defaultStartupWindow = 20 * time.Second
	defaultStartupPoll   = 2 * time.Second
)

// ContainerState is a project container as the startup check sees it.
type ContainerState struct {
	// Service is the Compose service the container runs.
	Service      string
	State        string // running, restarting, exited, created, paused or dead
	ExitCode     int
	RestartCount int
	// Health is starting, healthy or unhealthy, or empty without a check.
	Health string
}

// problem words what is wrong with a container, or nothing. A container that
// finished with exit code 0 is a one-off job and fine.
func (c ContainerState) problem() string {
	switch {
	case c.State == "restarting" || c.RestartCount > 0:
		return fmt.Sprintf("启动后不断重启（退出码 %d）", c.ExitCode)
	case (c.State == "exited" || c.State == "dead") && c.ExitCode != 0:
		return fmt.Sprintf("启动后退出（退出码 %d）", c.ExitCode)
	case c.State == "created":
		return "没有启动"
	case c.Health == "unhealthy":
		return "健康检查失败"
	}
	return ""
}

// StartupError reports the container that did not keep running, or, without
// a service, Compose not starting the app at all.
type StartupError struct {
	Service string
	Problem string
}

func (e *StartupError) Error() string {
	if e.Service == "" {
		return e.Problem
	}
	return fmt.Sprintf("容器 %s %s", e.Service, e.Problem)
}

func (e *StartupError) Is(target error) bool { return target == appstore.ErrAppNotRunning }

// SetStartupCheck changes how long installs watch their containers and how
// often; tests and the simulated engine shorten it.
func (s *Store) SetStartupCheck(window, poll time.Duration) {
	s.startupWindow, s.startupPoll = window, poll
}

// startupProblem watches a project's containers for the startup window and
// reports the first one that restarts, stops with an error, never starts or
// turns unhealthy.
func (s *Store) startupProblem(ctx context.Context, project string) error {
	deadline := time.Now().Add(s.startupWindow)
	for {
		states, err := s.docker.ProjectContainers(ctx, project)
		if err != nil {
			return fmt.Errorf("%w: cannot inspect app containers: %v", appstore.ErrAppNotRunning, err)
		}
		if len(states) == 0 {
			return &StartupError{Problem: "Docker Compose 没有创建任何容器"}
		}
		for _, state := range states {
			if problem := state.problem(); problem != "" {
				return &StartupError{Service: state.Service, Problem: problem}
			}
		}
		if !time.Now().Before(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.startupPoll):
		}
	}
}
