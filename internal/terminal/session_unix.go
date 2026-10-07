//go:build unix

package terminal

import (
	"context"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

const localShellSupported = true

const hangupGracePeriod = 2 * time.Second

// localSpawner starts shells as the Product Service's own user. It is only
// used for development without a Host Agent.
type localSpawner struct {
	shell string
	dir   string
}

func (s localSpawner) StartShell(_ context.Context, cols, rows uint16) (Shell, error) {
	cmd := exec.Command(s.shell, "-l")
	cmd.Dir = s.dir
	cmd.Env = shellEnvironment(s.shell)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return nil, err
	}
	shell := &localShell{cmd: cmd, ptmx: ptmx, done: make(chan struct{}), code: -1}
	go func() {
		_ = cmd.Wait()
		shell.code = cmd.ProcessState.ExitCode()
		close(shell.done)
	}()
	return shell, nil
}

type localShell struct {
	cmd   *exec.Cmd
	ptmx  *os.File
	done  chan struct{}
	code  int
	close sync.Once
}

func (s *localShell) Read(p []byte) (int, error)  { return s.ptmx.Read(p) }
func (s *localShell) Write(p []byte) (int, error) { return s.ptmx.Write(p) }
func (s *localShell) Done() <-chan struct{}       { return s.done }
func (s *localShell) ExitCode() int               { return s.code }

func (s *localShell) Resize(cols, rows uint16) error {
	return pty.Setsize(s.ptmx, &pty.Winsize{Cols: cols, Rows: rows})
}

// Close hangs up the shell's process group, escalating to SIGKILL when the
// shell ignores the hangup.
func (s *localShell) Close() error {
	s.close.Do(func() {
		_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGHUP)
		select {
		case <-s.done:
		case <-time.After(hangupGracePeriod):
			_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
			<-s.done
		}
		_ = s.ptmx.Close()
	})
	return nil
}
