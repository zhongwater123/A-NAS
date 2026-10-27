package ptyshell

import (
	"context"
	"io"
	"time"
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

// HangupGrace is how long a hung-up shell has to exit before it is killed.
const HangupGrace = 2 * time.Second
