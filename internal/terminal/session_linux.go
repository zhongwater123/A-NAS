package terminal

import (
	"context"
	"os"
	"os/exec"
	"sync"
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
	return &localShell{group: WatchProcessGroup(cmd), ptmx: ptmx}, nil
}

type localShell struct {
	group *ProcessGroup
	ptmx  *os.File
	close sync.Once
}

func (s *localShell) Read(p []byte) (int, error)  { return s.ptmx.Read(p) }
func (s *localShell) Write(p []byte) (int, error) { return s.ptmx.Write(p) }
func (s *localShell) Done() <-chan struct{}       { return s.group.Done() }
func (s *localShell) ExitCode() int               { return s.group.ExitCode() }

func (s *localShell) Resize(cols, rows uint16) error {
	return pty.Setsize(s.ptmx, &pty.Winsize{Cols: cols, Rows: rows})
}

// Close hangs up the shell's process group unless the shell already exited.
func (s *localShell) Close() error {
	s.close.Do(func() {
		s.group.HangUp(hangupGracePeriod)
		_ = s.ptmx.Close()
	})
	return nil
}
