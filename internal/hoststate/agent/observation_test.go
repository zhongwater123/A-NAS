package agent

import (
	"testing"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
)

func TestDiskObservationIssuesDoNotHideInvalidIPC(t *testing.T) {
	state, err := fake.NewHealthy().Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*diskDocument){
		"missing model without reason": func(d *diskDocument) { d.Model = "" },
		"zero capacity without reason": func(d *diskDocument) { d.CapacityBytes = 0 },
		"wrong reason for zero capacity": func(d *diskDocument) {
			d.CapacityBytes = 0
			d.ObservationIssues = []hoststate.DiskObservationIssue{hoststate.DiskMissingModel}
		},
		"unknown issue":               func(d *diskDocument) { d.ObservationIssues = []hoststate.DiskObservationIssue{"unknown"} },
		"unrestricted observation ID": func(d *diskDocument) { d.ID = "observation:unrestricted" },
	} {
		t.Run(name, func(t *testing.T) {
			document := documentFromDomain(state)
			change(&document.Disks[0])
			if _, err := document.toDomain(); err == nil {
				t.Fatal("invalid disk document was accepted")
			}
		})
	}
}
