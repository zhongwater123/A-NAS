package terminal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/coder/websocket"
)

// Shell is an interactive shell on a PTY.
type Shell interface {
	io.ReadWriter
	Resize(cols, rows uint16) error
	// Done is closed once the shell has exited; ExitCode is meaningful after
	// that and is -1 when unknown.
	Done() <-chan struct{}
	ExitCode() int
	// Close hangs up the shell and waits for it to exit.
	Close() error
}

// Spawner starts shells. In production the File Broker starts them as the
// signed-in administrator's Linux account (ADR 0008); ctx carries the
// session token. Development uses a local PTY.
type Spawner interface {
	StartShell(ctx context.Context, cols, rows uint16) (Shell, error)
}

const outputDrainTimeout = 500 * time.Millisecond

// runSession bridges one shell to the WebSocket until the shell exits, the
// client disconnects or ctx is canceled.
func runSession(ctx context.Context, conn *websocket.Conn, spawner Spawner, logger *slog.Logger) (websocket.StatusCode, string) {
	shell, err := spawner.StartShell(ctx, 80, 24)
	if err != nil {
		logger.ErrorContext(ctx, "terminal shell failed to start", "error", err)
		return websocket.StatusInternalError, "shell unavailable"
	}
	defer shell.Close()

	// Canceling a context passed to coder/websocket drops the connection
	// without a close frame, so I/O outlives ctx until the caller closes conn.
	connCtx := context.WithoutCancel(ctx)

	output := make(chan struct{})
	go func() {
		defer close(output)
		buffer := make([]byte, 32<<10)
		for {
			n, err := shell.Read(buffer)
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
				if _, err := shell.Write(data); err != nil {
					return
				}
				continue
			}
			var message controlMessage
			if json.Unmarshal(data, &message) == nil && message.validResize() {
				_ = shell.Resize(uint16(message.Cols), uint16(message.Rows))
			}
		}
	}()

	select {
	case <-shell.Done():
		select {
		case <-output:
		case <-time.After(outputDrainTimeout):
		}
		return websocket.StatusNormalClosure, fmt.Sprintf("exit %d", shell.ExitCode())
	case <-input:
	case <-ctx.Done():
	}
	_ = shell.Close()
	return websocket.StatusGoingAway, "session closed"
}
