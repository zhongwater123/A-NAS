package filebroker

import (
	"context"
	"errors"
	"fmt"
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

const hangupGracePeriod = 2 * time.Second

// serveTerminal starts a login shell as the administrator on a new PTY and
// passes the master side back. The connection stays open for the life of
// the shell: closing it hangs the shell up, the shell exiting closes it, and
// a session that stops being valid ends the shell.
func (s *Server) serveTerminal(conn *net.UnixConn, req request) {
	ctx := context.Background()
	identity, err := s.config.Sessions.ResolveSessionIdentity(ctx, req.Token)
	if err != nil || !identity.Enabled {
		_ = writeMessage(conn, response{Error: &wireError{Code: codeUnauthenticated}}, nil)
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
	exited := make(chan int, 1)
	go func() {
		_ = cmd.Wait()
		exited <- cmd.ProcessState.ExitCode()
	}()
	err = writeMessage(conn, response{}, master)
	_ = master.Close()
	if err != nil {
		hangUp(cmd.Process.Pid, exited)
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
	for {
		select {
		case code := <-exited:
			_ = writeMessage(conn, response{ExitCode: &code}, nil)
			return
		case <-closed:
			code := hangUp(cmd.Process.Pid, exited)
			_ = writeMessage(conn, response{ExitCode: &code}, nil)
			return
		case <-recheck.C:
			current, err := s.config.Sessions.ResolveSessionIdentity(ctx, req.Token)
			if err != nil || !current.Enabled || current.Role != accounts.RoleAdmin || current.UID != identity.UID {
				s.config.Logger.Info("terminal session no longer valid; hanging up", "username", identity.Username)
				code := hangUp(cmd.Process.Pid, exited)
				_ = writeMessage(conn, response{ExitCode: &code}, nil)
				return
			}
		}
	}
}

// hangUp sends SIGHUP to the shell's process group, escalating to SIGKILL
// when the shell ignores it, and returns the exit code.
func hangUp(pid int, exited <-chan int) int {
	_ = syscall.Kill(-pid, syscall.SIGHUP)
	select {
	case code := <-exited:
		return code
	case <-time.After(hangupGracePeriod):
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	return <-exited
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
	if err := cmd.Start(); err != nil {
		_ = master.Close()
		return nil, nil, err
	}
	return cmd, master, nil
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
