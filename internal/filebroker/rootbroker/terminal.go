package rootbroker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/zhongwater123/A-NAS/internal/filebroker/wire"

	"github.com/creack/pty"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/ptyshell"
)

// ShellGateArgument is the Host Agent subcommand that becomes a terminal
// shell once the broker has moved it into its scope.
const ShellGateArgument = "terminal-shell"

// serveTerminal starts a login shell as the administrator on a new PTY and
// passes the master side back. The connection stays open for the life of
// the shell: closing it hangs the shell up, the shell exiting closes it, and
// a session that stops being valid ends the shell.
func (s *Server) serveTerminal(conn *net.UnixConn, req wire.Request) {
	ctx := context.Background()
	identity, refused := s.identify(ctx, req.Token)
	if refused != nil {
		_ = wire.WriteMessage(conn, wire.Response{Error: refused}, nil)
		return
	}
	if identity.Role != accountid.RoleAdmin {
		_ = wire.WriteMessage(conn, wire.Response{Error: &wire.Error{Code: wire.CodeForbidden, Message: "the terminal is for administrators"}}, nil)
		return
	}
	cmd, master, err := s.startShell(identity, req.Cols, req.Rows)
	if err != nil {
		s.config.Logger.Error("terminal shell failed to start", "username", identity.Username, "error", err)
		_ = wire.WriteMessage(conn, wire.Response{Error: &wire.Error{Code: wire.CodeUnavailable}}, nil)
		return
	}
	s.config.Logger.Info("terminal shell started", "username", identity.Username, "pid", cmd.Process.Pid)
	shell := ptyshell.WatchProcessGroup(cmd)
	err = wire.WriteMessage(conn, wire.Response{}, master)
	_ = master.Close()
	if err != nil {
		shell.HangUp(ptyshell.HangupGrace)
		return
	}
	closed := make(chan struct{})
	go func() {
		// The client sends nothing more; a read returns when it disconnects.
		_, _ = conn.Read(make([]byte, 1))
		close(closed)
	}()
	recheck := time.NewTicker(s.config.TerminalRecheck)
	defer recheck.Stop()
	unconfirmed := 0
	for {
		select {
		case <-shell.Done():
			code := shell.ExitCode()
			_ = wire.WriteMessage(conn, wire.Response{ExitCode: &code}, nil)
			return
		case <-closed:
			code := shell.HangUp(ptyshell.HangupGrace)
			_ = wire.WriteMessage(conn, wire.Response{ExitCode: &code}, nil)
			return
		case <-recheck.C:
			if !s.recheck(ctx, req.Token, &unconfirmed, func(current accountid.Identity) bool {
				return current.Role == accountid.RoleAdmin && current.UID == identity.UID
			}) {
				s.config.Logger.Info("terminal session no longer valid; hanging up", "username", identity.Username)
				code := shell.HangUp(ptyshell.HangupGrace)
				_ = wire.WriteMessage(conn, wire.Response{ExitCode: &code}, nil)
				return
			}
		}
	}
}

func (s *Server) startShell(identity accountid.Identity, cols, rows uint16) (*exec.Cmd, *os.File, error) {
	credential, err := s.credential(identity)
	if err != nil {
		return nil, nil, err
	}
	if cols == 0 || cols > 1000 || rows == 0 || rows > 1000 {
		cols, rows = 80, 24
	}
	master, slave, err := pty.Open()
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = slave.Close() }()
	if !s.config.skipCredentials {
		// Like login(1): the user owns their terminal, the tty group may write.
		if err := slave.Chown(identity.UID, ttyGroup()); err != nil {
			_ = master.Close()
			return nil, nil, err
		}
		if err := slave.Chmod(0o620); err != nil {
			_ = master.Close()
			return nil, nil, err
		}
	}
	if err := pty.Setsize(master, &pty.Winsize{Cols: cols, Rows: rows}); err != nil {
		_ = master.Close()
		return nil, nil, err
	}
	home := "/"
	private := filepath.Join(s.config.VolumeRoot, "spaces", "private", identity.Username)
	if info, err := os.Stat(private); err == nil && info.IsDir() && s.config.VolumeReady() {
		home = private
	}
	cmd := s.config.ShellCommand()
	cmd.Dir = home
	cmd.Env = []string{
		"TERM=xterm-256color", "COLORTERM=truecolor", "SHELL=" + cmd.Path,
		"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8",
		"HOME=" + home, "USER=" + identity.Username, "LOGNAME=" + identity.Username,
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if !s.config.skipCredentials {
		cmd.SysProcAttr.Credential = credential
	}
	var release *os.File
	if s.config.Scopes != nil {
		var wait *os.File
		if wait, release, err = os.Pipe(); err != nil {
			_ = master.Close()
			return nil, nil, err
		}
		defer func() { _ = wait.Close() }()
		cmd = gateShell(s.executable, cmd, wait)
	}
	if err := cmd.Start(); err != nil {
		if release != nil {
			_ = release.Close()
		}
		_ = master.Close()
		return nil, nil, err
	}
	if release != nil {
		s.place(cmd.Process.Pid, "terminal", identity.Username)
		_, _ = release.Write([]byte{1})
		_ = release.Close()
	}
	return cmd, master, nil
}

// gateShell runs the shell through the broker's own binary, which waits for
// a byte on descriptor 3 before it becomes the shell: nothing the shell runs,
// its profile included, happens in the Host Agent's cgroup.
func gateShell(executable string, shell *exec.Cmd, wait *os.File) *exec.Cmd {
	gated := exec.Command(executable)
	gated.Args = append([]string{executable, ShellGateArgument, shell.Path}, shell.Args...)
	gated.Dir, gated.Env = shell.Dir, shell.Env
	gated.Stdin, gated.Stdout, gated.Stderr = shell.Stdin, shell.Stdout, shell.Stderr
	gated.SysProcAttr = shell.SysProcAttr
	gated.ExtraFiles = []*os.File{wait}
	return gated
}

// RunShellGate waits until the broker releases the shell, then becomes it.
// args are the shell's path followed by its argument list.
func RunShellGate(args []string) error {
	if len(args) < 2 {
		return errors.New("the terminal gate needs the shell's path and arguments")
	}
	gate := os.NewFile(3, "terminal-gate")
	var released [1]byte
	_, err := io.ReadFull(gate, released[:])
	_ = gate.Close()
	if err != nil {
		return fmt.Errorf("the File Broker did not release the shell: %w", err)
	}
	return syscall.Exec(args[0], args[1:], os.Environ()) //nolint:gosec // G702: the broker built this command line from the configured shell.
}

func ttyGroup() int {
	if group, err := user.LookupGroup("tty"); err == nil {
		if gid, err := strconv.Atoi(group.Gid); err == nil {
			return gid
		}
	}
	return 5
}
