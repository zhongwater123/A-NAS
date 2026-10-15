package shares

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestPartialSaveRecordsActualCommittedFilesForSender(t *testing.T) {
	backend := &testBackend{capture: func(ctx context.Context, visit func(Entry, io.Reader) error) error {
		if err := visit(Entry{Path: "a.txt", Size: 1}, strings.NewReader("a")); err != nil {
			return err
		}
		return visit(Entry{Path: "b.txt", Size: 1}, strings.NewReader("b"))
	}, save: func(ctx context.Context, entries []Entry, open func(string) (io.ReadCloser, error), commit func(string, func() error) error) error {
		if err := commit(entries[0].ID, func() error { return nil }); err != nil {
			return err
		}
		return errors.New("destination became read-only")
	}}
	s, _ := setup(t, backend)
	v := publish(t, s)
	job, err := s.Save(context.Background(), bob, v.ID, SaveRequest{RequestID: "partial", EntryIDs: []string{v.Entries[0].ID, v.Entries[1].ID}, Target: Target{RootID: "home"}})
	if err != nil {
		t.Fatal(err)
	}
	done := awaitJob(t, s, job.ID, "failed")
	if len(done.CompletedIDs) != 1 {
		t.Fatalf("completed files %+v", done)
	}
	view, err := s.Get(context.Background(), sender, v.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if !view.Recipients[0].Saved || view.Recipients[0].SavedFiles != 1 {
		t.Fatalf("partial saved state %+v", view.Recipients[0])
	}
}
func TestPublishedFileRemainsVisibleWhenCommitMetadataFails(t *testing.T) {
	backend := &testBackend{}
	s, _ := setup(t, backend)
	backend.save = func(ctx context.Context, entries []Entry, open func(string) (io.ReadCloser, error), commit func(string, func() error) error) error {
		return commit(entries[0].ID, func() error { return s.db.Close() })
	}
	v := publish(t, s)
	job, err := s.Save(context.Background(), bob, v.ID, SaveRequest{RequestID: "metadata-failure", EntryIDs: []string{v.Entries[0].ID}, Target: Target{RootID: "home"}})
	if err != nil {
		t.Fatal(err)
	}
	done := awaitJob(t, s, job.ID, "failed")
	if len(done.CompletedIDs) != 1 || !strings.Contains(done.Error, "recorded") {
		t.Fatalf("published fact hidden %+v", done)
	}
}
