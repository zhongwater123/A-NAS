//go:build linux

package vectorindextest

import "syscall"

// serverAttributes end the server with the test binary: a binary that a test
// timeout or a killed gate ends runs no cleanup, and a server left running
// kept the gate's lock files open and blocked the next gates (2026-11-25).
func serverAttributes() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
