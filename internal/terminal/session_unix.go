//go:build unix

package terminal

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"
)

const ptySupported = true

const (
	outputDrainTimeout = 500 * time.Millisecond
	hangupGracePeriod  = 2 * time.Second
)

// runSession bridges one login shell on a new PTY to the WebSocket until the
// shell exits, the client disconnects or ctx is canceled.
func runSession(ctx context.Context, conn *websocket.Conn, config Config, logger *slog.Logger) (websocket.StatusCode, string) {
	cmd := exec.Command(config.Shell, "-l")
	cmd.Dir = config.Dir
	cmd.Env = shellEnvironment(config.Shell)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 80, Rows: 24})
	if err != nil {
		logger.ErrorContext(ctx, "terminal shell failed to start", "shell", config.Shell, "error", err)
		return websocket.StatusInternalError, "shell unavailable"
	}
	defer ptmx.Close()

	// Canceling a context passed to coder/websocket drops the connection
	// without a close frame, so I/O outlives ctx until the caller closes conn.
	connCtx := context.WithoutCancel(ctx)

	exited := make(chan int, 1)
	go func() {
		_ = cmd.Wait()
		exited <- cmd.ProcessState.ExitCode()
	}()

	output := make(chan struct{})
	go func() {
		defer close(output)
		buffer := make([]byte, 32<<10)
		for {
			n, err := ptmx.Read(buffer)
			if n > 0 && conn.Write(connCtx, websocket.MessageBinary, buffer[:n]) != nil {
				return
			}
			if err != nil {
				return
			}
		}
	}()

	input := make(chan struct{})
	go func() {
		defer close(input)
		for {
			kind, data, err := conn.Read(connCtx)
			if err != nil {
				return
			}
			if kind == websocket.MessageBinary {
				if _, err := ptmx.Write(data); err != nil {
					return
				}
				continue
			}
			var message controlMessage
			if json.Unmarshal(data, &message) == nil && message.validResize() {
				_ = pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(message.Cols), Rows: uint16(message.Rows)})
			}
		}
	}()

	select {
	case code := <-exited:
		select {
		case <-output:
		case <-time.After(outputDrainTimeout):
		}
		return websocket.StatusNormalClosure, fmt.Sprintf("exit %d", code)
	case <-input:
	case <-ctx.Done():
	}
	terminate(cmd.Process.Pid, exited)
	return websocket.StatusGoingAway, "session closed"
}

// terminate hangs up the shell's process group, escalating to SIGKILL when the
// shell ignores the hangup.
func terminate(pid int, exited <-chan int) {
	_ = syscall.Kill(-pid, syscall.SIGHUP)
	select {
	case <-exited:
		return
	case <-time.After(hangupGracePeriod):
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	<-exited
}
