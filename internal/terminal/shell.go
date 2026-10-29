package terminal

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/coder/websocket"

	"github.com/zhongwater123/A-NAS/internal/ptyshell"
)

// Shell and Spawner live in internal/ptyshell, which the root file broker
// starts shells with without linking this WebSocket handler (#221).
type (
	Shell   = ptyshell.Shell
	Spawner = ptyshell.Spawner
)

const outputDrainTimeout = 500 * time.Millisecond

// runSession bridges one shell to the WebSocket until the shell exits, the
// client disconnects or ctx is canceled.
func runSession(ctx context.Context, conn *websocket.Conn, spawner Spawner, logger *slog.Logger) (websocket.StatusCode, string) {
	shell, err := spawner.StartShell(ctx, 80, 24)
	if err != nil {
		logger.ErrorContext(ctx, "terminal shell failed to start", "error", err)
		return websocket.StatusInternalError, "shell unavailable"
	}
	defer func() { _ = shell.Close() }()

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
