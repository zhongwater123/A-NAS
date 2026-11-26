package app_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/videocut/app"
	"github.com/zhongwater123/A-NAS/internal/videocut/domain"
	"github.com/zhongwater123/A-NAS/internal/videocutcompute"
)

func TestAJobFindsItsProjectsSong(t *testing.T) {
	ctx := context.Background()
	f := newSongFixture(t)
	if id, _, err := f.store.Chosen(ctx, "user:alice", f.project); err != nil || id != "" {
		t.Fatalf("Chosen() without a song = %q, %v", id, err)
	}
	song, err := f.put(t, "user:alice", f.project, songBytes(200, "symphony"))
	if err != nil {
		t.Fatal(err)
	}
	id, start, err := f.store.Chosen(ctx, "user:alice", f.project)
	if err != nil || id != song.ID || start != app.DefaultStart {
		t.Fatalf("Chosen() = %q, %v, %v", id, start, err)
	}
	if _, err := f.store.Excerpt(ctx, "user:alice", id, start); !errors.Is(err, domain.ErrSongAnalyzing) {
		t.Fatalf("Excerpt() before the analysis = %v", err)
	}
	if err := f.store.AnalyzeWaiting(ctx); err != nil {
		t.Fatal(err)
	}
	excerpt, err := f.store.Excerpt(ctx, "user:alice", id, start)
	if err != nil {
		t.Fatal(err)
	}
	// The default excerpt, as the domain reads it, named by the song as well.
	if excerpt.Start != 40 || excerpt.Frames != 2700 || string(excerpt.Music) != `{"excerpt":[40,130],"song":"Symphony"}` || excerpt.Key == "" {
		t.Fatalf("excerpt = %+v (%s)", excerpt, excerpt.Music)
	}
	// The default start named as a number, or as any time nearest its
	// downbeat, is the default excerpt: the same key, and the compute
	// Worker is not asked.
	asked := len(f.compute.asked)
	for _, at := range []float64{40, 40.9, 39.2} {
		again, err := f.store.Excerpt(ctx, "user:alice", id, at)
		if err != nil || again.Key != excerpt.Key || string(again.Music) != string(excerpt.Music) || len(f.compute.asked) != asked {
			t.Fatalf("the default start named as %v: %+v, %v, asked %d times more", at, again, err, len(f.compute.asked)-asked)
		}
	}
	// A dragged start snaps to its downbeat and changes the key.
	at := 21.3
	if _, err := f.store.SongStart(ctx, "user:alice", f.project, at); err != nil {
		t.Fatal(err)
	}
	if _, start, _ := f.store.Chosen(ctx, "user:alice", f.project); start != 22 {
		t.Fatalf("Chosen() after dragging = %v", start)
	}
	// The job is cut to the excerpt the project keeps, the one its member
	// saw: the compute Worker is not asked again, and may be away.
	asked, f.compute.down = len(f.compute.asked), true
	dragged, err := f.store.Excerpt(ctx, "user:alice", id, 22)
	if err != nil || dragged.Start != 22 || dragged.Frames != 2700 || dragged.Key == excerpt.Key ||
		string(dragged.Music) != `{"excerpt":[22,112],"song":"Symphony"}` {
		t.Fatalf("dragged = %+v (%s), %v", dragged, dragged.Music, err)
	}
	// A start no project keeps is the compute Worker's to cut.
	if _, err := f.store.Excerpt(ctx, "user:alice", id, 60); !errors.Is(err, videocutcompute.ErrWorkerUnavailable) {
		t.Fatalf("Excerpt() from a start no project keeps, without the worker = %v", err)
	}
	f.compute.down = false
	other, err := f.store.Excerpt(ctx, "user:alice", id, 60)
	if err != nil || other.Start != 60 || len(f.compute.asked) != asked+1 || string(other.Music) != `{"excerpt":[60,150],"song":"Symphony"}` {
		t.Fatalf("Excerpt() from a start no project keeps = %+v (%s), %v", other, other.Music, err)
	}
	// The copy is what the film's sound is made from.
	audio, err := f.store.Audio(ctx, "user:alice", id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = audio.Close() }()
	if data, _ := io.ReadAll(audio); string(data) != "song 200 symphony" {
		t.Fatalf("audio = %q", data)
	}
	// Another member's job cannot reach it.
	if _, err := f.store.Excerpt(ctx, "user:bob", id, start); !errors.Is(err, domain.ErrSongUnusable) {
		t.Fatalf("Excerpt() for bob = %v", err)
	}
	if _, err := f.store.Audio(ctx, "user:bob", id); !errors.Is(err, domain.ErrSongUnusable) {
		t.Fatalf("Audio() for bob = %v", err)
	}
	// A start that leaves no usable excerpt makes the job ask for another song.
	if _, err := f.store.Excerpt(ctx, "user:alice", id, 170); !errors.Is(err, domain.ErrSongUnusable) {
		t.Fatalf("Excerpt() late = %v", err)
	}
}

func TestAJobWithAnUnusableSongNeedsAnother(t *testing.T) {
	ctx := context.Background()
	f := newSongFixture(t)
	f.compute.unusable = app.UnusableLongSlot
	song, err := f.put(t, "user:alice", f.project, songBytes(200, "slow"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.AnalyzeWaiting(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Excerpt(ctx, "user:alice", song.ID, app.DefaultStart); !errors.Is(err, domain.ErrSongUnusable) {
		t.Fatalf("Excerpt() = %v", err)
	}
}
