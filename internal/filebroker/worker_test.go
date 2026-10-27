package filebroker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
)

// runTestWorker is the worker a harness starts. max-message=<bytes> lowers
// its message limit, and its file system holds operations on hold-* and
// stuck-* paths (holdingFS).
func runTestWorker(volume string, options []string) error {
	for _, option := range options {
		value, ok := strings.CutPrefix(option, "max-message=")
		limit, err := strconv.Atoi(value)
		if !ok || err != nil {
			return fmt.Errorf("unknown worker option %q", option)
		}
		maxMessage = limit
	}
	return runWorker(volume, holdingFS{files.NewRootFileSystem(volume, false)})
}

// holdingFS lets a test hold a worker's operation from another process. An
// operation on a path named hold-* marks it .started, waits for the request
// to be cancelled and marks it .cancelled; one on stuck-* waits for a
// .release file whatever happens, as an operation that ignores its context.
type holdingFS struct{ files.FileSystem }

func (h holdingFS) hold(ctx context.Context, path string) error {
	name := filepath.Base(path)
	if !strings.HasPrefix(name, "hold-") && !strings.HasPrefix(name, "stuck-") {
		return nil
	}
	if err := os.WriteFile(path+".started", nil, 0o600); err != nil {
		return err
	}
	if strings.HasPrefix(name, "stuck-") {
		for {
			if _, err := os.Stat(path + ".release"); err == nil {
				return nil
			}
			time.Sleep(time.Millisecond)
		}
	}
	<-ctx.Done()
	if err := os.WriteFile(path+".cancelled", nil, 0o600); err != nil {
		return err
	}
	return ctx.Err()
}

func (h holdingFS) Scan(ctx context.Context, path string, skip files.ScanSkip) ([]files.ScanEntry, error) {
	if err := h.hold(ctx, path); err != nil {
		return nil, err
	}
	return h.FileSystem.Scan(ctx, path, skip)
}

func (h holdingFS) Copy(ctx context.Context, from, to string) error {
	if err := h.hold(ctx, from); err != nil {
		return err
	}
	return h.FileSystem.Copy(ctx, from, to)
}

func (h holdingFS) Rename(ctx context.Context, from, to string) error {
	if err := h.hold(ctx, from); err != nil {
		return err
	}
	return h.FileSystem.Rename(ctx, from, to)
}

func TestTheWorkerSendsAScanInPages(t *testing.T) {
	volume := t.TempDir()
	makeTree(t, volume, 30, 100)
	broker, worker := socketPair(t)
	served := make(chan error, 1)
	go func() { served <- serveWorker(context.Background(), worker, files.NewRootFileSystem(volume, false)) }()

	if err := writeMessage(broker, request{Op: opScan, Path: volume, Paged: true}, nil); err != nil {
		t.Fatal(err)
	}
	var sizes []int
	for {
		var page response
		if _, err := readMessage(broker, &page); err != nil || page.Error != nil {
			t.Fatalf("page: %v %v", page.Error, err)
		}
		sizes = append(sizes, len(page.Entries))
		if !page.More {
			break
		}
	}
	if want := []int{scanPage, 3030 - scanPage}; !slices.Equal(sizes, want) {
		t.Fatalf("pages of %v entries, want %v", sizes, want)
	}
	// A Product Service from before pages gets the scan in one message.
	if err := writeMessage(broker, request{Op: opScan, Path: volume}, nil); err != nil {
		t.Fatal(err)
	}
	var whole response
	if _, err := readMessage(broker, &whole); err != nil || whole.More || len(whole.Entries) != 3030 {
		t.Fatalf("unpaged scan: %d entries, more %v, %v", len(whole.Entries), whole.More, err)
	}
	// A cancel that arrives after its request was answered leaves the next
	// request alone.
	if err := writeMessage(broker, request{Op: opCancel}, nil); err != nil {
		t.Fatal(err)
	}
	if err := writeMessage(broker, request{Op: opScan, Path: filepath.Join(volume, "d00"), Paged: true}, nil); err != nil {
		t.Fatal(err)
	}
	var next response
	if _, err := readMessage(broker, &next); err != nil || next.Error != nil || len(next.Entries) != 100 {
		t.Fatalf("scan after a late cancel: %d entries, %v %v", len(next.Entries), next.Error, err)
	}
	_ = broker.Close()
	if err := receive(t, served); err != nil {
		t.Fatalf("serveWorker() = %v after the broker left", err)
	}
}

func TestAScanLargerThanAPageArrivesWhole(t *testing.T) {
	h := newHarness(t)
	space := filepath.Join(h.volume, "spaces", "alice")
	makeTree(t, space, 30, 100)
	want, err := files.NewRootFileSystem(h.volume, false).Scan(context.Background(), space, files.ScanSkip{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := h.client.Scan(accounts.WithSessionToken(context.Background(), "alice-token"), space, files.ScanSkip{})
	if err != nil || !sameEntries(got, want) {
		t.Fatalf("Scan() through the broker = %d entries, %v; want %d", len(got), err, len(want))
	}
	// The executor of the assistant's changes reads a confined worker directly.
	member, err := h.server.memberFiles(alice)
	if err != nil {
		t.Fatal(err)
	}
	if got, err = member.Scan(context.Background(), space, files.ScanSkip{}); err != nil || !sameEntries(got, want) {
		t.Fatalf("Scan() through a confined worker = %d entries, %v; want %d", len(got), err, len(want))
	}
}

func TestAReplyOverTheLimitIsAnErrorAndTheWorkerServesOn(t *testing.T) {
	h := newHarness(t, "max-message=1024")
	ctx := accounts.WithSessionToken(context.Background(), "alice-token")
	space := filepath.Join(h.volume, "spaces", "alice")
	deep := filepath.Join(space, strings.Repeat("a", 250), strings.Repeat("b", 250), strings.Repeat("c", 250))
	if err := os.MkdirAll(deep, 0o770); err != nil {
		t.Fatal(err)
	}
	// One entry here does not fit in a message on its own.
	var refused *wireError
	if _, err := h.client.Scan(ctx, space, files.ScanSkip{}); !errors.As(err, &refused) || refused.Code != codeFailed || !strings.Contains(err.Error(), errTooLarge.Error()) {
		t.Fatalf("Scan() of an entry over the limit error = %v", err)
	}
	// Under the same limit, an ordinary tree travels in smaller pages.
	shared := filepath.Join(h.volume, "spaces", "shared")
	makeTree(t, shared, 3, 20)
	want, err := files.NewRootFileSystem(h.volume, false).Scan(context.Background(), shared, files.ScanSkip{})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := h.client.Scan(ctx, shared, files.ScanSkip{}); err != nil || !sameEntries(got, want) {
		t.Fatalf("Scan() under a small limit = %d entries, %v; want %d", len(got), err, len(want))
	}
	if _, err := h.client.Lstat(ctx, space); err != nil {
		t.Fatalf("Lstat() after the refused reply error = %v", err)
	}
	if got := h.spawned.Load(); got != 1 {
		t.Fatalf("spawned %d workers, want the one that refused the reply to serve on", got)
	}
}

func TestACancelledCallStopsTheWorkersOperation(t *testing.T) {
	h := newHarness(t)
	ctx := accounts.WithSessionToken(context.Background(), "alice-token")
	space := filepath.Join(h.volume, "spaces", "alice")
	held := filepath.Join(space, "hold-tree")
	if err := os.MkdirAll(held, 0o770); err != nil {
		t.Fatal(err)
	}
	for _, op := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"scan", func(ctx context.Context) error { _, err := h.client.Scan(ctx, held, files.ScanSkip{}); return err }},
		{"copy", func(ctx context.Context) error { return h.client.Copy(ctx, held, filepath.Join(space, "copy")) }},
	} {
		call, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- op.run(call) }()
		waitForFile(t, held+".started")
		cancel()
		if err := receive(t, done); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled %s error = %v", op.name, err)
		}
		// The worker saw the cancel and answered the broker, which took it back.
		waitForFile(t, held+".cancelled")
		waitUntil(t, "the worker is free", func() bool { _, busy := workerCounts(h.server); return busy == 0 })
		for _, marker := range []string{".started", ".cancelled"} {
			if err := os.Remove(held + marker); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := os.Lstat(filepath.Join(space, "copy")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the cancelled copy made its target: %v", err)
	}
	if _, err := h.client.Lstat(ctx, space); err != nil {
		t.Fatalf("Lstat() after the cancels error = %v", err)
	}
	if got := h.spawned.Load(); got != 1 {
		t.Fatalf("spawned %d workers, want one that outlived both cancels", got)
	}
}

func TestALongOperationDoesNotHoldUpTheMembersOtherCalls(t *testing.T) {
	h := newHarness(t)
	ctx := accounts.WithSessionToken(context.Background(), "alice-token")
	space := filepath.Join(h.volume, "spaces", "alice")
	if err := os.MkdirAll(space, 0o770); err != nil {
		t.Fatal(err)
	}
	first := holdScan(t, h, ctx, filepath.Join(space, "hold-0"))
	// While it runs, the member still lists and looks at files.
	if _, err := h.client.Lstat(ctx, space); err != nil {
		t.Fatalf("Lstat() beside a long scan error = %v", err)
	}
	if _, err := h.client.Scan(ctx, space, files.ScanSkip{}); err != nil {
		t.Fatalf("Scan() beside a long scan error = %v", err)
	}
	held := []heldCall{first}
	for i := 1; i < workersPerUser; i++ {
		held = append(held, holdScan(t, h, ctx, filepath.Join(space, fmt.Sprintf("hold-%d", i))))
	}
	// Every worker is busy: further calls wait, and one gives up with its caller.
	waiting, giveUp := context.WithCancel(ctx)
	gaveUp := make(chan error, 1)
	go func() { _, err := h.client.Lstat(waiting, space); gaveUp <- err }()
	next := make(chan error, 1)
	go func() { _, err := h.client.Lstat(ctx, space); next <- err }()
	select {
	case err := <-next:
		t.Fatalf("a call was served while every worker was busy: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	giveUp()
	if err := receive(t, gaveUp); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting call given up error = %v", err)
	}
	held[0].cancel()
	if err := receive(t, held[0].done); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled scan error = %v", err)
	}
	if err := receive(t, next); err != nil {
		t.Fatalf("waiting call error = %v", err)
	}
	for _, call := range held[1:] {
		call.cancel()
		if err := receive(t, call.done); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled scan error = %v", err)
		}
	}
	if got := h.spawned.Load(); got != workersPerUser {
		t.Fatalf("spawned %d workers, want %d", got, workersPerUser)
	}
}

// A worker waiting on systemd for its scope holds up its own account only:
// another account's calls, on a worker it has or on a new one, go on (#277).
func TestASlowPlacementHoldsUpNoOtherAccount(t *testing.T) {
	h := newHarness(t)
	placer := &slowPlacer{slow: "alice", delay: 3 * time.Second, entered: make(chan struct{}), release: make(chan struct{})}
	h.server.config.Scopes = placer
	t.Cleanup(placer.unblock)
	bob := accounts.WithSessionToken(context.Background(), "bob-token")
	space := filepath.Join(h.volume, "spaces", "bob")
	if err := os.MkdirAll(space, 0o770); err != nil {
		t.Fatal(err)
	}
	if _, err := h.client.Lstat(bob, space); err != nil {
		t.Fatal(err)
	}
	alice := make(chan error, 1)
	go func() {
		_, err := h.client.Lstat(accounts.WithSessionToken(context.Background(), "alice-token"), h.volume)
		alice <- err
	}()
	select {
	case <-placer.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("alice's worker was never placed")
	}
	// Bob's free worker serves one call; a long scan holds it, so the next
	// call starts a second worker while alice's is still being placed. Each
	// call has less time than the placement takes.
	quickly := func(what string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(bob, placer.delay/2)
		defer cancel()
		if _, err := h.client.Lstat(ctx, space); err != nil {
			t.Fatalf("Lstat() on %s error = %v", what, err)
		}
	}
	quickly("bob's worker")
	held := holdScan(t, h, bob, filepath.Join(space, "hold-0"))
	quickly("bob's new worker")
	select {
	case err := <-alice:
		t.Fatalf("alice's call returned before her worker was placed: %v", err)
	default:
	}
	held.cancel()
	if err := receive(t, held.done); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled scan error = %v", err)
	}
	placer.unblock()
	if err := receive(t, alice); err != nil {
		t.Fatalf("alice's call error = %v", err)
	}
}

// slowPlacer takes delay, or until unblock, to place slow's processes.
type slowPlacer struct {
	slow    string
	delay   time.Duration
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	enter   sync.Once
}

func (p *slowPlacer) Place(ctx context.Context, _ int, _, username string) error {
	if username != p.slow {
		return nil
	}
	p.enter.Do(func() { close(p.entered) })
	select {
	case <-p.release:
	case <-time.After(p.delay):
	case <-ctx.Done():
	}
	return nil
}

func (p *slowPlacer) unblock() { p.once.Do(func() { close(p.release) }) }

// Calls that wait on the same worker start share it, and its failure.
func TestAFailedStartReachesEveryCallWaitingOnIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var lookups atomic.Int32
		release := make(chan struct{})
		server, err := NewServer(Config{
			Sessions:     resolverFunc(func(string) (accounts.Identity, error) { return accounts.Identity{}, nil }),
			VolumeRoot:   t.TempDir(),
			VolumeReady:  func() bool { return true },
			Command:      func(string) *exec.Cmd { return exec.Command("false") },
			ProbeCommand: func() *exec.Cmd { return exec.Command("false") },
			LookupUID: func(string) (int, error) {
				lookups.Add(1)
				<-release
				return 0, errors.New("unknown user")
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		identity := accounts.Identity{Username: "alice", UID: 20101, Role: accounts.RoleMember, Enabled: true}
		key := workerKey{uid: identity.UID, role: identity.Role}
		const calls = 3
		failures := make(chan error, calls)
		for range calls {
			go func() {
				_, err := server.takeWorker(context.Background(), key, identity)
				failures <- err
			}()
		}
		synctest.Wait()
		close(release)
		for range calls {
			if err := <-failures; err == nil || !strings.Contains(err.Error(), "not provisioned") {
				t.Fatalf("takeWorker() error = %v, want the start's failure", err)
			}
		}
		if got := lookups.Load(); got != 1 {
			t.Fatalf("started %d times, want one start for the waiting calls", got)
		}
	})
}

func TestReapingStopsOnlyFreeWorkers(t *testing.T) {
	h := newHarness(t)
	ctx := accounts.WithSessionToken(context.Background(), "alice-token")
	space := filepath.Join(h.volume, "spaces", "alice")
	if err := os.MkdirAll(space, 0o770); err != nil {
		t.Fatal(err)
	}
	long := holdScan(t, h, ctx, filepath.Join(space, "hold-long"))
	if _, err := h.client.Lstat(ctx, space); err != nil {
		t.Fatal(err)
	}
	h.server.reapIdle(time.Now().Add(time.Hour))
	if total, busy := workerCounts(h.server); total != 1 || busy != 1 {
		t.Fatalf("after reaping %d workers are left, %d busy; want only the busy one", total, busy)
	}
	long.cancel()
	if err := receive(t, long.done); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled scan error = %v", err)
	}
	waitUntil(t, "the worker is free", func() bool { _, busy := workerCounts(h.server); return busy == 0 })
	h.server.reapIdle(time.Now().Add(time.Hour))
	if total, _ := workerCounts(h.server); total != 0 {
		t.Fatalf("%d free workers survived reaping", total)
	}
}

func TestCloseStopsBusyAndFreeWorkers(t *testing.T) {
	h := newHarness(t)
	ctx := accounts.WithSessionToken(context.Background(), "alice-token")
	space := filepath.Join(h.volume, "spaces", "alice")
	if err := os.MkdirAll(space, 0o770); err != nil {
		t.Fatal(err)
	}
	held := []heldCall{
		holdScan(t, h, ctx, filepath.Join(space, "hold-a")),
		holdScan(t, h, ctx, filepath.Join(space, "hold-b")),
	}
	if _, err := h.client.Lstat(ctx, space); err != nil {
		t.Fatal(err)
	}
	h.server.mu.Lock()
	var workers []*worker
	for _, p := range h.server.pools {
		workers = append(workers, p.workers...)
	}
	h.server.mu.Unlock()
	if len(workers) != 3 {
		t.Fatalf("%d workers, want two busy and one free", len(workers))
	}
	h.server.Close()
	for _, w := range workers {
		select {
		case <-w.exited:
		case <-time.After(10 * time.Second):
			t.Fatal("a worker outlived Close")
		}
	}
	for _, call := range held {
		if err := receive(t, call.done); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("call cut by Close error = %v, want ErrUnavailable", err)
		}
		call.cancel()
	}
	if _, err := h.client.Lstat(ctx, space); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Lstat() after Close error = %v, want ErrUnavailable", err)
	}
}

type heldCall struct {
	cancel context.CancelFunc
	done   chan error
}

// holdScan starts a scan of the hold-* folder path, which the worker holds
// until the call is cancelled.
func holdScan(t *testing.T, h *harness, ctx context.Context, path string) heldCall {
	t.Helper()
	if err := os.MkdirAll(path, 0o770); err != nil {
		t.Fatal(err)
	}
	call, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := h.client.Scan(call, path, files.ScanSkip{})
		done <- err
	}()
	waitForFile(t, path+".started")
	return heldCall{cancel: cancel, done: done}
}

// makeTree makes folders d00… each holding count files.
func makeTree(t *testing.T, root string, folders, count int) {
	t.Helper()
	for i := range folders {
		folder := filepath.Join(root, fmt.Sprintf("d%02d", i))
		if err := os.MkdirAll(folder, 0o770); err != nil {
			t.Fatal(err)
		}
		for j := range count {
			if err := os.WriteFile(filepath.Join(folder, fmt.Sprintf("f%03d", j)), nil, 0o660); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func sameEntries(got, want []files.ScanEntry) bool {
	return slices.EqualFunc(got, want, func(a, b files.ScanEntry) bool {
		if a.Path != b.Path || !a.Info.ModTime.Equal(b.Info.ModTime) {
			return false
		}
		a.Info.ModTime, b.Info.ModTime = time.Time{}, time.Time{}
		return a.Info == b.Info
	})
}

func workerCounts(s *Server) (total, busy int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.pools {
		for _, w := range p.workers {
			total++
			if w.busy {
				busy++
			}
		}
	}
	return total, busy
}

func socketPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	var conns [2]*net.UnixConn
	for i, fd := range pair {
		file := os.NewFile(uintptr(fd), "pair")
		connection, err := net.FileConn(file)
		_ = file.Close()
		if err != nil {
			t.Fatal(err)
		}
		conns[i] = connection.(*net.UnixConn)
		t.Cleanup(func() { _ = conns[i].Close() })
	}
	return conns[0], conns[1]
}

func receive(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("the call did not return within 10 s")
		return nil
	}
}

// waitForFile waits for a marker the worker, another process, writes.
func waitForFile(t *testing.T, path string) {
	t.Helper()
	waitUntil(t, filepath.Base(path), func() bool {
		_, err := os.Stat(path)
		return err == nil
	})
}

func waitUntil(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("gave up waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}
