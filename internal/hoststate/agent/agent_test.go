package agent_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path"
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

	started, err := client.StartCreateVolume(context.Background(), storage.CreateVolumeRequest{
		PlanID: "plan:1", Disks: []storage.DiskTarget{{DiskID: "disk:fake-data-01", Fingerprint: "fingerprint"}}, Layout: storage.LayoutSingle,
	})
	if err != nil {
		t.Fatalf("StartCreateVolume() error = %v", err)
	}
	if started.State != storage.OperationRunning || started.Step != storage.StepFormat || volume.request.PlanID != "plan:1" || volume.request.Layout != storage.LayoutSingle {
		t.Fatalf("started operation/request = %#v / %#v", started, volume.request)
	}
	created, err := client.VolumeOperation(context.Background(), "plan:1")
	if err != nil || created.State != storage.OperationSucceeded || created.Volume == nil || len(created.Volume.DiskIDs) != 1 || created.Volume.DiskIDs[0] != "disk:fake-data-01" {
		t.Fatalf("VolumeOperation() = %#v, %v; want the created volume", created, err)
	}
	if _, err := client.VolumeOperation(context.Background(), "plan:unknown"); !errors.Is(err, storage.ErrOperationNotFound) {
		t.Fatalf("VolumeOperation() of an unknown plan error = %v, want ErrOperationNotFound", err)
	}
	if _, err := client.StartAddDisks(context.Background(), storage.AddDisksRequest{
		PlanID: "plan:2", FilesystemUUID: "uuid", Disks: []storage.DiskTarget{{DiskID: "disk:fake-data-02", Fingerprint: "fingerprint"}}, Layout: storage.LayoutMirror,
	}); err != nil || volume.added.FilesystemUUID != "uuid" || volume.added.Disks[0].DiskID != "disk:fake-data-02" || volume.added.Layout != storage.LayoutMirror {
		t.Fatalf("StartAddDisks() error = %v, request = %#v", err, volume.added)
	}
	if _, err := client.StartAddDisks(context.Background(), storage.AddDisksRequest{PlanID: "plan:busy"}); !errors.Is(err, storage.ErrPlanState) {
		t.Fatalf("StartAddDisks() while another operation runs error = %v, want ErrPlanState", err)
	}
	if err := client.ContinueConversion(context.Background()); !errors.Is(err, storage.ErrNoConversion) {
		t.Fatalf("ContinueConversion() with nothing to convert error = %v, want ErrNoConversion", err)
	}
	credential := accounts.CredentialRequest{
		UserID: "user:alice", PrivateSpaceID: "space:alice", Username: "alice",
		Password: "alice password for testing", Role: accounts.RoleMember, UID: 20101, Enabled: true,
	}
	if err := client.SetCredential(context.Background(), credential); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}
	if credentials.request != credential {
		t.Fatalf("credential request = %#v, want %#v", credentials.request, credential)
	}
	if err := client.Create(context.Background(), "space:alice", "snapshot:1"); err != nil ||
		!reflect.DeepEqual(snapshots.created, []string{"space:alice/snapshot:1"}) {
		t.Fatalf("Create snapshot error = %v, created %v", err, snapshots.created)
	}
	reader, object, err := client.Open(context.Background(), "snapshot:1", "photo.jpg")
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

// A listing is streamed, so a space far beyond what one JSON reply could
// carry (issue #162: 1 MiB, about 6,000 entries) arrives whole and in order.
func TestClientStreamsLargeSnapshotListings(t *testing.T) {
	snapshots := &agentSnapshotBackend{objects: 100_000}
	client := agent.NewClient(serve(t, agent.NewOperationsHandler(agent.Services{Snapshots: snapshots}, discardLogger())))
	received := 0
	err := client.Objects(context.Background(), "snapshot:1", func(object files.SnapshotObject) error {
		if want := listedObject(received); object != want {
			return fmt.Errorf("object %d = %#v, want %#v", received, object, want)
		}
		received++
		return nil
	})
	if err != nil || received != 100_000 {
		t.Fatalf("Objects() error = %v after %d objects, want all 100000", err, received)
	}
	if err := client.Objects(context.Background(), "snapshot:missing", func(files.SnapshotObject) error { return nil }); !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("Objects() of a missing snapshot error = %v, want ErrNotFound", err)
	}
}

func TestClientRefusesASnapshotListingCutShort(t *testing.T) {
	snapshots := &agentSnapshotBackend{objects: 10_000, failAt: 5_000}
	client := agent.NewClient(serve(t, agent.NewOperationsHandler(agent.Services{Snapshots: snapshots}, discardLogger())))
	received := 0
	err := client.Objects(context.Background(), "snapshot:1", func(files.SnapshotObject) error { received++; return nil })
	if err == nil || received != 5_000 {
		t.Fatalf("Objects() of a failing walk error = %v after %d objects, want an error after 5000", err, received)
	}

	// A listing that stops without its end line, as when the Host Agent dies.
	cut := agent.NewClient(serve(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, `{"object":{"key":"a","name":"a","kind":"file","sizeBytes":1}}`+"\n")
	})))
	received = 0
	err = cut.Objects(context.Background(), "snapshot:1", func(files.SnapshotObject) error { received++; return nil })
	if err == nil || received != 1 {
		t.Fatalf("Objects() of a cut-off listing error = %v, want an error", err)
	}

	// A count that disagrees with the lines sent.
	miscounted := agent.NewClient(serve(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"object":{"key":"a","name":"a","kind":"file","sizeBytes":1}}`+"\n"+`{"end":{"count":2}}`+"\n")
	})))
	if err := miscounted.Objects(context.Background(), "snapshot:1", func(files.SnapshotObject) error { return nil }); err == nil {
		t.Fatal("Objects() accepted a listing whose count disagrees with its lines")
	}
}

func TestClientDeletesAndPrunesSnapshots(t *testing.T) {
	snapshots := &agentSnapshotBackend{
		deleteErr: files.ErrNotFound,
		pruned:    files.SnapshotPrune{Present: []string{"snapshot:1"}, Removed: 3},
	}
	client := agent.NewClient(serve(t, agent.NewOperationsHandler(agent.Services{Snapshots: snapshots}, discardLogger())))
	if err := client.Delete(context.Background(), "space:gone", "snapshot:1"); !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("Delete() in an unknown space error = %v, want ErrNotFound", err)
	}
	pruned, err := client.Prune(context.Background(), []string{"snapshot:1", "snapshot:2"})
	if err != nil || !reflect.DeepEqual(pruned, snapshots.pruned) || !reflect.DeepEqual(snapshots.kept, []string{"snapshot:1", "snapshot:2"}) {
		t.Fatalf("Prune() = %#v, %v; kept %v", pruned, err, snapshots.kept)
	}
	if _, err := client.Prune(context.Background(), nil); err != nil || snapshots.kept == nil || len(snapshots.kept) != 0 {
		t.Fatalf("Prune() keeping nothing error = %v, kept %#v", err, snapshots.kept)
	}
	snapshots.pruneErr = files.ErrVolumeUnavailable
	if _, err := client.Prune(context.Background(), nil); !errors.Is(err, files.ErrVolumeUnavailable) {
		t.Fatalf("Prune() without the data volume error = %v, want ErrVolumeUnavailable", err)
	}
}

func TestClientSynchronizesIdentitiesAndReportsConflicts(t *testing.T) {
	credentials := &agentCredentialProvisioner{}
	client := agent.NewClient(serve(t, agent.NewOperationsHandler(agent.Services{
		Reader: fake.NewHealthy(), Credentials: credentials, Identities: credentials,
	}, discardLogger())))
	identities := []accounts.Identity{
		{Username: "owner", UID: 20100, Role: accounts.RoleAdmin, Enabled: true},
		{Username: "bob", UID: 20101, Role: accounts.RoleMember, Enabled: false},
	}
	if err := client.SyncIdentities(context.Background(), identities); err != nil {
		t.Fatalf("SyncIdentities() error = %v", err)
	}
	if !reflect.DeepEqual(credentials.identities, identities) {
		t.Fatalf("synchronized identities = %#v, want %#v", credentials.identities, identities)
	}

	credentials.failure = fmt.Errorf("%w: anas-dev", accounts.ErrIdentityConflict)
	err := client.SetCredential(context.Background(), accounts.CredentialRequest{
		UserID: "user:x", PrivateSpaceID: "space:x", Username: "anas-dev", Password: "a password for testing",
		Role: accounts.RoleMember, UID: 20102, Enabled: true,
	})
	if !errors.Is(err, accounts.ErrIdentityConflict) {
		t.Fatalf("SetCredential() conflict error = %v, want ErrIdentityConflict", err)
	}
	credentials.failure = errors.New("useradd failed")
	if err := client.SyncIdentities(context.Background(), identities); errors.Is(err, accounts.ErrIdentityConflict) || err == nil {
		t.Fatalf("generic failure error = %v, want a non-conflict failure", err)
	}
}

type agentVolumeExecutor struct {
	request storage.CreateVolumeRequest
	added   storage.AddDisksRequest
}

// agentVolumeExecutor reports a creation running, then done when asked.
func (e *agentVolumeExecutor) StartCreateVolume(_ context.Context, request storage.CreateVolumeRequest) (storage.VolumeOperation, error) {
	e.request = request
	return storage.VolumeOperation{PlanID: request.PlanID, State: storage.OperationRunning, Step: storage.StepFormat}, nil
}

func (e *agentVolumeExecutor) StartAddDisks(_ context.Context, request storage.AddDisksRequest) (storage.VolumeOperation, error) {
	if request.PlanID == "plan:busy" {
		return storage.VolumeOperation{}, storage.ErrPlanState
	}
	e.added = request
	return storage.VolumeOperation{PlanID: request.PlanID, State: storage.OperationRunning, Step: storage.StepErase}, nil
}

func (e *agentVolumeExecutor) VolumeOperation(_ context.Context, planID string) (storage.VolumeOperation, error) {
	if planID != e.request.PlanID {
		return storage.VolumeOperation{}, storage.ErrOperationNotFound
	}
	return storage.VolumeOperation{PlanID: planID, State: storage.OperationSucceeded, Volume: &storage.Volume{
		ID: "volume:data", DiskIDs: []string{e.request.Disks[0].DiskID}, Layout: e.request.Layout, State: storage.VolumeStateAvailable,
	}}, nil
}

func (e *agentVolumeExecutor) ContinueConversion(context.Context) error {
	return storage.ErrNoConversion
}

type agentCredentialProvisioner struct {
	request    accounts.CredentialRequest
	identities []accounts.Identity
	failure    error
}

func (p *agentCredentialProvisioner) SetCredential(_ context.Context, request accounts.CredentialRequest) error {
	p.request = request
	return p.failure
}

func (p *agentCredentialProvisioner) SyncIdentities(_ context.Context, identities []accounts.Identity) error {
	p.identities = identities
	return p.failure
}

func (*agentCredentialProvisioner) DisableCredential(context.Context, string) error { return nil }

type agentSnapshotBackend struct {
	created []string
	// objects is how many objects "snapshot:1" lists; failAt > 0 fails the
	// walk after that many.
	objects, failAt int
	deleteErr       error
	kept            []string
	pruned          files.SnapshotPrune
	pruneErr        error
}

func (b *agentSnapshotBackend) Create(_ context.Context, spaceID, snapshotID string) error {
	b.created = append(b.created, spaceID+"/"+snapshotID)
	return nil
}

func (b *agentSnapshotBackend) Objects(_ context.Context, snapshotID string, visit func(files.SnapshotObject) error) error {
	if snapshotID != "snapshot:1" {
		return files.ErrNotFound
	}
	for index := range b.objects {
		if b.failAt > 0 && index == b.failAt {
			return errors.New("walk failed")
		}
		if err := visit(listedObject(index)); err != nil {
			return err
		}
	}
	return nil
}

// listedObject is the index-th object of a listing: folders of 100 files,
// each folder before its files.
func listedObject(index int) files.SnapshotObject {
	folder := fmt.Sprintf("相册/2026 年 %05d", index/101)
	if index%101 == 0 {
		return files.SnapshotObject{Key: folder, ParentKey: "相册", Name: path.Base(folder), Kind: files.EntryKindDirectory,
			ModifiedAt: time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)}
	}
	name := fmt.Sprintf("IMG_%06d.jpg", index)
	return files.SnapshotObject{Key: folder + "/" + name, ParentKey: folder, Name: name, Kind: files.EntryKindFile,
		SizeBytes: int64(index) * 1000, ModifiedAt: time.Date(2026, time.October, 2, 3, 4, 5, index, time.UTC)}
}

func (*agentSnapshotBackend) Open(context.Context, string, string) (io.ReadCloser, files.SnapshotObject, error) {
	return io.NopCloser(strings.NewReader("snapshot bytes")), files.SnapshotObject{Key: "photo.jpg", Name: "photo.jpg", Kind: files.EntryKindFile, SizeBytes: 14}, nil
}

func (b *agentSnapshotBackend) Delete(context.Context, string, string) error { return b.deleteErr }

func (b *agentSnapshotBackend) Prune(_ context.Context, keep []string) (files.SnapshotPrune, error) {
	b.kept = keep
	return b.pruned, b.pruneErr
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

func TestClientReadsMetricsOverUnixSocket(t *testing.T) {
	client := agent.NewClient(serve(t, agent.NewHandler(fake.NewHealthy(), discardLogger())))

	metrics, err := client.ReadMetrics(context.Background())
	if err != nil {
		t.Fatalf("ReadMetrics() error = %v", err)
	}
	if metrics.CPU.UsagePercent != 23.5 || metrics.CPU.LogicalCores != 4 || metrics.Memory.TotalBytes != 8<<30 || metrics.Network.ReceiveBytesPerSecond != 2_516_582 {
		t.Fatalf("metrics = %+v", metrics)
	}
}

func TestClientRejectsInconsistentMetrics(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"observedAt":"2026-10-07T00:00:00Z","cpu":{"usagePercent":12,"logicalCores":4},"memory":{"totalBytes":10,"usedBytes":11},"network":{"receiveBytesPerSecond":0,"transmitBytesPerSecond":0}}`)
	})
	client := agent.NewClient(serve(t, handler))

	if _, err := client.ReadMetrics(context.Background()); err == nil {
		t.Fatal("ReadMetrics() error = nil, want validation error")
	}
}

func TestClientReportsUnavailableMetrics(t *testing.T) {
	client := agent.NewClient(serve(t, agent.NewHandler(fake.NewUnavailable(), discardLogger())))

	if _, err := client.ReadMetrics(context.Background()); err == nil {
		t.Fatal("ReadMetrics() error = nil, want unavailable error")
	}
}
