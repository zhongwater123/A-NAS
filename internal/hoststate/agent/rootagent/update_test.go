package rootagent_test

import (
	"context"
	"errors"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/hoststate/agent"
	"github.com/zhongwater123/A-NAS/internal/hoststate/agent/rootagent"
	"github.com/zhongwater123/A-NAS/internal/systemupdate"
)

// fakeUpdater stands in for the executor: it holds the state and is busy
// while running.
type fakeUpdater struct {
	status   systemupdate.Status
	requests []systemupdate.Request
}

func (f *fakeUpdater) SystemUpdate(context.Context) (systemupdate.Status, error) {
	return f.status, nil
}

func (f *fakeUpdater) RequestSystemUpdate(_ context.Context, request systemupdate.Request) (systemupdate.Status, error) {
	if f.status.Running {
		return systemupdate.Status{}, systemupdate.ErrBusy
	}
	f.requests = append(f.requests, request)
	f.status.Running = true
	return f.status, nil
}

func (f *fakeUpdater) SetSystemUpdateSettings(_ context.Context, settings systemupdate.Settings) (systemupdate.Status, error) {
	f.status.Settings = settings
	return f.status, nil
}

func TestClientDrivesTheSystemUpdateOverUnixSocket(t *testing.T) {
	updater := &fakeUpdater{status: systemupdate.Status{State: systemupdate.State{
		Phase: systemupdate.PhaseReady, Current: "1.0.1", Candidate: "1.0.2", Settings: systemupdate.DefaultSettings()}}}
	client := agent.NewClient(serve(t, rootagent.NewOperationsHandler(rootagent.Services{SystemUpdate: updater}, discardLogger())))
	ctx := context.Background()

	status, err := client.SystemUpdate(ctx)
	if err != nil || status.Phase != systemupdate.PhaseReady || status.Candidate != "1.0.2" || status.Running {
		t.Fatalf("SystemUpdate() = %+v, %v", status, err)
	}
	status, err = client.RequestSystemUpdate(ctx, systemupdate.Request{Kind: systemupdate.RequestInstall, By: "owner"})
	if err != nil || !status.Running || len(updater.requests) != 1 || updater.requests[0].By != "owner" {
		t.Fatalf("RequestSystemUpdate() = %+v, %v; requests %+v", status, err, updater.requests)
	}
	if _, err := client.RequestSystemUpdate(ctx, systemupdate.Request{Kind: systemupdate.RequestCheck, By: "owner"}); !errors.Is(err, systemupdate.ErrBusy) {
		t.Fatalf("a request while running = %v, want ErrBusy", err)
	}
	// The timer's own requests, and unknown ones, are not the Product Service's to make.
	for _, kind := range []string{systemupdate.RequestTick, systemupdate.RequestBoot, "reboot"} {
		updater.status.Running = false
		if _, err := client.RequestSystemUpdate(ctx, systemupdate.Request{Kind: kind, By: "owner"}); err == nil {
			t.Errorf("request %q was accepted", kind)
		}
	}
	settings := systemupdate.Settings{AutoDownload: false, AutoInstall: true, WindowStart: "01:30", WindowEnd: "04:00"}
	status, err = client.SetSystemUpdateSettings(ctx, settings)
	if err != nil || status.Settings != settings {
		t.Fatalf("SetSystemUpdateSettings() = %+v, %v", status, err)
	}
	if _, err := client.SetSystemUpdateSettings(ctx, systemupdate.Settings{WindowStart: "25:00", WindowEnd: "04:00"}); err == nil {
		t.Fatal("an install window that is not a time of day was accepted")
	}
}
