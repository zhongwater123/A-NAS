package filebroker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/zhongwater123/A-NAS/internal/filebroker/wire"

	"github.com/creack/pty"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/ptyshell"
)

// StartShell asks the File Broker for a shell running as the signed-in
// administrator. The broker connection is held until Close.
func (c *Client) StartShell(ctx context.Context, cols, rows uint16) (ptyshell.Shell, error) {
	token := accountid.SessionToken(ctx)
	if token == "" {
		return nil, fmt.Errorf("%w: no session for terminal", wire.ErrUnavailable)
	}
	var dialer net.Dialer
	connection, err := dialer.DialContext(ctx, "unix", c.socket)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", wire.ErrUnavailable, err)
	}
	conn := connection.(*net.UnixConn)
	if err := wire.WriteMessage(conn, wire.Request{Token: token, Op: wire.OpTerminal, Cols: cols, Rows: rows}, nil); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: %v", wire.ErrUnavailable, err)
	}
	var result wire.Response
	master, err := wire.ReadMessage(conn, &result)
	if err == nil && result.Error != nil {
		err = result.Error
	}
	if err == nil && master == nil {
		err = errors.New("no terminal returned")
	}
	if err != nil {
		wire.CloseFile(master)
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
	var result wire.Response
	if file, err := wire.ReadMessage(s.conn, &result); err == nil {
		wire.CloseFile(file)
		if result.ExitCode != nil {
			s.code = *result.ExitCode
		}
	}
	close(s.done)
}

func (s *brokerShell) Read(p []byte) (int, error) { return s.master.Read(p) }

func (s *brokerShell) Write(p []byte) (int, error) { return s.master.Write(p) }

func (s *brokerShell) Done() <-chan struct{} { return s.done }

func (s *brokerShell) ExitCode() int { return s.code }

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
		case <-time.After(ptyshell.HangupGrace + time.Second):
		}
		_ = s.conn.Close()
	})
	return nil
}

var _ ptyshell.Spawner = (*Client)(nil)
