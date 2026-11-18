package montage_test

import (
	"context"
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

func TestUnavailableAnalysisAsksMemberForLocalModel(t *testing.T) {
	env := newJobEnv(t)
	env.lib.set(func() { env.lib.states["asset:a1"] = photos.AnalysisUnavailable })
	job := env.settle(t, env.generate(t, "model:unavailable").ID)
	if job.State != montage.JobNeedsInput || job.WaitReason != montage.NeedAnalysis {
		t.Fatalf("%+v", job)
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
