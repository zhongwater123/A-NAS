package filebroker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
)

func TestMain(m *testing.M) {
	// The test binary doubles as the worker the broker spawns.
	if len(os.Args) >= 3 && os.Args[1] == WorkerArgument {
		if err := runTestWorker(os.Args[2], os.Args[3:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) >= 4 && os.Args[1] == ShellGateArgument {
		if err := RunShellGate(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if len(os.Args) == 2 && os.Args[1] == ProbeArgument {
		if err := RunProbe(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestClientRunsFileOperationsThroughAWorker(t *testing.T) {
	harness := newHarness(t)
	ctx := accounts.WithSessionToken(context.Background(), "alice-token")
	fsys := harness.client
	space := filepath.Join(harness.volume, "spaces", "alice")
	if err := os.MkdirAll(space, 0o770); err != nil {
		t.Fatal(err)
	}

	if err := fsys.Mkdir(ctx, filepath.Join(space, "docs")); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	file, temporary, err := fsys.CreateTemp(ctx, filepath.Join(space, "docs"), ".a-nas-upload-")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	if _, err := file.WriteString("written through a passed descriptor"); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	target := filepath.Join(space, "docs", "note.txt")
	if err := fsys.Rename(ctx, temporary, target); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	reader, err := fsys.Open(ctx, target)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	contents, _ := io.ReadAll(reader)
	_ = reader.Close()
	if string(contents) != "written through a passed descriptor" {
		t.Fatalf("read back %q", contents)
	}
	info, err := fsys.Lstat(ctx, target)
	if err != nil || info.Kind != files.KindFile || info.Size != int64(len(contents)) || info.Inode == 0 {
		t.Fatalf("Lstat() = %#v, %v", info, err)
	}
	saved := time.Date(2026, 9, 24, 10, 0, 0, 123456789, time.UTC)
	if err := fsys.SetModTime(ctx, target, saved); err != nil {
		t.Fatalf("SetModTime() error = %v", err)
	}
	if info, err := fsys.Lstat(ctx, target); err != nil || !info.ModTime.Equal(saved) {
		t.Fatalf("modification time after SetModTime() = %v, %v; want %v", info.ModTime, err, saved)
	}
	if err := fsys.SetModTime(ctx, filepath.Join(space, "docs"), saved); !errors.Is(err, files.ErrUnsupportedType) {
		t.Fatalf("SetModTime(folder) error = %v, want ErrUnsupportedType", err)
	}
	if err := fsys.SetModTime(ctx, "/etc/passwd", saved); !errors.Is(err, files.ErrForbidden) {
		t.Fatalf("SetModTime outside the volume error = %v, want ErrForbidden", err)
	}
	if err := fsys.Copy(ctx, filepath.Join(space, "docs"), filepath.Join(space, "copy")); err != nil {
		t.Fatalf("Copy() error = %v", err)
	}
	if size, err := fsys.Usage(ctx, space); err != nil || size != 2*int64(len(contents)) {
		t.Fatalf("Usage() = %d, %v", size, err)
	}
	entries, err := fsys.Scan(ctx, space, files.ScanSkip{Names: []string{"copy"}})
	if err != nil || len(entries) != 2 || entries[0].Path != "docs" || entries[1].Path != "docs/note.txt" {
		t.Fatalf("Scan() = %#v, %v", entries, err)
	}
	if err := fsys.Rename(ctx, filepath.Join(space, "copy"), filepath.Join(space, "docs")); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("Rename() onto an existing target error = %v, want fs.ErrExist", err)
	}
	if _, err := fsys.Lstat(ctx, filepath.Join(space, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Lstat(missing) error = %v, want fs.ErrNotExist", err)
	}
	if _, err := fsys.Open(ctx, "/etc/passwd"); !errors.Is(err, files.ErrForbidden) {
		t.Fatalf("Open outside the volume error = %v, want ErrForbidden", err)
	}
	if err := fsys.RemoveAll(ctx, filepath.Join(space, "copy")); err != nil {
		t.Fatalf("RemoveAll() error = %v", err)
	}
	if got := harness.spawned.Load(); got != 1 {
		t.Fatalf("spawned %d workers, want one reused worker", got)
	}
}

func TestBrokerRefusesUnverifiedCallers(t *testing.T) {
	harness := newHarness(t)
	space := filepath.Join(harness.volume, "spaces")

	if err := harness.client.MkdirAll(context.Background(), space); !errors.Is(err, files.ErrForbidden) {
		t.Fatalf("call without a session error = %v, want ErrForbidden", err)
	}
	forged := accounts.WithSessionToken(context.Background(), "forged-token")
	if err := harness.client.MkdirAll(forged, space); !errors.Is(err, files.ErrForbidden) {
		t.Fatalf("forged token error = %v, want ErrForbidden", err)
	}
	system := accounts.WithSessionToken(context.Background(), "system-token")
	if err := harness.client.MkdirAll(system, space); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("out-of-range UID error = %v, want ErrUnavailable", err)
	}
	impostor := accounts.WithSessionToken(context.Background(), "impostor-token")
	if err := harness.client.MkdirAll(impostor, space); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unprovisioned account error = %v, want ErrUnavailable", err)
	}
	harness.ready.Store(false)
	alice := accounts.WithSessionToken(context.Background(), "alice-token")
	if err := harness.client.MkdirAll(alice, space); !errors.Is(err, files.ErrVolumeUnavailable) {
		t.Fatalf("offline volume error = %v, want ErrVolumeUnavailable", err)
	}
	if got := harness.spawned.Load(); got != 0 {
		t.Fatalf("spawned %d workers for refused callers", got)
	}
	if _, err := os.Stat(space); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a refused call created %s", space)
	}
}

// A session directory that cannot be read right now says nothing about the
// caller: file operations are unavailable, not refused (#166).
func TestFileOperationsAreUnavailableWhileSessionsCannotBeRead(t *testing.T) {
	server, err := NewServer(Config{
		Sessions: resolverFunc(func(string) (accounts.Identity, error) {
			return accounts.Identity{}, errors.New("read session directory: database is locked")
		}),
		VolumeRoot: t.TempDir(), VolumeReady: func() bool { return true },
		Command:         func(volumeRoot string) *exec.Cmd { return exec.Command(os.Args[0], WorkerArgument, volumeRoot) },
		skipCredentials: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "broker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = listener.Close()
		server.Close()
	})
	err = NewClient(socket).Mkdir(accounts.WithSessionToken(context.Background(), "alice-token"), filepath.Join(t.TempDir(), "docs"))
	if !errors.Is(err, ErrUnavailable) || errors.Is(err, files.ErrForbidden) {
		t.Fatalf("Mkdir() error = %v, want ErrUnavailable and not a refusal", err)
	}
}

// Each file worker runs in a scope of its own, apart from the Host Agent's
// cgroup (#166).
func TestWorkersArePlacedInScopesOfTheirOwn(t *testing.T) {
	harness := newHarness(t)
	placer := &recordingPlacer{marker: filepath.Join(t.TempDir(), "placed")}
	harness.server.config.Scopes = placer
	space := filepath.Join(harness.volume, "spaces", "alice")
	if err := os.MkdirAll(space, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := harness.client.Mkdir(accounts.WithSessionToken(context.Background(), "alice-token"), filepath.Join(space, "docs")); err != nil {
		t.Fatal(err)
	}
	if placed := placer.calls(); len(placed) != 1 || !strings.HasPrefix(placed[0], "files:alice:") {
		t.Fatalf("placed = %v, want alice's worker once", placed)
	}
}

func TestIdleWorkersAreStoppedAndRespawned(t *testing.T) {
	harness := newHarness(t)
	ctx := accounts.WithSessionToken(context.Background(), "alice-token")
	if err := harness.client.MkdirAll(ctx, filepath.Join(harness.volume, "a")); err != nil {
		t.Fatal(err)
	}
	harness.server.reapIdle(time.Now().Add(time.Hour))
	if remaining, _ := workerCounts(harness.server); remaining != 0 {
		t.Fatalf("%d idle workers survived reaping", remaining)
	}
	if err := harness.client.MkdirAll(ctx, filepath.Join(harness.volume, "b")); err != nil {
		t.Fatalf("call after reaping error = %v", err)
	}
	if got := harness.spawned.Load(); got != 2 {
		t.Fatalf("spawned %d workers, want a fresh worker after reaping", got)
	}
}

func TestFileServiceWorksThroughTheBroker(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	accountStore, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = accountStore.Close() })
	accountService := accounts.NewService(accountStore, acceptingCredentials{}, accounts.Options{})
	owner, err := accountService.SetupAdministrator(ctx, "alice", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	spaces, err := accountService.ListSpaces(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	var private accounts.Space
	for _, space := range spaces {
		if space.Kind == accounts.SpaceKindPrivate {
			private = space
		}
	}
	catalog, err := files.OpenSQLite(filepath.Join(t.TempDir(), "files.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	service := files.NewService(catalog, harness.volume, accountService, harness.client, files.Options{
		DisableCapacityReserve: true, AllowUnverifiedVolume: true})
	userCtx := accounts.WithSessionToken(ctx, "alice-token")
	// The Host Agent creates spaces; workers never create them.
	if err := os.MkdirAll(filepath.Join(harness.volume, "spaces", "private", "alice"), 0o770); err != nil {
		t.Fatal(err)
	}

	uploaded, err := service.Upload(userCtx, owner, private.ID, "", "report.txt", strings.NewReader("through the broker"))
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	content, err := service.OpenContent(userCtx, owner, uploaded.ID)
	if err != nil {
		t.Fatalf("OpenContent() error = %v", err)
	}
	data, _ := io.ReadAll(content.Reader)
	_ = content.Reader.Close()
	if string(data) != "through the broker" {
		t.Fatalf("downloaded %q", data)
	}
	trashed, err := service.Delete(userCtx, owner, private.ID, uploaded.ID)
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := service.Restore(userCtx, owner, trashed.ID, "", "", files.ConflictAsk); err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if _, err := service.List(ctx, owner, private.ID, ""); !errors.Is(err, files.ErrForbidden) {
		t.Fatalf("List() without a session error = %v, want ErrForbidden", err)
	}
}

type harness struct {
	volume  string
	server  *Server
	client  *Client
	ready   atomic.Bool
	spawned atomic.Int32
}

// newHarness runs a broker whose workers get options, see runTestWorker.
func newHarness(t *testing.T, options ...string) *harness {
	t.Helper()
	h := &harness{volume: filepath.Join(t.TempDir(), "volume")}
	if err := os.MkdirAll(h.volume, 0o770); err != nil {
		t.Fatal(err)
	}
	h.ready.Store(true)
	identities := map[string]accounts.Identity{
		"alice-token":    {Username: "alice", UID: 20101, Role: accounts.RoleMember, Enabled: true},
		"system-token":   {Username: "daemon", UID: 1, Role: accounts.RoleMember, Enabled: true},
		"impostor-token": {Username: "mallory", UID: 20102, Role: accounts.RoleMember, Enabled: true},
	}
	server, err := NewServer(Config{
		Sessions:    resolverFunc(func(token string) (accounts.Identity, error) { return lookup(identities, token) }),
		VolumeRoot:  h.volume,
		VolumeReady: h.ready.Load,
		Command: func(volumeRoot string) *exec.Cmd {
			h.spawned.Add(1)
			return exec.Command(os.Args[0], append([]string{WorkerArgument, volumeRoot}, options...)...)
		},
		LookupUID: func(username string) (int, error) {
			if username == "alice" {
				return 20101, nil
			}
			return 0, errors.New("unknown user")
		},
		skipCredentials: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "broker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = listener.Close()
		server.Close()
	})
	h.server = server
	h.client = NewClient(socket)
	return h
}

type resolverFunc func(string) (accounts.Identity, error)

func (f resolverFunc) ResolveSessionIdentity(_ context.Context, token string) (accounts.Identity, error) {
	return f(token)
}

func lookup(identities map[string]accounts.Identity, token string) (accounts.Identity, error) {
	identity, ok := identities[token]
	if !ok {
		return accounts.Identity{}, accounts.ErrSessionNotFound
	}
	return identity, nil
}

type acceptingCredentials struct{}

func (acceptingCredentials) SetCredential(context.Context, accounts.CredentialRequest) error {
	return nil
}
func (acceptingCredentials) DisableCredential(context.Context, string) error { return nil }

// Without CAP_SETUID and CAP_SETGID, as for a non-root test run or a Host
// Agent whose sandbox took them (issue #38), the startup probe must fail.
// The root integration test covers the probe succeeding.
func TestIdentitySwitchProbeFailsWithoutTheCapabilities(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can switch identities")
	}
	server, err := NewServer(Config{
		Sessions:     resolverFunc(func(string) (accounts.Identity, error) { return accounts.Identity{}, accounts.ErrSessionNotFound }),
		VolumeRoot:   t.TempDir(),
		VolumeReady:  func() bool { return true },
		ProbeCommand: func() *exec.Cmd { return exec.Command(os.Args[0], ProbeArgument) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.VerifyIdentitySwitch(); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("VerifyIdentitySwitch() error = %v, want EPERM", err)
	}
}

// Only the worker sees the real mount, so a read-only volume reaches the
// Product Service as unavailable rather than as a generic failure.
func TestReadOnlyVolumeIsReportedAsUnavailable(t *testing.T) {
	err := encodeError(&fs.PathError{Op: "open", Path: "spaces/shared/a.txt", Err: syscall.EROFS})
	if !errors.Is(err, files.ErrVolumeUnavailable) {
		t.Fatalf("encodeError(EROFS) = %v, want files.ErrVolumeUnavailable", err)
	}
}
