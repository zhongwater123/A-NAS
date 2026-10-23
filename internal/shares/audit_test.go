package shares

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRevokeFencesEvenWhenMetadataCannotBeWritten(t *testing.T) {
	s, _ := setup(t, &testBackend{})
	v := publish(t, s)
	backend := &fencedBackend{}
	s.mu.Lock()
	s.options.Backend = backend
	s.mu.Unlock()
	s.db.Close()
	if err := s.Revoke(context.Background(), sender, v.ID); err == nil {
		t.Fatal("persistence failure ignored")
	}
	if backend.calls != 1 {
		t.Fatal("remote publication remained unfenced after database failure")
	}
}
func TestMaintenanceCancelsDisabledRecipientWithoutRevokingOtherRecipients(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	b := &testBackend{save: func(ctx context.Context, entries []Entry, open func(string) (io.ReadCloser, error), commit func(string, func() error) error) error {
		close(started)
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	}}
	s, _ := setup(t, b)
	var disabled atomic.Bool
	s.mu.Lock()
	s.options.Active = func(ctx context.Context, id string) (bool, error) { return id != bob.ID || !disabled.Load(), nil }
	s.mu.Unlock()
	v := publish(t, s)
	job, err := s.Save(context.Background(), bob, v.ID, SaveRequest{RequestID: "disable", EntryIDs: []string{v.Entries[0].ID}, Target: Target{RootID: "home"}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	disabled.Store(true)
	if err = s.Maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("disabled recipient save not cancelled")
	}
	view, err := s.Get(context.Background(), carol, v.ID, false)
	if err != nil || view.Status != "published" {
		t.Fatalf("other recipient lost share %+v %v", view, err)
	}
	disabled.Store(false)
	awaitJob(t, s, job.ID, "cancelled")
}
func TestOfflineVolumeRejectsMetadataWritesWithoutLeakingMessages(t *testing.T) {
	s, _ := setup(t, &testBackend{})
	v := publish(t, s)
	s.mu.Lock()
	s.options.CheckVolume = func(context.Context) error { return errors.New("volume offline") }
	s.mu.Unlock()
	if _, err := s.SendMessage(context.Background(), bob, v.ID, bob.ID, "offline", "must not publish"); err == nil {
		t.Fatal("metadata written without verified volume")
	}
	messages, _ := s.Messages(context.Background(), bob, v.ID, bob.ID, 0, 100)
	if len(messages) != 1 {
		t.Fatal("unpersisted offline message leaked")
	}
}
func TestDirectoryOutageIsNotInvalidCredentials(t *testing.T) {
	s, _ := setup(t, &testBackend{})
	v := publish(t, s)
	outage := errors.New("lookup unavailable")
	s.mu.Lock()
	s.options.Active = func(context.Context, string) (bool, error) { return false, outage }
	s.mu.Unlock()
	if _, err := s.Get(context.Background(), bob, v.ID, false); !errors.Is(err, outage) || errors.Is(err, ErrUnauthorized) {
		t.Fatalf("outage misclassified %v", err)
	}
}
func TestUnknownPublicationIsInterruptedAndMarkedUncertain(t *testing.T) {
	backend := &testBackend{save: func(context.Context, []Entry, func(string) (io.ReadCloser, error), func(string, func() error) error) error {
		return ErrPublicationUnknown
	}}
	s, _ := setup(t, backend)
	v := publish(t, s)
	job, err := s.Save(context.Background(), bob, v.ID, SaveRequest{RequestID: "unknown", EntryIDs: []string{v.Entries[0].ID}, Target: Target{RootID: "home"}})
	if err != nil {
		t.Fatal(err)
	}
	done := awaitJob(t, s, job.ID, "interrupted")
	if !strings.Contains(done.Error, "may exist") {
		t.Fatalf("unknown outcome hidden %+v", done)
	}
	view, err := s.Get(context.Background(), sender, v.ID, false)
	if err != nil || !view.Recipients[0].SaveUncertain {
		t.Fatalf("sender uncertainty hidden %+v %v", view, err)
	}
}
func TestDisabledRecipientDoesNotInvalidateSenderSession(t *testing.T) {
	s, _ := setup(t, &testBackend{})
	v := publish(t, s)
	s.mu.Lock()
	s.options.Active = func(ctx context.Context, id string) (bool, error) { return id != bob.ID, nil }
	s.mu.Unlock()
	if _, err := s.SendMessage(context.Background(), sender, v.ID, bob.ID, "disabled", "reply"); err == nil || errors.Is(err, ErrUnauthorized) {
		t.Fatalf("sender session incorrectly rejected: %v", err)
	}
	if _, err := s.SendMessage(context.Background(), sender, v.ID, carol.ID, "active", "reply"); err != nil {
		t.Fatal(err)
	}
}
func TestInterruptMarksExistingSaveInterrupted(t *testing.T) {
	started := make(chan struct{})
	b := &testBackend{save: func(ctx context.Context, entries []Entry, open func(string) (io.ReadCloser, error), commit func(string, func() error) error) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}}
	s, _ := setup(t, b)
	v := publish(t, s)
	job, err := s.Save(context.Background(), bob, v.ID, SaveRequest{RequestID: "interrupt", EntryIDs: []string{v.Entries[0].ID}, Target: Target{RootID: "home"}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err = s.Interrupt("File service restarted"); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	j := s.records[v.ID].Jobs[job.ID].Job
	s.mu.Unlock()
	if j.Status != "interrupted" || !strings.Contains(j.Error, "restarted") {
		t.Fatalf("interruption %+v", j)
	}
}
