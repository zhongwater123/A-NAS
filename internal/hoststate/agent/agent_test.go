package agent_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hoststate/agent"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
)

func TestClientReadsHostStateOverUnixSocket(t *testing.T) {
	socketPath := serve(t, agent.NewHandler(fake.NewHealthy(), discardLogger()))
	client := agent.NewClient(socketPath)

	state, err := client.Read(context.Background())
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if got, want := state.System.Hostname, "anas-fake"; got != want {
		t.Fatalf("hostname = %q, want %q", got, want)
	}
	if got, want := state.ObservedAt, time.Date(2026, time.October, 6, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("observed at = %s, want %s", got, want)
	}
	if got, want := len(state.Disks), 2; got != want {
		t.Fatalf("disk count = %d, want %d", got, want)
	}
	if got, want := state.Disks[0].ID.String(), "disk:fake-data-01"; got != want {
		t.Fatalf("first disk ID = %q, want %q", got, want)
	}
}

func TestClientReportsUnavailableReader(t *testing.T) {
	socketPath := serve(t, agent.NewHandler(fake.NewUnavailable(), discardLogger()))
	client := agent.NewClient(socketPath)

	if _, err := client.Read(context.Background()); err == nil {
		t.Fatal("Read() error = nil, want unavailable error")
	}
}

func TestClientRejectsMalformedResponse(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{not-json")
	})
	client := agent.NewClient(serve(t, handler))

	if _, err := client.Read(context.Background()); err == nil {
		t.Fatal("Read() error = nil, want malformed response error")
	}
}

func TestClientReportsMissingSocket(t *testing.T) {
	client := agent.NewClient(filepath.Join(t.TempDir(), "missing.sock"))

	if _, err := client.Read(context.Background()); err == nil {
		t.Fatal("Read() error = nil, want connection error")
	}
}

func TestClientHonorsCanceledContext(t *testing.T) {
	client := agent.NewClient(filepath.Join(t.TempDir(), "missing.sock"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := client.Read(ctx); err == nil {
		t.Fatal("Read() error = nil, want context cancellation")
	}
}

func TestClientTimesOutUnresponsiveAgent(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusOK)
	})
	client := agent.NewClient(serve(t, handler))
	started := time.Now()

	if _, err := client.Read(context.Background()); err == nil {
		t.Fatal("Read() error = nil, want timeout error")
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Fatalf("Read() timed out after %s, want no more than 4s", elapsed)
	}
}

func serve(t *testing.T, handler http.Handler) string {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "host-agent.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen on Unix socket: %v", err)
	}
	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
	})
	return socketPath
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
