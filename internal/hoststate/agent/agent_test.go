package agent_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/hoststate"
	"github.com/zhongwater123/A-NAS/internal/hoststate/agent"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/storage"
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
	if got, want := state.Disks[0].SMARTStatus, hoststate.HealthHealthy; got != want {
		t.Fatalf("data disk SMART = %q, want %q", got, want)
	}
	if got, want := state.Disks[1].Filesystems, []string{"ext4"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("system filesystems = %#v, want %#v", got, want)
	}
	if !state.Disks[1].InUse {
		t.Fatal("system disk InUse = false, want true")
	}
}

func TestClientUsesTypedVolumeAndCredentialOperationsOverUnixSocket(t *testing.T) {
	volume := &agentVolumeExecutor{}
	credentials := &agentCredentialProvisioner{}
	snapshots := &agentSnapshotBackend{}
	handler := agent.NewOperationsHandler(agent.Services{
		Reader: fake.NewHealthy(), Volume: volume, Credentials: credentials, Snapshots: snapshots,
	}, discardLogger())
	client := agent.NewClient(serve(t, handler))

	created, err := client.CreateVolume(context.Background(), storage.CreateVolumeRequest{
		PlanID: "plan:1", DiskID: "disk:fake-data-01", Fingerprint: "fingerprint",
	})
	if err != nil {
		t.Fatalf("CreateVolume() error = %v", err)
	}
	if created.DiskID != "disk:fake-data-01" || volume.request.PlanID != "plan:1" {
		t.Fatalf("created volume/request = %#v / %#v", created, volume.request)
	}
	credential := accounts.CredentialRequest{
		UserID: "user:alice", PrivateSpaceID: "space:alice", Username: "alice",
		Password: "alice password for testing", Role: accounts.RoleMember,
	}
	if err := client.SetCredential(context.Background(), credential); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}
	if credentials.request.Username != "alice" || credentials.request.Password != credential.Password {
		t.Fatalf("credential request = %#v", credentials.request)
	}
	objects, err := client.Create(context.Background(), "space:alice", "snapshot:1")
	if err != nil || len(objects) != 1 || objects[0].Name != "photo.jpg" {
		t.Fatalf("Create snapshot objects = %#v, err=%v", objects, err)
	}
	reader, object, err := client.Open(context.Background(), "snapshot:1", objects[0].Key)
	if err != nil {
		t.Fatalf("Open snapshot object: %v", err)
	}
	contents, _ := io.ReadAll(reader)
	_ = reader.Close()
	if string(contents) != "snapshot bytes" || object.Kind != files.EntryKindFile {
		t.Fatalf("snapshot content/object = %q / %#v", contents, object)
	}
	if err := client.Delete(context.Background(), "space:alice", "snapshot:1"); err != nil {
		t.Fatalf("Delete snapshot: %v", err)
	}
}

type agentVolumeExecutor struct{ request storage.CreateVolumeRequest }

func (e *agentVolumeExecutor) CreateVolume(_ context.Context, request storage.CreateVolumeRequest) (storage.Volume, error) {
	e.request = request
	return storage.Volume{ID: "volume:data", DiskID: request.DiskID, State: storage.VolumeStateAvailable}, nil
}

type agentCredentialProvisioner struct{ request accounts.CredentialRequest }

func (p *agentCredentialProvisioner) SetCredential(_ context.Context, request accounts.CredentialRequest) error {
	p.request = request
	return nil
}

func (*agentCredentialProvisioner) DisableCredential(context.Context, string) error { return nil }

type agentSnapshotBackend struct{}

func (*agentSnapshotBackend) Create(context.Context, string, string) ([]files.SnapshotObject, error) {
	return []files.SnapshotObject{{Key: "photo.jpg", Name: "photo.jpg", Kind: files.EntryKindFile, SizeBytes: 14}}, nil
}

func (*agentSnapshotBackend) Open(context.Context, string, string) (io.ReadCloser, files.SnapshotObject, error) {
	return io.NopCloser(strings.NewReader("snapshot bytes")), files.SnapshotObject{Key: "photo.jpg", Name: "photo.jpg", Kind: files.EntryKindFile, SizeBytes: 14}, nil
}

func (*agentSnapshotBackend) Delete(context.Context, string, string) error { return nil }

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
