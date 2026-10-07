//go:build !unix

package terminal

import (
	"context"
	"log/slog"

	"github.com/coder/websocket"
)

const ptySupported = false

func runSession(context.Context, *websocket.Conn, Config, *slog.Logger) (websocket.StatusCode, string) {
	return websocket.StatusInternalError, "terminal requires a Unix PTY"
}
