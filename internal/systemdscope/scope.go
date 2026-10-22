// Package systemdscope moves the processes the Host Agent starts for users,
// File Broker workers and terminal shells, into transient systemd scope units
// of their own (issue #166). In the Host Agent's cgroup they shared its
// memory and task limits, and the journal named anything a shell logged,
// with logger or systemd-cat, as the Host Agent's own.
package systemdscope

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	systemd "github.com/coreos/go-systemd/v22/dbus"
	"github.com/coreos/go-systemd/v22/unit"
	"github.com/godbus/dbus/v5"
)

// stopTimeout is how long systemd waits for what runs in a scope to exit
// after SIGTERM and SIGHUP before it kills it.
const stopTimeout = 10 * time.Second

// Placer starts the scopes through systemd's private socket, which only root
// can reach and which works without a D-Bus daemon.
type Placer struct {
	mu   sync.Mutex
	conn *systemd.Conn
	// boundTo is the Host Agent's own unit. Each scope binds to it, so
	// stopping, restarting or losing the Host Agent ends what it started, as
	// sharing its cgroup did.
	boundTo string
}

// New connects to systemd and finds the unit the calling process runs in.
func New(ctx context.Context) (*Placer, error) {
	conn, err := systemd.NewSystemdConnectionContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to systemd: %w", err)
	}
	boundTo, err := conn.GetUnitNameByPID(ctx, uint32(os.Getpid()))
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("find the Host Agent's unit: %w", err)
	}
	return &Placer{conn: conn, boundTo: boundTo}, nil
}

// Place starts the scope anas-<what>-<username>-<pid>.scope around pid and
// waits until systemd has moved the process into it. what says what the
// process does, as "terminal" or "files".
func (p *Placer) Place(ctx context.Context, pid int, what, username string) error {
	name := fmt.Sprintf("anas-%s-%s-%d.scope", what, unit.UnitNameEscape(username), pid)
	properties := []systemd.Property{
		systemd.PropDescription(fmt.Sprintf("A-NAS %s of %s", what, username)),
		systemd.PropPids(uint32(pid)),
		systemd.PropBindsTo(p.boundTo),
		{Name: "CollectMode", Value: dbus.MakeVariant("inactive-or-failed")},
		// An interactive shell ignores SIGTERM and ends on SIGHUP.
		{Name: "SendSIGHUP", Value: dbus.MakeVariant(true)},
		{Name: "TimeoutStopUSec", Value: dbus.MakeVariant(uint64(stopTimeout / time.Microsecond))},
	}
	conn, err := p.connection(ctx)
	if err != nil {
		return err
	}
	done := make(chan string, 1)
	if _, err := conn.StartTransientUnitContext(ctx, name, "fail", properties, done); err != nil {
		return fmt.Errorf("start %s: %w", name, err)
	}
	select {
	case result := <-done:
		if result != "done" {
			return fmt.Errorf("start %s: job %s", name, result)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("start %s: %w", name, ctx.Err())
	}
}

// connection reconnects after systemd dropped the connection, as it does
// when it re-executes itself.
func (p *Placer) connection(ctx context.Context) (*systemd.Conn, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn.Connected() {
		return p.conn, nil
	}
	conn, err := systemd.NewSystemdConnectionContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("reconnect to systemd: %w", err)
	}
	p.conn.Close()
	p.conn = conn
	return conn, nil
}

// Close disconnects from systemd. Scopes already started stay as they are.
func (p *Placer) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.conn.Close()
}
