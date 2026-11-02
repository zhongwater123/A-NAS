// Package vectorindextest runs the pinned Qdrant server for tests, the way a
// vector index instance runs on the NAS: with deploy/qdrant/config.yaml,
// reached through a Unix socket that is forwarded to the instance's gRPC port
// as systemd-socket-proxyd forwards it there (docs/architecture/vector-index.md).
//
// The binary is ANAS_QDRANT_BINARY, or the one scripts/fetch-qdrant.sh keeps
// under build/. Without it a test is skipped, unless ANAS_QDRANT_REQUIRED=1,
// as in CI, makes that a failure.
package vectorindextest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/procfs"
)

// startTimeout bounds how long the server may take to listen on its ports.
const startTimeout = 60 * time.Second

// Instance is a running server.
type Instance struct {
	// Socket is the Unix socket to give vectorindex.New.
	Socket string
	// Storage is the server's storage directory.
	Storage string
	// Connections counts the connections open through Socket, as the proxy
	// on the NAS would see them: while any is open it does not exit.
	Connections func() int
}

// Start runs a server of its own for the test and stops it when the test
// ends.
func Start(t testing.TB) Instance {
	t.Helper()
	root := RepositoryRoot(t)
	binary := os.Getenv("ANAS_QDRANT_BINARY")
	if binary == "" {
		binary = fetchedBinary(t, root)
	}
	//nolint:gosec // G703: the pinned binary, fetched by hash or named by the developer for tests.
	if _, err := os.Stat(binary); err != nil {
		if os.Getenv("ANAS_QDRANT_REQUIRED") == "1" {
			t.Fatalf("Qdrant is required but missing (%v); run scripts/fetch-qdrant.sh or set ANAS_QDRANT_BINARY", err)
		}
		t.Skipf("Qdrant is missing (%v); run scripts/fetch-qdrant.sh or set ANAS_QDRANT_BINARY", err)
	}

	for attempt := 1; ; attempt++ {
		instance, err := start(t, binary, root)
		if err == nil {
			return instance
		}
		if !errors.Is(err, errPortTaken) || attempt == startAttempts {
			t.Fatal(err)
		}
		t.Logf("starting Qdrant again on other ports: %v", err)
	}
}

// startAttempts bounds how often Start picks new ports for the server.
const startAttempts = 5

// errPortTaken means that another process listened on a port picked for the
// server before the server could. freePort releases a port before the server
// binds it, and tests running in parallel open ports all the time.
var errPortTaken = errors.New("another process took a port picked for Qdrant")

// pickPort picks a port for the server; a test replaces it.
var pickPort = freePort

// start runs a server on newly picked ports and waits until it listens on
// both. Reaching a port does not show that: the server only logs an error
// when another process took its gRPC port, and runs on without it, so a test
// would talk to whatever took the port, another test's server included.
// Only the server's own listening sockets count.
func start(t testing.TB, binary, root string) (Instance, error) {
	t.Helper()
	work := t.TempDir()
	httpPort := pickPort(t)
	grpcPort := pickPort(t)
	for grpcPort == httpPort {
		grpcPort = pickPort(t)
	}
	//nolint:gosec // G702: the pinned binary, fetched by hash or named by the developer for tests.
	command := exec.Command(binary, "--config-path", filepath.Join(root, "deploy", "qdrant", "config.yaml"), "--disable-telemetry")
	command.Dir = work
	command.Env = append(os.Environ(),
		"QDRANT__STORAGE__STORAGE_PATH="+filepath.Join(work, "storage"),
		"QDRANT__STORAGE__SNAPSHOTS_PATH="+filepath.Join(work, "snapshots"),
		"QDRANT__STORAGE__TEMP_PATH="+filepath.Join(work, "tmp"),
		"QDRANT_INIT_FILE_PATH="+filepath.Join(work, ".qdrant-initialized"),
		"QDRANT__SERVICE__HTTP_PORT="+strconv.Itoa(httpPort),
		"QDRANT__SERVICE__GRPC_PORT="+strconv.Itoa(grpcPort),
	)
	output := &lockedBuffer{}
	command.Stdout, command.Stderr = output, output
	if err := command.Start(); err != nil {
		return Instance{}, err
	}
	exited := make(chan struct{})
	go func() {
		_ = command.Wait()
		close(exited)
	}()
	stop := func() {
		_ = command.Process.Kill()
		<-exited
	}

	ended := func() error {
		return fmt.Errorf("%w: the server ended before listening:\n%s", errPortTaken, output)
	}
	deadline := time.Now().Add(startTimeout)
	for {
		select {
		case <-exited:
			// Binding a taken HTTP port ends the server.
			return Instance{}, ended()
		default:
		}
		ownsHTTP, err := listensOn(command.Process.Pid, httpPort)
		if err == nil && ownsHTTP {
			var ownsGRPC bool
			ownsGRPC, err = listensOn(command.Process.Pid, grpcPort)
			if err == nil && ownsGRPC {
				break
			}
		}
		if err != nil {
			// The process can end while its sockets are being read.
			select {
			case <-exited:
				return Instance{}, ended()
			default:
			}
			stop()
			return Instance{}, err
		}
		// A taken gRPC port only shows in the log; waiting would last until
		// the deadline.
		if strings.Contains(output.String(), "Error while starting") {
			stop()
			return Instance{}, fmt.Errorf("%w:\n%s", errPortTaken, output)
		}
		if time.Now().After(deadline) {
			stop()
			return Instance{}, fmt.Errorf("the server did not listen on its ports within %s:\n%s", startTimeout, output)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Cleanup(stop)
	socket, connections := forward(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(grpcPort)))
	return Instance{Socket: socket, Storage: filepath.Join(work, "storage"), Connections: connections}, nil
}

// tcpListen is the state of a listening socket in /proc/net/tcp.
const tcpListen = 0x0A

// listensOn reports whether process pid holds the socket listening on
// 127.0.0.1:port.
func listensOn(pid, port int) (bool, error) {
	fs, err := procfs.NewDefaultFS()
	if err != nil {
		return false, err
	}
	//nolint:staticcheck // SA1019: /proc/net/tcp is all this needs; netlink would add a dependency for a test helper.
	sockets, err := fs.NetTCP()
	if err != nil {
		return false, err
	}
	proc, err := fs.Proc(pid)
	if err != nil {
		return false, err
	}
	targets, err := proc.FileDescriptorTargets()
	if err != nil {
		return false, err
	}
	for _, socket := range sockets {
		if socket.St == tcpListen && socket.LocalPort == uint64(port) && socket.LocalAddr.Equal(net.IPv4(127, 0, 0, 1)) &&
			slices.Contains(targets, fmt.Sprintf("socket:[%d]", socket.Inode)) {
			return true, nil
		}
	}
	return false, nil
}

// lockedBuffer collects the server's output while it is read.
type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

// forward listens on a Unix socket until the test ends and connects each
// connection to address. It returns the socket and a count of the
// connections open through it.
func forward(t testing.TB, address string) (string, func() int) {
	t.Helper()
	// Unix socket paths are short; t.TempDir can exceed the limit.
	dir, err := os.MkdirTemp("", "qd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "index.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	// mu guards closed, open and starting a pair's goroutine, so that the
	// cleanup either sees a pair to close and wait for, or the pair sees the
	// forwarder closed and closes itself.
	var connections sync.WaitGroup
	var mu sync.Mutex
	closed := false
	open := map[net.Conn]bool{}
	t.Cleanup(func() {
		_ = listener.Close()
		mu.Lock()
		closed = true
		for conn := range open {
			_ = conn.Close()
		}
		mu.Unlock()
		connections.Wait()
	})
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			server, err := net.Dial("tcp", address)
			if err != nil {
				_ = client.Close()
				continue
			}
			mu.Lock()
			if closed {
				mu.Unlock()
				_ = client.Close()
				_ = server.Close()
				return
			}
			open[client], open[server] = true, true
			connections.Go(func() {
				pipe(client, server)
				mu.Lock()
				delete(open, client)
				delete(open, server)
				mu.Unlock()
			})
			mu.Unlock()
		}
	}()
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(open) / 2
	}
	return socket, count
}

// pipe copies both ways until either side ends, then closes both.
func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	copyTo := func(to, from net.Conn) {
		_, _ = io.Copy(to, from)
		done <- struct{}{}
	}
	go copyTo(a, b)
	go copyTo(b, a)
	<-done
	_ = a.Close()
	_ = b.Close()
	<-done
}

func freePort(t testing.TB) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().(*net.TCPAddr).Port
}

// RepositoryRoot is the module root, found from this file, for tests that
// read pinned files.
func RepositoryRoot(t testing.TB) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the vectorindextest package")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..")
}

// fetchedBinary is where scripts/fetch-qdrant.sh keeps the pinned binary.
func fetchedBinary(t testing.TB, root string) string {
	t.Helper()
	manifest, err := Manifest(root)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "build", "qdrant", manifest.SHA256, "qdrant")
}

// PinnedRelease is what deploy/qdrant/qdrant.json pins.
type PinnedRelease struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

// Manifest reads the pinned release from the repository at root.
func Manifest(root string) (PinnedRelease, error) {
	data, err := os.ReadFile(filepath.Join(root, "deploy", "qdrant", "qdrant.json"))
	if err != nil {
		return PinnedRelease{}, err
	}
	var manifest PinnedRelease
	if err := json.Unmarshal(data, &manifest); err != nil {
		return PinnedRelease{}, err
	}
	if manifest.Version == "" || len(manifest.SHA256) != 64 {
		return PinnedRelease{}, errors.New("deploy/qdrant/qdrant.json lacks a version or SHA-256")
	}
	return manifest, nil
}
