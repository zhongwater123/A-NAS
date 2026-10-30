package rootagent_test

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
	"github.com/zhongwater123/A-NAS/internal/hoststate/agent"
	"github.com/zhongwater123/A-NAS/internal/hoststate/agent/rootagent"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
)

type observedState struct{ hoststate.State }

func (o observedState) Read(context.Context) (hoststate.State, error) { return o.State, nil }

func (observedState) ReadMetrics(ctx context.Context) (hoststate.Metrics, error) {
	return fake.NewHealthy().ReadMetrics(ctx)
}

func TestClientReadsSystemStorageAndRejectsInvalidUsage(t *testing.T) {
	for _, usage := range []*hoststate.StorageUsage{
		nil,
		{TotalBytes: 100 << 30, AvailableBytes: 25 << 30},
		{TotalBytes: 100 << 30},
		{TotalBytes: 0},
		{TotalBytes: 10, AvailableBytes: 11},
	} {
		state, err := fake.NewHealthy().Read(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		state.System.Storage = usage
		client := agent.NewClient(serve(t, rootagent.NewHandler(observedState{state}, discardLogger())))
		got, err := client.Read(context.Background())
		invalid := usage != nil && (usage.TotalBytes == 0 || usage.AvailableBytes > usage.TotalBytes)
		if invalid {
			if err == nil {
				t.Fatalf("invalid usage %+v was accepted", usage)
			}
		} else if err != nil || !reflect.DeepEqual(got.System.Storage, usage) {
			t.Fatalf("usage = %+v, %v; want %+v", got.System.Storage, err, usage)
		}
	}
}

func TestClientReadsTheDataVolumeReadOnlyStateAndErrors(t *testing.T) {
	state, err := fake.NewHealthy().Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	total, available, errorsCounted := uint64(500<<30), uint64(300<<30), uint64(3)
	for index := range state.Disks {
		if state.Disks[index].ID.String() == "disk:fake-data-01" {
			state.Disks[index].Role = hoststate.DiskRoleData
			state.Disks[index].FilesystemCapacityBytes = &total
			state.Disks[index].FilesystemAvailableBytes = &available
			state.Disks[index].FilesystemReadOnly = true
			state.Disks[index].FilesystemErrors = &errorsCounted
		}
	}
	client := agent.NewClient(serve(t, rootagent.NewHandler(observedState{state}, discardLogger())))

	got, err := client.Read(context.Background())
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	data := got.Disks[0]
	if !data.FilesystemReadOnly || data.FilesystemErrors == nil || *data.FilesystemErrors != 3 {
		t.Fatalf("data disk = %+v, want read-only with 3 errors", data)
	}
	if got.Disks[1].FilesystemReadOnly || got.Disks[1].FilesystemErrors != nil {
		t.Fatalf("system disk = %+v, want no data volume facts", got.Disks[1])
	}
}

// The Host Agent reports a failed reconciliation of the data volume with its
// state, and leaves the field out otherwise (#166).
func TestClientReadsAFailedReconciliation(t *testing.T) {
	state, err := fake.NewHealthy().Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var failed atomic.Bool
	handler := rootagent.NewOperationsHandler(rootagent.Services{Reader: observedState{state}, ReconcileFailed: failed.Load}, discardLogger())
	client := agent.NewClient(serve(t, handler))
	if got, err := client.Read(context.Background()); err != nil || got.ReconcileFailed {
		t.Fatalf("Read() = %v, %v; want no failed reconciliation", got.ReconcileFailed, err)
	}
	failed.Store(true)
	if got, err := client.Read(context.Background()); err != nil || !got.ReconcileFailed {
		t.Fatalf("Read() = %v, %v; want the failed reconciliation", got.ReconcileFailed, err)
	}
}

func TestClientRejectsDataVolumeFactsOnAnotherDisk(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"observedAt":"2026-10-06T00:00:00Z","system":{"id":"host:fake-01","hostname":"anas-fake",
"operatingSystem":{"name":"Debian","version":"13"},"architecture":"amd64","uptimeSeconds":1,"health":"unknown"},
"disks":[{"id":"disk:system","model":"SSD","transport":"nvme","capacityBytes":1,"rotational":false,"removable":false,"inUse":true,
"filesystems":["ext4"],"role":"system","health":"healthy","smartStatus":"healthy","filesystemErrors":2}]}`)
	})
	client := agent.NewClient(serve(t, handler))

	if _, err := client.Read(context.Background()); err == nil {
		t.Fatal("Read() error = nil, want data volume facts on the system disk refused")
	}
}

// A data volume spanning disks, its profiles and the stale signatures on a
// spare disk cross the socket unchanged.
func TestClientReadsTheDataVolumeAcrossDisks(t *testing.T) {
	state, err := fake.NewWithSpareDisk().Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	state.Disks[0].Role = hoststate.DiskRoleData
	state.DataVolume = &hoststate.DataVolume{
		FilesystemUUID: "09e275fe-794a-458d-8200-b6e67c55cc22", DiskIDs: []hoststate.ResourceID{state.Disks[0].ID},
		Data:      []hoststate.ProfileUsage{{Profile: hoststate.ProfileSingle, UsedBytes: 7}, {Profile: hoststate.ProfileRAID1, UsedBytes: 3}},
		Metadata:  []hoststate.ProfileUsage{{Profile: hoststate.ProfileRAID1, UsedBytes: 1}},
		Operation: hoststate.VolumeOperationBalance,
	}
	client := agent.NewClient(serve(t, rootagent.NewHandler(observedState{state}, discardLogger())))

	got, err := client.Read(context.Background())
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if !reflect.DeepEqual(got.DataVolume, state.DataVolume) {
		t.Fatalf("data volume = %+v, want %+v", got.DataVolume, state.DataVolume)
	}
	if !reflect.DeepEqual(got.Disks[1].Signatures, state.Disks[1].Signatures) {
		t.Fatalf("signatures = %+v, want %+v", got.Disks[1].Signatures, state.Disks[1].Signatures)
	}
}

func TestClientRejectsADataVolumeOnADiskThatIsNotADataDisk(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"observedAt":"2026-10-06T00:00:00Z","system":{"id":"host:fake-01","hostname":"anas-fake",
"operatingSystem":{"name":"Debian","version":"13"},"architecture":"amd64","uptimeSeconds":1,"health":"unknown"},
"disks":[{"id":"disk:system","model":"SSD","transport":"nvme","capacityBytes":1,"rotational":false,"removable":false,"inUse":true,
"filesystems":["ext4"],"role":"system","health":"healthy","smartStatus":"healthy"}],
"dataVolume":{"filesystemUuid":"uuid","diskIds":["disk:system"],"data":[],"metadata":[]}}`)
	})
	client := agent.NewClient(serve(t, handler))

	if _, err := client.Read(context.Background()); err == nil {
		t.Fatal("Read() error = nil, want a data volume on the system disk refused")
	}
}

// CPU and GPU temperatures cross the Host Agent socket with their limits,
// and fan speeds with them; an implausible reading is refused rather than
// shown.
func TestClientReadsTheThermalState(t *testing.T) {
	state, err := fake.NewHealthy().Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	state.System.Thermal.GPUs = append(state.System.Thermal.GPUs, hoststate.GPU{
		ID: state.System.ID, Name: "GPU 1", Temperature: &hoststate.Temperature{Celsius: 52, HotCelsius: 90},
	})
	got, err := agent.NewClient(serve(t, rootagent.NewHandler(observedState{state}, discardLogger()))).Read(context.Background())
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if !reflect.DeepEqual(got.System.Thermal, state.System.Thermal) {
		t.Fatalf("thermal = %+v, want %+v", got.System.Thermal, state.System.Thermal)
	}

	state.System.Thermal.CPU = &hoststate.Temperature{Celsius: 400, HotCelsius: 90}
	if _, err := agent.NewClient(serve(t, rootagent.NewHandler(observedState{state}, discardLogger()))).Read(context.Background()); err == nil {
		t.Fatal("Read() accepted a CPU at 400 °C")
	}

	state.System.Thermal.CPU = nil
	state.System.Thermal.Fans[0].Stopped = true
	if _, err := agent.NewClient(serve(t, rootagent.NewHandler(observedState{state}, discardLogger()))).Read(context.Background()); err == nil {
		t.Fatal("Read() accepted a stopped fan turning at 1240 RPM")
	}
}
