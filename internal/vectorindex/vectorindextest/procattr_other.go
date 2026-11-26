//go:build !linux

package vectorindextest

import "syscall"

func serverAttributes() *syscall.SysProcAttr { return nil }
