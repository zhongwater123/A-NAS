package systemupdateapi

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/systemupdate"
)

// decliningUpdater takes requests and changes nothing, as the service does
// for an install the system disk has no room for.
type decliningUpdater struct {
	status   systemupdate.Status
	requests []systemupdate.Request
}

func (u *decliningUpdater) SystemUpdate(context.Context) (systemupdate.Status, error) {
	return u.status, nil
}

func (u *decliningUpdater) RequestSystemUpdate(_ context.Context, request systemupdate.Request) (systemupdate.Status, error) {
	u.requests = append(u.requests, request)
	return u.status, nil
}

func (u *decliningUpdater) SetSystemUpdateSettings(_ context.Context, settings systemupdate.Settings) (systemupdate.Status, error) {
	u.status.Settings = settings
	return u.status, nil
}

type recordedEvent struct {
	actor, action, resource string
	at                      time.Time
}

type recorder struct{ events []recordedEvent }

func (r *recorder) RecordSystemUpdate(_ context.Context, actorID, action, resourceID, _ string, at time.Time) error {
	r.events = append(r.events, recordedEvent{actor: actorID, action: action, resource: resourceID, at: at})
	return nil
}

func TestTheAutomaticInstallAsksOnceAWindowAsTheSystem(t *testing.T) {
	updater := &decliningUpdater{status: systemupdate.Status{State: systemupdate.State{
		Phase: systemupdate.PhaseReady, Current: "1", Candidate: "2",
		Settings: systemupdate.Settings{AutoInstall: true, WindowStart: "03:00", WindowEnd: "05:00"},
	}}}
	events := &recorder{}
	api := New(updater, nil, events, slog.New(slog.DiscardHandler))
	now := time.Date(2026, 11, 14, 2, 55, 0, 0, time.Local)
	api.now = func() time.Time { return now }
	ctx := context.Background()
	// Every five minutes through the night; the service declines each time.
	for range 30 {
		api.automatic(ctx)
		now = now.Add(5 * time.Minute)
	}
	if len(updater.requests) != 1 {
		t.Fatalf("asked %d times in one window, want once", len(updater.requests))
	}
	// The next night's window asks again.
	now = time.Date(2026, 11, 15, 3, 5, 0, 0, time.Local)
	api.automatic(ctx)
	if len(updater.requests) != 2 || updater.requests[1] != (systemupdate.Request{Kind: systemupdate.RequestInstall, By: systemupdate.ByAutomatic}) {
		t.Fatalf("requests = %+v", updater.requests)
	}
	installs := 0
	for _, event := range events.events {
		if event.action == "system.update.install" {
			installs++
			if event.actor != systemActor {
				t.Errorf("the automatic install is recorded as %q, want the system", event.actor)
			}
		}
	}
	if installs != 2 {
		t.Fatalf("recorded %d automatic installs, want 2", installs)
	}
}

func TestOutcomesAreRecordedAtTheTimeTheyHappened(t *testing.T) {
	at := time.Date(2026, 11, 14, 3, 10, 0, 0, time.UTC)
	updater := &decliningUpdater{status: systemupdate.Status{State: systemupdate.State{
		Phase: systemupdate.PhaseRolledBack, Current: "1", Settings: systemupdate.DefaultSettings(),
		Records: []systemupdate.Record{{At: at, Kind: systemupdate.RecordRolledBack, Version: "2", From: "1", By: systemupdate.ByAutomatic}},
	}}}
	events := &recorder{}
	api := New(updater, nil, events, slog.New(slog.DiscardHandler))
	api.mirror(context.Background(), updater.status)
	if len(events.events) != 1 {
		t.Fatalf("events = %+v", events.events)
	}
	if event := events.events[0]; !event.at.Equal(at) || event.actor != systemActor || event.action != "system.update.result.rolled-back" {
		t.Fatalf("event = %+v, want the rollback at %s", event, at)
	}
}
