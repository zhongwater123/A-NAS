package engine

import (
	"context"
	"errors"
	"github.com/zhongwater123/A-NAS/internal/appstore"
	"io"
	"testing"
	"time"
)

func TestAdmissionInspectionDoesNotHoldJobLockOrAcceptStaleSnapshot(t *testing.T) {
	s := New(nil, appstore.Policy{}, t.TempDir(), nil, nil)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := s.startChecked("first", appstore.JobInstall, time.Minute, func(*appstore.Job) error { close(entered); <-release; return nil }, func(context.Context, io.Writer) error { t.Error("stale inspection admitted"); return nil })
		done <- err
	}()
	<-entered
	// A different job can start and finish while Docker inspection waits.
	_, err := s.start("other", appstore.JobInstall, time.Minute, func(context.Context, io.Writer) error { return nil })
	if err != nil {
		t.Error(err)
	}
	s.Wait()
	close(release)
	if err := <-done; !errors.Is(err, appstore.ErrBusy) {
		t.Fatalf("stale admission: %v", err)
	}
}
func TestCommitRejectsExpiredPreparationBeforeWorkerForgetsToken(t *testing.T) {
	s := New(nil, appstore.Policy{}, t.TempDir(), nil, nil)
	s.active = "memos"
	s.preparations["token"] = &installPreparation{id: "memos", ready: make(chan error, 1), deadline: time.Now().Add(-time.Second)}
	if _, err := s.CommitInstall(context.Background(), "memos", "token", appstore.Identity{}); !errors.Is(err, appstore.ErrUnavailable) {
		t.Fatalf("expired commit: %v", err)
	}
	if len(s.preparations["token"].ready) != 0 {
		t.Fatal("expired commit sent success to worker")
	}
}
