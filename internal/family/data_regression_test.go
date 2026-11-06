package family

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/activity"
)

func TestUpdatePreservesInvisiblePhotos(t *testing.T) {
	for _, omit := range []bool{false, true} {
		t.Run(map[bool]string{false: "replace visible", true: "text only"}[omit], func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			baby := f.family(t)
			f.photos.add("visible", nil, true)
			f.photos.add("hidden", nil, true)
			r := must(f.store.AddRecord(ctx, mom, baby.ID, RecordInput{Kind: KindJournal, OccurredOn: "2026-10-28", Text: "before", Photos: []string{"visible", "hidden"}}))
			f.photos.trash("hidden", true)
			input := RecordInput{Text: "after", OccurredOn: r.OccurredOn, Version: r.Version}
			if !omit {
				input.Photos = []string{"visible"}
			}
			updated, err := f.store.UpdateRecord(ctx, mom, r.ID, input)
			if err != nil {
				t.Fatal(err)
			}
			if updated.HiddenPhotos != 1 {
				t.Fatalf("hidden photo lost: %+v", updated)
			}
			f.photos.trash("hidden", false)
			restored := must(f.store.Record(ctx, mom, r.ID))
			if len(restored.Photos) != 2 {
				t.Fatalf("restored photos = %+v", restored.Photos)
			}
		})
	}
}

func TestPurgeRetainsRecentUploadsAndRemovesOrphanRecordings(t *testing.T) {
	f := newFixture(t)
	f.now = time.Now()
	for _, name := range []string{".upload-active", ".upload-crashed", "rec_orphan"} {
		path := filepath.Join(f.store.recordings, name)
		if err := os.WriteFile(path, []byte("audio"), 0600); err != nil {
			t.Fatal(err)
		}
		if name != ".upload-active" {
			old := f.now.Add(-48 * time.Hour)
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := f.store.Purge(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.store.recordings, ".upload-active")); err != nil {
		t.Errorf("active upload removed: %v", err)
	}
	for _, name := range []string{".upload-crashed", "rec_orphan"} {
		if _, err := os.Stat(filepath.Join(f.store.recordings, name)); !os.IsNotExist(err) {
			t.Errorf("orphan %s remains: %v", name, err)
		}
	}
}

func TestRecordingCapacityBeforeAndAfterWrite(t *testing.T) {
	for _, refuseAt := range []int{1, 2} {
		f := newFixture(t)
		baby := f.family(t)
		calls := 0
		f.store.admits = func(_ string, incoming int64) (bool, error) {
			calls++
			want := int64(MaxRecordingBytes)
			if calls == 2 {
				want = 0
			}
			if incoming != want {
				t.Errorf("incoming=%d want %d", incoming, want)
			}
			return calls != refuseAt, nil
		}
		_, err := f.store.AddRecording(context.Background(), mom, baby.ID, RecordingInput{ContentType: "audio/webm"}, strings.NewReader("audio"))
		wantErr(t, err, ErrNoSpace)
		if calls != refuseAt {
			t.Errorf("checks=%d want %d", calls, refuseAt)
		}
		entries, err := os.ReadDir(f.store.recordings)
		if err != nil || len(entries) != 0 {
			t.Fatalf("failed upload left files: %v %v", entries, err)
		}
		var count int
		if err := f.store.db.QueryRow("SELECT count(*) FROM records").Scan(&count); err != nil || count != 0 {
			t.Fatalf("records=%d err=%v", count, err)
		}
	}
}

func TestWallPagesBeyondTwoHundredLetters(t *testing.T) {
	f := newFixture(t)
	baby := f.family(t)
	ctx := context.Background()
	ids := map[string]bool{}
	for i := range 205 {
		c := must(f.store.WallComment(ctx, mom, baby.ID, CommentInput{Text: itoa(i)}))
		ids[c.ID] = true
	}
	cursor := ""
	for {
		page := must(f.store.Wall(ctx, dad, baby.ID, cursor))
		if len(page.Items) > PageSize {
			t.Fatal("oversized page")
		}
		for _, c := range page.Items {
			if !ids[c.ID] {
				t.Fatalf("duplicate %s", c.ID)
			}
			delete(ids, c.ID)
		}
		cursor = page.Next
		if cursor == "" {
			break
		}
	}
	if len(ids) != 0 {
		t.Fatalf("%d letters unreachable", len(ids))
	}
	_, err := f.store.Wall(ctx, mom, baby.ID, "invalid")
	wantErr(t, err, ErrInvalid)
	_, err = f.store.Wall(ctx, tenant, baby.ID, "")
	wantErr(t, err, ErrNotMember)
}

func TestFamilyActivityScopeAndRetention(t *testing.T) {
	f := newFixture(t)
	f.family(t)
	ctx := context.Background()
	events := must(f.store.Activity(ctx, mom, activity.Query{Limit: 50, Subject: &activity.Subject{ActorIDs: []string{dad.AccountID}}}))
	if len(events) == 0 {
		t.Fatal("family activities missing")
	}
	for _, e := range events {
		if e.ActorUserID != mom.AccountID || activity.CategoryOf(e.Action) != activity.CategoryFamily {
			t.Fatalf("wrong scope: %+v", e)
		}
	}
	f.now = f.now.AddDate(0, 0, AuditDays+1)
	must(f.store.Join(ctx, tenant, "uncle"))
	if err := f.store.Purge(ctx); err != nil {
		t.Fatal(err)
	}
	if events := must(f.store.Activity(ctx, mom, activity.Query{Limit: 50})); len(events) != 0 {
		t.Fatal("expired events retained")
	}
	if events := must(f.store.Activity(ctx, tenant, activity.Query{Limit: 50})); len(events) != 1 {
		t.Fatalf("new join event missing: %+v", events)
	}
}

type simultaneousPhotos struct {
	Photos
	calls   atomic.Int32
	ready   chan struct{}
	release chan struct{}
}

func (p *simultaneousPhotos) Lookup(ctx context.Context, a Actor, ids []string) (map[string]Photo, error) {
	if p.calls.Add(1) <= 2 {
		p.ready <- struct{}{}
		<-p.release
	}
	return p.Photos.Lookup(ctx, a, ids)
}
func TestConcurrentRecordRetryReturnsOriginal(t *testing.T) {
	for _, kind := range []Kind{KindJournal, KindPhotos} {
		t.Run(string(kind), func(t *testing.T) {
			f := newFixture(t)
			baby := f.family(t)
			ctx := context.Background()
			f.photos.add("a", at("2026-10-28 10:00"), true)
			p := &simultaneousPhotos{Photos: f.photos, ready: make(chan struct{}, 2), release: make(chan struct{})}
			f.store.photos = p
			type result struct {
				r   Record
				err error
			}
			results := make(chan result, 2)
			for range 2 {
				go func() {
					r, err := f.store.AddRecord(ctx, mom, baby.ID, RecordInput{Kind: kind, Text: "hello", OccurredOn: "2026-10-28", Photos: []string{"a"}, RequestKey: "retry"})
					results <- result{r, err}
				}()
			}
			<-p.ready
			<-p.ready
			close(p.release)
			first, second := <-results, <-results
			if first.err != nil || second.err != nil || first.r.ID != second.r.ID {
				t.Fatalf("retries differ: %+v %+v", first, second)
			}
		})
	}
}

func TestPurgeDoesNotInterruptAnOldActiveUpload(t *testing.T) {
	f := newFixture(t)
	f.now = time.Now()
	baby := f.family(t)
	ctx := context.Background()
	reader, writer := io.Pipe()
	type result struct {
		record Record
		err    error
	}
	done := make(chan result, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		record, err := f.store.AddRecording(ctx, mom, baby.ID, RecordingInput{ContentType: "audio/webm"}, reader)
		done <- result{record, err}
	}()
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
		<-finished
	})
	// A completed pipe write proves the real upload has created its temporary file.
	if _, err := writer.Write([]byte("audio")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(f.store.recordings)
	if err != nil || len(entries) != 1 {
		t.Fatalf("uploads: %v %v", entries, err)
	}
	old := f.now.Add(-48 * time.Hour)
	path := filepath.Join(f.store.recordings, entries[0].Name())
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Purge(ctx); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	uploaded := <-done
	if uploaded.err != nil {
		t.Fatalf("purge interrupted upload: %v", uploaded.err)
	}
	file, _, err := f.store.OpenRecording(ctx, mom, uploaded.record.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(file)
	if err != nil || string(data) != "audio" {
		t.Fatalf("audio lost: %s %v", data, err)
	}
}

func TestUpdateRetainedUndatedPhotoKeepsLocalCalendarDate(t *testing.T) {
	saoPaulo, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		zone *time.Location
		day  string
	}{
		{name: "negative UTC offset", zone: time.FixedZone("UTC-5", -5*60*60), day: "2026-10-29"},
		{name: "day without local midnight", zone: saoPaulo, day: "2018-11-04"},
	} {
		t.Run(test.name, func(t *testing.T) {
			previous := time.Local
			time.Local = test.zone
			t.Cleanup(func() { time.Local = previous })
			f := newFixture(t)
			baby := f.family(t)
			ctx := context.Background()
			imported, err := time.ParseInLocation("2006-01-02 15:04", test.day+" 12:00", test.zone)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"visible", "hidden"} {
				f.photos.photos[id] = Photo{ID: id, Shared: true, ImportedAt: imported}
			}
			record := must(f.store.AddRecord(ctx, mom, baby.ID, RecordInput{Kind: KindPhotos, Photos: []string{"visible", "hidden"}}))
			f.photos.trash("hidden", true)
			updated, err := f.store.UpdateRecord(ctx, mom, record.ID, RecordInput{
				Text: "edited", Photos: []string{"visible"}, Version: record.Version,
			})
			if err != nil {
				t.Fatal(err)
			}
			if updated.OccurredOn != record.OccurredOn {
				t.Fatalf("retaining a hidden photo changed the calendar date: got %s, want %s", updated.OccurredOn, record.OccurredOn)
			}
		})
	}
}
