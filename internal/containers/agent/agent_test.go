package agent_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/containers"
	"github.com/zhongwater123/A-NAS/internal/containers/agent"
	"github.com/zhongwater123/A-NAS/internal/containers/fake"
)

func TestClientRoundTripsSnapshotActionsAndLogs(t *testing.T) {
	client := agent.NewClient(serve(t, agent.NewHandler(fake.New(), discardLogger())))
	ctx := context.Background()

	snapshot, err := client.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if snapshot.Engine.Version != "29.1.3" || len(snapshot.Containers) != 3 || len(snapshot.Images) != 3 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	jellyfin := snapshot.Containers[0]
	if jellyfin.Name != "jellyfin" || jellyfin.Usage == nil || jellyfin.Ports[0].HostPort != 8096 {
		t.Fatalf("first container = %+v", jellyfin)
	}

	if err := client.Act(ctx, jellyfin.ID, containers.ActionStop); err != nil {
		t.Fatalf("Act(stop) error = %v", err)
	}
	snapshot, _ = client.Snapshot(ctx)
	if snapshot.Containers[0].State != containers.StateExited || snapshot.Containers[0].Usage != nil {
		t.Fatalf("after stop = %+v", snapshot.Containers[0])
	}

	lines, err := client.Logs(ctx, jellyfin.ID, 2)
	if err != nil {
		t.Fatalf("Logs() error = %v", err)
	}
	if len(lines) != 2 || lines[1].Stream != "stderr" || lines[1].Time.IsZero() {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestClientMapsAgentErrorsToDomainErrors(t *testing.T) {
	client := agent.NewClient(serve(t, agent.NewHandler(fake.New(), discardLogger())))
	missing := strings.Repeat("0", 64)

	if err := client.Act(context.Background(), missing, containers.ActionStart); !errors.Is(err, containers.ErrNotFound) {
		t.Fatalf("Act(missing) error = %v, want ErrNotFound", err)
	}
	if err := client.Act(context.Background(), "jellyfin", containers.ActionStart); !errors.Is(err, containers.ErrInvalidID) {
		t.Fatalf("Act(name) error = %v, want ErrInvalidID", err)
	}

	offline := agent.NewClient(serve(t, agent.NewHandler(fake.NewUnavailable(), discardLogger())))
	if _, err := offline.Snapshot(context.Background()); !errors.Is(err, containers.ErrUnavailable) {
		t.Fatalf("Snapshot(offline) error = %v, want ErrUnavailable", err)
	}
	if _, err := agent.NewClient(filepath.Join(t.TempDir(), "missing.sock")).Snapshot(context.Background()); !errors.Is(err, containers.ErrUnavailable) {
		t.Fatalf("Snapshot(no socket) error = %v, want ErrUnavailable", err)
	}
}

func TestHandlerOnlyExposesTypedOperations(t *testing.T) {
	socket := serve(t, agent.NewHandler(fake.New(), discardLogger()))
	httpClient := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	id := fake.ID("jellyfin")
	tests := []struct {
		method, path string
		want         int
	}{
		{http.MethodPost, "/v1/containers/" + id + "/remove", http.StatusNotFound},
		{http.MethodPost, "/v1/containers/" + id + "/exec", http.StatusNotFound},
		{http.MethodGet, "/v1/containers/" + id + "/start", http.StatusMethodNotAllowed},
		{http.MethodPost, "/v1/containers/" + id + "/logs", http.StatusMethodNotAllowed},
		{http.MethodGet, "/v1/containers/json", http.StatusNotFound},
		{http.MethodPost, "/v1/containers/" + id + "/start/extra", http.StatusNotFound},
	}
	for _, test := range tests {
		request, _ := http.NewRequest(test.method, "http://container-agent"+test.path, nil)
		response, err := httpClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if response.StatusCode != test.want {
			t.Fatalf("%s %s = %d, want %d", test.method, test.path, response.StatusCode, test.want)
		}
	}
}

func serve(t *testing.T, handler http.Handler) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "agent.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return socket
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
