package agent_test

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
	"github.com/zhongwater123/A-NAS/internal/hoststate/agent"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
)

type observedState struct{ hoststate.State }

func (o observedState) Read(context.Context) (hoststate.State, error) { return o.State, nil }

func (observedState) ReadMetrics(ctx context.Context) (hoststate.Metrics, error) {
	return fake.NewHealthy().ReadMetrics(ctx)
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
	client := agent.NewClient(serve(t, agent.NewHandler(observedState{state}, discardLogger())))

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
	client := agent.NewClient(serve(t, agent.NewHandler(observedState{state}, discardLogger())))

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
