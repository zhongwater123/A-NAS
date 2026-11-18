package montage_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/montage"
	"github.com/zhongwater123/A-NAS/internal/photos"
)

func TestRestartAfterLostPhotoReplyReconcilesDespiteTrashedInput(t *testing.T) {
	env := newJobEnv(t)
	env.lib.set(func() { env.lib.lostSave = 1 })
	job := env.generate(t, "lost:reply")
	if !env.store.RunOnce(context.Background()) {
		t.Fatal("job not claimed")
	}
	if waiting := env.job(t, job.ID); waiting.WaitReason != montage.WaitPhotos || env.lib.saves != 1 {
		t.Fatalf("job=%+v, imports=%d", waiting, env.lib.saves)
	}
	env.lib.set(func() { v := env.lib.videos["asset:a1"]; v.trashed = true; env.lib.videos["asset:a1"] = v })
	if err := env.store.Close(); err != nil {
		t.Fatal(err)
	}
	env.open(t)
	env.clock.advance(3 * time.Minute)
	if done := env.settle(t, job.ID); done.State != montage.JobDone || done.Version == nil || env.lib.saves != 1 {
		t.Fatalf("job=%+v, imports=%d", done, env.lib.saves)
	}
}

// Without the local model that describes the cards, the job needs the model
// and not one video or another: analysing again or dropping a video does not
// help, and the member goes on once the model is installed.
func TestUnavailableAnalysisAsksMemberForLocalModel(t *testing.T) {
	ctx := t.Context()
	env := newJobEnv(t)
	env.lib.set(func() { env.lib.states["asset:a1"] = photos.AnalysisUnavailable })
	job := env.settle(t, env.generate(t, "model:unavailable").ID)
	if job.State != montage.JobNeedsInput || job.WaitReason != montage.NeedModel || len(job.Missing) != 0 {
		t.Fatalf("%+v", job)
	}
	if _, err := env.store.RetryAnalysis(ctx, "user:alice", job.ID); !errors.Is(err, montage.ErrJobState) {
		t.Fatalf("RetryAnalysis() without the model = %v", err)
	}
	if _, err := env.store.DropAssets(ctx, "user:alice", job.ID, []string{"asset:a1"}); !errors.Is(err, montage.ErrJobState) {
		t.Fatalf("DropAssets() without the model = %v", err)
	}
	// Still not installed: the job stops again, it does not poll.
	if _, err := env.store.Retry(ctx, "user:alice", job.ID); err != nil {
		t.Fatal(err)
	}
	if again := env.settle(t, job.ID); again.State != montage.JobNeedsInput || again.WaitReason != montage.NeedModel {
		t.Fatalf("%+v", again)
	}
	env.lib.set(func() { delete(env.lib.states, "asset:a1") })
	if _, err := env.store.Retry(ctx, "user:alice", job.ID); err != nil {
		t.Fatal(err)
	}
	if done := env.settle(t, job.ID); done.State != montage.JobDone {
		t.Fatalf("%+v", done)
	}
}

func TestLostPhotoReplyThenGrantRevocationRequiresCheckingTheAlbum(t *testing.T) {
	env := newJobEnv(t)
	env.lib.set(func() { env.lib.lostSave = 1 })
	job := env.generate(t, "lost:revoked")
	if !env.store.RunOnce(t.Context()) {
		t.Fatal("job not claimed")
	}
	if waiting := env.job(t, job.ID); waiting.WaitReason != montage.WaitPhotos {
		t.Fatalf("%+v", waiting)
	}
	env.lib.set(func() {
		for _, g := range env.lib.grants {
			g.revoked = true
		}
	})
	env.clock.advance(3 * time.Minute)
	done := env.settle(t, job.ID)
	if done.State != montage.JobFailed || !strings.Contains(done.Error, "无法确认") || done.Version != nil || env.lib.saves != 1 {
		t.Fatalf("job=%+v, imports=%d", done, env.lib.saves)
	}
}

type reanalysedSong struct {
	music
	version string
}

func (song reanalysedSong) ExcerptKey(context.Context, string, string, float64) (string, error) {
	return song.version, nil
}

func (song reanalysedSong) Excerpt(ctx context.Context, owner, songID string, start float64) (montage.Excerpt, error) {
	result, err := song.music.Excerpt(ctx, owner, songID, start)
	result.Key = song.version
	return result, err
}

func TestSongAnalysisChangeVoidsBothAcceptedPlans(t *testing.T) {
	env := newJobEnv(t)
	env.lib.set(func() { env.lib.noSpace = true })
	env.store.UseMusic(reanalysedSong{version: "before"})
	job := env.generate(t, "new:analysis")
	if needs := env.settle(t, job.ID); needs.WaitReason != montage.NeedStorageFull {
		t.Fatalf("%+v", needs)
	}
	env.store.UseMusic(reanalysedSong{version: "after"})
	env.lib.set(func() { env.lib.noSpace = false })
	if _, err := env.store.Retry(t.Context(), "user:alice", job.ID); err != nil {
		t.Fatal(err)
	}
	if done := env.settle(t, job.ID); done.State != montage.JobDone || env.planner.picks != 2 || env.planner.shots != 2 {
		t.Fatalf("job=%+v, picks=%d, shots=%d", done, env.planner.picks, env.planner.shots)
	}
}
