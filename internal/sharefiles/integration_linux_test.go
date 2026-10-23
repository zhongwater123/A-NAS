//go:build linux

package sharefiles

import (
	"context"
	"errors"
	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/shares"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type credentials struct{}

func (credentials) SetCredential(context.Context, accounts.CredentialRequest) error { return nil }
func (credentials) DisableCredential(context.Context, string) error                 { return nil }

type fixture struct {
	local          *Local
	remote         *Remote
	files          *files.Service
	users          *accounts.Service
	alice, bob     accounts.User
	ac, bc         context.Context
	as, bs         string
	volume, socket string
}

func setup(t *testing.T) *fixture { return setupFS(t, nil) }
func setupFS(t *testing.T, wrap func(files.FileSystem) files.FileSystem) *fixture {
	t.Helper()
	root := t.TempDir()
	ctx := context.Background()
	db, e := accounts.OpenSQLite(filepath.Join(root, "accounts.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = db.Close() })
	users := accounts.NewService(db, credentials{}, accounts.Options{})
	alice, e := users.SetupAdministrator(ctx, "alice", "correct horse battery staple")
	if e != nil {
		t.Fatal(e)
	}
	bob, e := users.CreateMember(ctx, alice, "bob", "initial secure password")
	if e != nil {
		t.Fatal(e)
	}
	if e = users.ChangePassword(ctx, bob, "initial secure password", "changed secure password"); e != nil {
		t.Fatal(e)
	}
	a, e := users.Authenticate(ctx, "alice", "correct horse battery staple")
	if e != nil {
		t.Fatal(e)
	}
	b, e := users.Authenticate(ctx, "bob", "changed secure password")
	if e != nil {
		t.Fatal(e)
	}
	catalog, e := files.OpenSQLite(filepath.Join(root, "files.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	volume := filepath.Join(root, "volume")
	if e = os.Mkdir(volume, 0700); e != nil {
		t.Fatal(e)
	}
	var fsys files.FileSystem = files.NewRootFileSystem(volume, true)
	if wrap != nil {
		fsys = wrap(fsys)
	}
	fs := files.NewService(catalog, volume, users, files.Options{FileSystem: fsys, AllowUnverifiedVolume: true, DisableCapacityReserve: true})
	f := &fixture{local: NewLocal(fs, users), files: fs, users: users, alice: alice, bob: bob, ac: accounts.WithSessionToken(ctx, a.Token), bc: accounts.WithSessionToken(ctx, b.Token), volume: volume}
	for _, u := range []accounts.User{alice, bob} {
		spaces, e := users.ListSpaces(ctx, u)
		if e != nil {
			t.Fatal(e)
		}
		for _, s := range spaces {
			if s.Kind == accounts.SpaceKindPrivate {
				if u.ID == alice.ID {
					f.as = s.ID
				} else {
					f.bs = s.ID
				}
			}
		}
	}
	// Short socket paths are necessary on Linux's sockaddr_un.
	socketDir, e := os.MkdirTemp("", "anas-bridge-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	if group := os.Getenv("ANAS_SHARE_TEST_GID"); group != "" {
		gid, e := strconv.Atoi(group)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.Chown(socketDir, -1, gid); e != nil {
			t.Fatal(e)
		}
	}
	f.socket = filepath.Join(socketDir, "files.sock")
	stop, e := Serve(ctx, f.socket, f.local)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(stop)
	f.remote, e = NewRemote(ctx, f.socket)
	if e != nil {
		t.Fatal(e)
	}
	return f
}
func user(a accounts.User) shares.User { return shares.User{ID: a.ID, Username: a.Username} }
func TestRemoteCaptureSaveKeepsEmptyDirectoriesAndRenames(t *testing.T) {
	f := setup(t)
	dir, e := f.files.CreateDirectory(f.ac, f.alice, f.as, "", "source")
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.files.CreateDirectory(f.ac, f.alice, f.as, dir.ID, "empty")
	if e != nil {
		t.Fatal(e)
	}
	file, e := f.files.Upload(f.ac, f.alice, f.as, dir.ID, "hello.txt", strings.NewReader("hello"))
	if e != nil {
		t.Fatal(e)
	}
	backend := NewBackend(f.remote)
	var entries []shares.Entry
	contents := map[string]string{}
	e = backend.Capture(f.ac, user(f.alice), []string{dir.ID, file.ID}, func(entry shares.Entry, r io.Reader) error {
		entries = append(entries, entry)
		if r != nil {
			b, e := io.ReadAll(r)
			contents[entry.ID] = string(b)
			return e
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if len(entries) != 3 {
		t.Fatalf("entries %+v", entries)
	}
	for range 2 {
		e = backend.Save(f.bc, user(f.bob), shares.Target{RootID: f.bs, ShareID: "share"}, entries, func(id string) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(contents[id])), nil }, func(_ string, publish func() error) error { return publish() }, nil)
		if e != nil {
			t.Fatal(e)
		}
	}
	top, e := f.files.List(f.bc, f.bob, f.bs, "")
	if e != nil {
		t.Fatal(e)
	}
	if len(top) != 2 {
		t.Fatalf("target %+v", top)
	}
	for _, folder := range top {
		children, e := f.files.List(f.bc, f.bob, f.bs, folder.ID)
		if e != nil || len(children) != 2 {
			t.Fatalf("children %+v %v", children, e)
		}
	}
	if _, e = f.remote.Select(f.bc, user(f.alice), []string{file.ID}); e == nil {
		t.Fatal("claimed actor bypassed session")
	}
	stat, e := os.Stat(f.socket)
	if e != nil {
		t.Fatal(e)
	}
	parent, err := os.Stat(filepath.Dir(f.socket))
	if err != nil {
		t.Fatal(err)
	}
	if stat.Sys().(*syscall.Stat_t).Gid != parent.Sys().(*syscall.Stat_t).Gid {
		t.Fatal("socket did not inherit parent group")
	}
	if stat.Mode().Perm() != 0660 {
		t.Fatal(stat.Mode())
	}
}
func TestExpiredSessionCanAbortStagedUpload(t *testing.T) {
	f := setup(t)
	id, e := f.remote.Stage(f.bc, user(f.bob), shares.Target{RootID: f.bs, ShareID: "s"}, shares.Entry{Path: "a.txt", Size: 5}, strings.NewReader("hello"))
	if e != nil {
		t.Fatal(e)
	}
	if e = f.users.EndSession(context.Background(), accounts.SessionToken(f.bc)); e != nil {
		t.Fatal(e)
	}
	if e = f.remote.Commit(f.bc, user(f.bob), id); !errors.Is(e, shares.ErrUnauthorized) {
		t.Fatalf("commit: %v", e)
	}
	if e = f.remote.Abort(f.bc, user(f.bob), id); e != nil {
		t.Fatal(e)
	}
	f.local.cleanup.Wait()
	f.local.mu.Lock()
	remaining := len(f.local.pending)
	f.local.mu.Unlock()
	if remaining != 0 {
		t.Fatal("upload still held")
	}
	paths, _ := filepath.Glob(filepath.Join(f.volume, "spaces", "private", "bob", ".a-nas-upload-*"))
	for _, p := range paths {
		st, e := os.Stat(p)
		if e != nil || st.Size() != 0 {
			t.Fatalf("unreleased content: %v %v", st, e)
		}
	}
}
func dropRequest(t *testing.T, f *fixture, req wireRequest, body string) {
	t.Helper()
	c, e := net.DialUnix("unix", nil, &net.UnixAddr{Name: f.socket, Net: "unix"})
	if e != nil {
		t.Fatal(e)
	}
	req.Token = accounts.SessionToken(f.bc)
	req.Generation = f.remote.generation
	if e = writeWire(c, req, nil); e != nil {
		t.Fatal(e)
	}
	if body != "" {
		if _, e = io.WriteString(c, body); e != nil {
			t.Fatal(e)
		}
	}
	c.Close()
}
func awaitStatus(t *testing.T, f *fixture, op, id string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		v, e := f.remote.simple(context.Background(), wireRequest{Op: "status", Name: op, ID: id}, nil)
		if e == nil {
			return v
		}
		if !errors.Is(e, shares.ErrNotFound) {
			t.Fatal(e)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("operation did not settle")
	return ""
}
func TestLostStageAndCommitRepliesHaveQueryableOutcomes(t *testing.T) {
	f := setup(t)
	id := randomID()
	dropRequest(t, f, wireRequest{Op: "stage", ID: id, ShareID: "s", Target: shares.Target{RootID: f.bs}, Entry: shares.Entry{Path: "a.txt", Size: 5}}, "hello")
	if got := awaitStatus(t, f, "stage", id); got != id {
		t.Fatal(got)
	}
	dropRequest(t, f, wireRequest{Op: "commit", ID: id}, "")
	awaitStatus(t, f, "commit", id)
	if e := f.remote.Commit(f.bc, user(f.bob), id); e != nil {
		t.Fatal(e)
	}
	top, e := f.files.List(f.bc, f.bob, f.bs, "")
	if e != nil || len(top) != 1 {
		t.Fatalf("duplicate/lost publication: %+v %v", top, e)
	}
	dirID := randomID()
	dropRequest(t, f, wireRequest{Op: "directory", ID: dirID, ShareID: "s", Target: shares.Target{RootID: f.bs}, Name: "empty"}, "")
	first := awaitStatus(t, f, "directory", dirID)
	again, e := f.remote.reliable(f.bc, wireRequest{Op: "directory", ID: dirID, ShareID: "s", Target: shares.Target{RootID: f.bs}, Name: "empty"})
	if e != nil || again != first {
		t.Fatalf("directory replay %s %v", again, e)
	}
}

type blockedReader struct{ entered, release chan struct{} }

func (b *blockedReader) Read(p []byte) (int, error) {
	select {
	case <-b.entered:
	default:
		close(b.entered)
	}
	<-b.release
	return 0, io.EOF
}
func TestLocalFenceDoesNotWaitForDomainReader(t *testing.T) {
	f := setup(t)
	reader := &blockedReader{make(chan struct{}), make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = f.local.Stage(f.bc, user(f.bob), shares.Target{RootID: f.bs, ShareID: "blocked"}, shares.Entry{Path: "blocked.txt", Size: 1}, reader)
	}()
	<-reader.entered
	fenced := make(chan error, 1)
	go func() { fenced <- f.local.Fence(context.Background(), "blocked") }()
	select {
	case e := <-fenced:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		close(reader.release)
		t.Fatal("fence deadlocks on reader")
	}
	close(reader.release)
	<-done
	f.local.cleanup.Wait()
}
func TestOldGenerationCannotPublishAfterReset(t *testing.T) {
	f := setup(t)
	id, e := f.remote.Stage(f.bc, user(f.bob), shares.Target{RootID: f.bs, ShareID: "s"}, shares.Entry{Path: "a", Size: 1}, strings.NewReader("x"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = NewRemote(context.Background(), f.socket); e != nil {
		t.Fatal(e)
	}
	if e = f.remote.Check(context.Background()); !errors.Is(e, shares.ErrInactive) {
		t.Fatalf("stale bridge health: %v", e)
	}
	if e = f.remote.Commit(f.bc, user(f.bob), id); !errors.Is(e, shares.ErrInactive) {
		t.Fatalf("late commit %v", e)
	}
	f.local.cleanup.Wait()
}

type delayedPublication struct {
	files.FileSystem
	entered, release chan struct{}
	calls            atomic.Int32
}

func (d *delayedPublication) Rename(ctx context.Context, from, to string) error {
	d.calls.Add(1)
	select {
	case <-d.entered:
	default:
		close(d.entered)
	}
	<-d.release
	return d.FileSystem.Rename(ctx, from, to)
}
func TestPublicationTimeoutIsUnknownUntilWorkerSettles(t *testing.T) {
	worker := &delayedPublication{entered: make(chan struct{}), release: make(chan struct{})}
	f := setupFS(t, func(fs files.FileSystem) files.FileSystem { worker.FileSystem = fs; return worker })
	id, e := f.remote.Stage(f.bc, user(f.bob), shares.Target{RootID: f.bs, ShareID: "s"}, shares.Entry{Path: "late.txt", Size: 5}, strings.NewReader("whole"))
	if e != nil {
		t.Fatal(e)
	}
	result := make(chan error, 1)
	go func() {
		_, e := f.remote.reliableWithin(f.bc, wireRequest{Op: "commit", ID: id}, 20*time.Millisecond, 20*time.Millisecond)
		result <- e
	}()
	<-worker.entered
	if e = <-result; !errors.Is(e, shares.ErrPublicationUnknown) {
		close(worker.release)
		t.Fatalf("indeterminate publication reported as certain: %v", e)
	}
	// The timed-out transport does not cancel the filesystem operation or cache
	// a false failure. Its eventual success is queryable with the same handle.
	close(worker.release)
	awaitStatus(t, f, "commit", id)
	if e = f.remote.Commit(f.bc, user(f.bob), id); e != nil {
		t.Fatal(e)
	}
	if worker.calls.Load() != 1 {
		t.Fatalf("publication replayed %d times", worker.calls.Load())
	}
	entries, e := f.files.List(f.bc, f.bob, f.bs, "")
	if e != nil || len(entries) != 1 {
		t.Fatalf("late publication %+v %v", entries, e)
	}
	content, e := f.files.OpenContent(f.bc, f.bob, entries[0].ID)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = content.Reader.Close() }()
	b, e := io.ReadAll(content.Reader)
	if e != nil || string(b) != "whole" {
		t.Fatalf("late file is not complete: %q %v", b, e)
	}
}
