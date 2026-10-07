package terminal

import (
	"errors"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// ProcessGroup is a started shell that leads its own process group. The group
// is signaled only until the shell is reaped: after that its PID, and with it
// the group ID, can belong to an unrelated process.
type ProcessGroup struct {
	cmd    *exec.Cmd
	mu     sync.Mutex
	reaped bool
	code   int
	done   chan struct{}
}

// WatchProcessGroup reaps cmd, already started as a process group leader,
// once it exits.
func WatchProcessGroup(cmd *exec.Cmd) *ProcessGroup {
	group := &ProcessGroup{cmd: cmd, code: -1, done: make(chan struct{})}
	go group.wait()
	return group
}

func (g *ProcessGroup) wait() {
	// Observe the exit without reaping, so the PID stays reserved while a
	// signal may still be sent to it; reap only under the lock.
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, g.cmd.Process.Pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	g.mu.Lock()
	_ = g.cmd.Wait()
	g.code = g.cmd.ProcessState.ExitCode()
	g.reaped = true
	g.mu.Unlock()
	close(g.done)
}

// Done is closed once the shell has exited and been reaped.
func (g *ProcessGroup) Done() <-chan struct{} { return g.done }

// ExitCode is the shell's exit code after Done is closed; -1 when it was
// killed by a signal or is unknown.
func (g *ProcessGroup) ExitCode() int { return g.code }

// HangUp sends SIGHUP to the group, escalates to SIGKILL when the shell is
// still running after grace, and returns the exit code. A shell that already
// exited is not signaled.
func (g *ProcessGroup) HangUp(grace time.Duration) int {
	g.signal(syscall.SIGHUP)
	select {
	case <-g.done:
		return g.code
	case <-time.After(grace):
	}
	g.signal(syscall.SIGKILL)
	<-g.done
	return g.code
}

func (g *ProcessGroup) signal(signal syscall.Signal) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.reaped {
		_ = syscall.Kill(-g.cmd.Process.Pid, signal)
	}
}
