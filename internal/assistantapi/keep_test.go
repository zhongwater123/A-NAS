package assistantapi

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

func init() {
	keepRetryMin = 20 * time.Millisecond
	keepRetryMax = 100 * time.Millisecond
}

var alice = accounts.User{ID: "u-alice", Username: "alice"}

// endingGate stands in for the File Broker's session gate: it refuses the
// sessions that ended and records the sessions it was asked to connect with.
type endingGate struct {
	inner Connector
	mu    sync.Mutex
	ended map[string]bool
	asked []string
}

func (g *endingGate) Connect(ctx context.Context, account string, fresh bool) (net.Conn, string, error) {
	session := accounts.SessionToken(ctx)
	g.mu.Lock()
	g.asked = append(g.asked, session)
	ended := g.ended[session]
	g.mu.Unlock()
	if ended {
		return nil, "", ErrSessionRejected
	}
	return g.inner.Connect(ctx, account, fresh)
}

func (g *endingGate) end(session string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ended[session] = true
}

func (g *endingGate) lastAsked() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.asked) == 0 {
		return ""
	}
	return g.asked[len(g.asked)-1]
}

func newKeeper(t *testing.T) (*Handler, *fakeServe, *endingGate) {
	t.Helper()
	serve := newFakeServe(t)
	gate := &endingGate{inner: NewSocketConnector(serve.dir, serve.dir), ended: map[string]bool{}}
	h := New(Options{Connector: gate, SocketDir: serve.dir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	t.Cleanup(func() {
		// Sessions that are still kept end with the test.
		h.keepsMu.Lock()
		var sessions []string
		for _, k := range h.keeps {
			for session := range k.sessions {
				sessions = append(sessions, session)
			}
		}
		h.keepsMu.Unlock()
		for _, session := range sessions {
			h.Forget(session)
		}
	})
	return h, serve, gate
}

func (f *fakeServe) connections() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns
}

func waitConnections(t *testing.T, f *fakeServe, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for f.connections() < want {
		if time.Now().After(deadline) {
			t.Fatalf("connections = %d, want %d", f.connections(), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitClosed(t *testing.T, f *fakeServe, what string) {
	t.Helper()
	select {
	case <-f.closed:
	case <-time.After(5 * time.Second):
		t.Fatal(what)
	}
}

func (h *Handler) kept(account string) bool {
	h.keepsMu.Lock()
	defer h.keepsMu.Unlock()
	return h.keeps[account] != nil
}

func waitNotKept(t *testing.T, h *Handler, account string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.kept(account) {
		if time.Now().After(deadline) {
			t.Fatalf("%s's instance is still kept", account)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAKeptInstanceStaysConnectedUntilTheMembersLastSessionEnds(t *testing.T) {
	h, serve, gate := newKeeper(t)
	h.Keep(alice, "phone")
	waitConnections(t, serve, 1)
	// A second session of hers shares the connection.
	h.Keep(alice, "laptop")
	time.Sleep(100 * time.Millisecond)
	if got := serve.connections(); got != 1 {
		t.Fatalf("connections after a second session = %d", got)
	}
	// Signing out on the phone moves the connection to the laptop's session.
	h.Forget("phone")
	waitClosed(t, serve, "signing out of the session it was kept with left the connection open")
	waitConnections(t, serve, 2)
	if got := gate.lastAsked(); got != "laptop" {
		t.Fatalf("kept again with %q", got)
	}
	h.Forget("laptop")
	waitClosed(t, serve, "signing out of her last session left the instance kept")
	waitNotKept(t, h, "alice")
	if got := serve.connections(); got != 2 {
		t.Fatalf("connections after she signed out = %d", got)
	}
}

func TestAnInstanceThatRestartsIsKeptAgain(t *testing.T) {
	h, serve, _ := newKeeper(t)
	h.Keep(alice, "laptop")
	waitConnections(t, serve, 1)
	serve.mu.Lock()
	conn := serve.conn
	serve.mu.Unlock()
	_ = conn.CloseNow()
	waitClosed(t, serve, "the instance did not drop the connection")
	waitConnections(t, serve, 2)
}

func TestAnInstanceIsLetGoWhenTheGateEndsTheSession(t *testing.T) {
	h, serve, gate := newKeeper(t)
	h.Keep(alice, "laptop")
	waitConnections(t, serve, 1)
	// The session expires; the gate cuts the connection and refuses it from now on.
	gate.end("laptop")
	serve.mu.Lock()
	conn := serve.conn
	serve.mu.Unlock()
	_ = conn.CloseNow()
	waitClosed(t, serve, "the cut connection stayed open")
	waitNotKept(t, h, "alice")
	time.Sleep(100 * time.Millisecond)
	if got := serve.connections(); got != 1 {
		t.Fatalf("connections after the session ended = %d", got)
	}
}

func TestNothingIsKeptForAMemberWithoutAnAssistant(t *testing.T) {
	h, serve, gate := newKeeper(t)
	h.Keep(accounts.User{ID: "u-bob", Username: "bob"}, "bob-session")
	h.Keep(accounts.User{ID: "u-x", Username: "../alice"}, "x-session")
	time.Sleep(100 * time.Millisecond)
	if serve.connections() != 0 || gate.lastAsked() != "" || h.kept("bob") {
		t.Fatalf("kept an instance for a member without one: %d connections", serve.connections())
	}
}

// Stopping the Product Service ends the kept connection and the watch over
// the member's tasks, and Wait returns once they stopped (#192).
func TestStoppingEndsTheKeptInstanceAndTheTaskWatch(t *testing.T) {
	serve := newFakeServe(t)
	ctx, stop := context.WithCancel(context.Background())
	h := New(Options{Connector: NewSocketConnector(serve.dir, serve.dir), SocketDir: serve.dir, Notices: &heardRuns{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Context: ctx})
	h.Keep(alice, "alice-session")
	waitConnections(t, serve, 1)
	stop()
	waitClosed(t, serve, "stopping left the kept connection open")
	stopped := make(chan struct{})
	go func() {
		h.Wait()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the keeper or the task watch did not stop")
	}
	// Nothing is kept once stopping began.
	h.Keep(accounts.User{ID: "u-alice", Username: "alice"}, "later-session")
	time.Sleep(100 * time.Millisecond)
	if got := serve.connections(); got != 1 {
		t.Fatalf("connections after stopping = %d", got)
	}
}
