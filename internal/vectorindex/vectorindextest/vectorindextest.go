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
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"
)

// startTimeout bounds how long the server may take to accept connections.
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
	if _, err := os.Stat(binary); err != nil {
		if os.Getenv("ANAS_QDRANT_REQUIRED") == "1" {
			t.Fatalf("Qdrant is required but missing (%v); run scripts/fetch-qdrant.sh or set ANAS_QDRANT_BINARY", err)
		}
		t.Skipf("Qdrant is missing (%v); run scripts/fetch-qdrant.sh or set ANAS_QDRANT_BINARY", err)
	}

	work := t.TempDir()
	httpPort, grpcPort := freePort(t), freePort(t)
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
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		_ = command.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = command.Process.Kill()
		<-exited
	})

	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(grpcPort))
	deadline := time.Now().Add(startTimeout)
	for {
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err == nil {
			_ = conn.Close()
			break
		}
		select {
		case <-exited:
			t.Fatalf("Qdrant exited before accepting connections:\n%s", output.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("Qdrant did not accept connections within %s:\n%s", startTimeout, output.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	socket, connections := forward(t, address)
	return Instance{Socket: socket, Storage: filepath.Join(work, "storage"), Connections: connections}
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
	var connections sync.WaitGroup
	var mu sync.Mutex
	open := map[net.Conn]bool{}
	track := func(conn net.Conn, add bool) {
		mu.Lock()
		defer mu.Unlock()
		if add {
			open[conn] = true
		} else {
			delete(open, conn)
		}
	}
	t.Cleanup(func() {
		_ = listener.Close()
		mu.Lock()
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
			track(client, true)
			track(server, true)
			connections.Go(func() {
				pipe(client, server)
				track(client, false)
				track(server, false)
			})
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
