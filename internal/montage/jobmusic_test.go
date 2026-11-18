package montage_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/montage"
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
	if err != nil || id != song.ID || start != montage.DefaultStart {
		t.Fatalf("Chosen() = %q, %v, %v", id, start, err)
	}
	if _, err := f.store.Excerpt(ctx, "user:alice", id, start); !errors.Is(err, montage.ErrSongAnalyzing) {
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
	if excerpt.Start != 40 || excerpt.Frames != 2700 || string(excerpt.Music) != `{"song":"Symphony","excerpt":[40,130]}` || excerpt.Key == "" {
		t.Fatalf("excerpt = %+v (%s)", excerpt, excerpt.Music)
	}
	again, _ := f.store.Excerpt(ctx, "user:alice", id, 40)
	if again.Key != excerpt.Key {
		t.Fatalf("the default start named as a number changed the key: %q, %q", again.Key, excerpt.Key)
	}
	// A dragged start snaps to its downbeat and changes the key.
	at := 21.3
	if _, err := f.store.SongStart(ctx, "user:alice", f.project, at); err != nil {
		t.Fatal(err)
	}
	if _, start, _ := f.store.Chosen(ctx, "user:alice", f.project); start != 22 {
		t.Fatalf("Chosen() after dragging = %v", start)
	}
	dragged, err := f.store.Excerpt(ctx, "user:alice", id, 22)
	if err != nil || dragged.Start != 22 || dragged.Key == excerpt.Key || string(dragged.Music) != `{"song":"Symphony","excerpt":[22,112]}` {
		t.Fatalf("dragged = %+v, %v", dragged, err)
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
	if _, err := f.store.Excerpt(ctx, "user:bob", id, start); !errors.Is(err, montage.ErrSongUnusable) {
		t.Fatalf("Excerpt() for bob = %v", err)
	}
	if _, err := f.store.Audio(ctx, "user:bob", id); !errors.Is(err, montage.ErrSongUnusable) {
		t.Fatalf("Audio() for bob = %v", err)
	}
	// A start that leaves no usable excerpt makes the job ask for another song.
	if _, err := f.store.Excerpt(ctx, "user:alice", id, 170); !errors.Is(err, montage.ErrSongUnusable) {
		t.Fatalf("Excerpt() late = %v", err)
	}
}

func TestAJobWithAnUnusableSongNeedsAnother(t *testing.T) {
	ctx := context.Background()
	f := newSongFixture(t)
	f.compute.unusable = montage.UnusableLongSlot
	song, err := f.put(t, "user:alice", f.project, songBytes(200, "slow"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.AnalyzeWaiting(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Excerpt(ctx, "user:alice", song.ID, montage.DefaultStart); !errors.Is(err, montage.ErrSongUnusable) {
		t.Fatalf("Excerpt() = %v", err)
	}
}
