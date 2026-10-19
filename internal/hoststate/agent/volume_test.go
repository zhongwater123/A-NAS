package agent_test

import (
	"context"
	"io"
	"net/http"
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
