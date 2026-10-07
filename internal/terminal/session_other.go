//go:build !unix

package terminal

import (
	"context"
	"errors"
)

const localShellSupported = false

type localSpawner struct {
	shell string
	dir   string
}

func (localSpawner) StartShell(context.Context, uint16, uint16) (Shell, error) {
	return nil, errors.New("terminal requires a Unix PTY")
}
