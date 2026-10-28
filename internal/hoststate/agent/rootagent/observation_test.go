package rootagent

import (
	"testing"

	"github.com/zhongwater123/A-NAS/internal/hoststate/agent/wire"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
)

func TestDiskObservationIssuesDoNotHideInvalidIPC(t *testing.T) {
	state, err := fake.NewHealthy().Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*wire.DiskDocument){
		"missing model without reason": func(d *wire.DiskDocument) { d.Model = "" },
		"zero capacity without reason": func(d *wire.DiskDocument) { d.CapacityBytes = 0 },
		"wrong reason for zero capacity": func(d *wire.DiskDocument) {
			d.CapacityBytes = 0
			d.ObservationIssues = []hoststate.DiskObservationIssue{hoststate.DiskMissingModel}
		},
		"unknown issue":               func(d *wire.DiskDocument) { d.ObservationIssues = []hoststate.DiskObservationIssue{"unknown"} },
		"unrestricted observation ID": func(d *wire.DiskDocument) { d.ID = "observation:unrestricted" },
	} {
		t.Run(name, func(t *testing.T) {
			document := wire.DocumentFromDomain(state)
			change(&document.Disks[0])
			if _, err := document.ToDomain(); err == nil {
				t.Fatal("invalid disk document was accepted")
			}
		})
	}
}
