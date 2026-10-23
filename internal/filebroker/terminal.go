package filebroker

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
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/terminal"
)

const opTerminal = "terminal"

// ShellGateArgument is the Host Agent subcommand that becomes a terminal
// shell once the broker has moved it into its scope.
const ShellGateArgument = "terminal-shell"

const hangupGracePeriod = 2 * time.Second

// serveTerminal starts a login shell as the administrator on a new PTY and
// passes the master side back. The connection stays open for the life of
// the shell: closing it hangs the shell up, the shell exiting closes it, and
// a session that stops being valid ends the shell.
func (s *Server) serveTerminal(conn *net.UnixConn, req request) {
	ctx := context.Background()
	identity, refused := s.identify(ctx, req.Token)
	if refused != nil {
		_ = writeMessage(conn, response{Error: refused}, nil)
		return
	}
	if identity.Role != accounts.RoleAdmin {
		_ = writeMessage(conn, response{Error: &wireError{Code: codeForbidden, Message: "the terminal is for administrators"}}, nil)
		return
	}
	cmd, master, err := s.startShell(identity, req.Cols, req.Rows)
	if err != nil {
		s.config.Logger.Error("terminal shell failed to start", "username", identity.Username, "error", err)
		_ = writeMessage(conn, response{Error: &wireError{Code: codeUnavailable}}, nil)
		return
	}
	s.config.Logger.Info("terminal shell started", "username", identity.Username, "pid", cmd.Process.Pid)
	shell := terminal.WatchProcessGroup(cmd)
	err = writeMessage(conn, response{}, master)
	_ = master.Close()
	if err != nil {
		shell.HangUp(hangupGracePeriod)
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
			_ = writeMessage(conn, response{ExitCode: &code}, nil)
			return
		case <-closed:
			code := shell.HangUp(hangupGracePeriod)
			_ = writeMessage(conn, response{ExitCode: &code}, nil)
			return
		case <-recheck.C:
			if !s.recheck(ctx, req.Token, &unconfirmed, func(current accounts.Identity) bool {
				return current.Role == accounts.RoleAdmin && current.UID == identity.UID
			}) {
				s.config.Logger.Info("terminal session no longer valid; hanging up", "username", identity.Username)
				code := shell.HangUp(hangupGracePeriod)
				_ = writeMessage(conn, response{ExitCode: &code}, nil)
				return
			}
		}
	}
}

func (s *Server) startShell(identity accounts.Identity, cols, rows uint16) (*exec.Cmd, *os.File, error) {
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
	defer slave.Close()
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
		defer wait.Close()
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

// StartShell asks the File Broker for a shell running as the signed-in
// administrator. The broker connection is held until Close.
func (c *Client) StartShell(ctx context.Context, cols, rows uint16) (terminal.Shell, error) {
	token := accounts.SessionToken(ctx)
	if token == "" {
		return nil, fmt.Errorf("%w: no session for terminal", ErrUnavailable)
	}
	var dialer net.Dialer
	connection, err := dialer.DialContext(ctx, "unix", c.socket)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	conn := connection.(*net.UnixConn)
	if err := writeMessage(conn, request{Token: token, Op: opTerminal, Cols: cols, Rows: rows}, nil); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	var result response
	master, err := readMessage(conn, &result)
	if err == nil && result.Error != nil {
		err = result.Error
	}
	if err == nil && master == nil {
		err = errors.New("no terminal returned")
	}
	if err != nil {
		closeFile(master)
		_ = conn.Close()
		return nil, err
	}
	shell := &brokerShell{master: master, conn: conn, done: make(chan struct{}), code: -1}
	go shell.watch()
	return shell, nil
}

type brokerShell struct {
	master *os.File
	conn   *net.UnixConn
	done   chan struct{}
	code   int
	close  sync.Once
}

// watch waits for the broker to report that the shell exited.
func (s *brokerShell) watch() {
	var result response
	if file, err := readMessage(s.conn, &result); err == nil {
		closeFile(file)
		if result.ExitCode != nil {
			s.code = *result.ExitCode
		}
	}
	close(s.done)
}

func (s *brokerShell) Read(p []byte) (int, error)  { return s.master.Read(p) }
func (s *brokerShell) Write(p []byte) (int, error) { return s.master.Write(p) }
func (s *brokerShell) Done() <-chan struct{}       { return s.done }
func (s *brokerShell) ExitCode() int               { return s.code }

func (s *brokerShell) Resize(cols, rows uint16) error {
	return pty.Setsize(s.master, &pty.Winsize{Cols: cols, Rows: rows})
}

// Close disconnects from the broker, which hangs the shell up, and waits for
// the broker to report the exit.
func (s *brokerShell) Close() error {
	s.close.Do(func() {
		_ = s.master.Close()
		_ = s.conn.CloseWrite()
		select {
		case <-s.done:
		case <-time.After(hangupGracePeriod + time.Second):
		}
		_ = s.conn.Close()
	})
	return nil
}

var _ terminal.Spawner = (*Client)(nil)
