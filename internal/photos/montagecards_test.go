package photos_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
	"github.com/zhongwater123/A-NAS/internal/photos"
)

const (
	cardsJob   = "montage-cards/v1"
	qualityJob = "montage-quality/v1"
)

// jobRow is a job as the Catalog holds it.
type jobRow struct {
	state, class string
	attempts     int
}

// job reads the only job of derivation, or reports that there is none.
func (f montageFixture) job(t *testing.T, derivation string) (jobRow, bool) {
	t.Helper()
	var job jobRow
	err := catalog(t, f.root).QueryRow("SELECT state, error_class, attempts FROM jobs WHERE derivation = ?", derivation).
		Scan(&job.state, &job.class, &job.attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return job, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return job, true
}

// wantJob fails unless the job of derivation is as wanted.
func (f montageFixture) wantJob(t *testing.T, derivation string, want jobRow) {
	t.Helper()
	if got, ok := f.job(t, derivation); !ok || got != want {
		t.Fatalf("the %s job = %+v (there: %v), want %+v", derivation, got, ok, want)
	}
}

// rounds runs n rounds of the AI runner, each of which must do work.
func (f montageFixture) rounds(t *testing.T, n int) {
	t.Helper()
	for i := range n {
		if worked, err := f.service.ProcessAIJob(context.Background(), f.embedder, f.gate); !worked || err != nil {
			t.Fatalf("round %d = %v, %v", i+1, worked, err)
		}
	}
}

// stages reads where a granted video's cards and their quality stand.
func (f montageFixture) stages(t *testing.T, grant photos.Grant, video photos.Asset) (cards, quality photos.AnalysisState) {
	t.Helper()
	looked, err := f.service.LookupGrant(context.Background(), granting(namedAlice), grant.ID, []string{video.ID})
	if err != nil || len(looked) != 1 {
		t.Fatalf("LookupGrant() = %+v, %v", looked, err)
	}
	return looked[0].Cards, looked[0].Quality
}

// count runs a query that counts rows.
func (f montageFixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var count int
	if err := catalog(t, f.root).QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// cardsFixture is a video of Alice's that a project asked for, its windows
// done in two rounds and its cards waiting to be cut.
func cardsFixture(t *testing.T) (f montageFixture, video photos.Asset, grant photos.Grant, object string) {
	t.Helper()
	f = newMontageFixture(t)
	f.enableCompute(t)
	private, _ := libraries(t, f.service, namedAlice)
	video = importPhoto(t, f.service, namedAlice, private.ID, "", "trip.mp4", mp4Bytes(1))
	grant, object = f.grant(t, namedAlice, "project:cards", video)
	f.rounds(t, 2)
	f.wantJob(t, cardsJob, jobRow{state: "pending"})
	return f, video, grant, object
}

// Cutting cards the service stopped, or that ran out of time, is not a
// result the Worker refused: the video's cards are cut again, at once after
// a stop and after a wait, with an attempt spent, after a timeout.
func TestCuttingCardsThatStoppedOrTimedOutIsTriedAgain(t *testing.T) {
	f, video, grant, _ := cardsFixture(t)
	// An install restarts the photo service while the Worker cuts the cards.
	ctx, cancel := context.WithCancel(context.Background())
	f.compute.hook = func(called context.Context, _ string) error {
		cancel()
		<-called.Done()
		return called.Err()
	}
	if worked, err := f.service.ProcessAIJob(ctx, f.embedder, f.gate); worked || err != nil {
		t.Fatalf("a round the service stopped in = %v, %v", worked, err)
	}
	f.wantJob(t, cardsJob, jobRow{state: "pending"})
	// The call's five minutes run out with the service still running.
	f.compute.hook = nil
	f.compute.cardsErr = context.DeadlineExceeded
	f.rounds(t, 1)
	f.wantJob(t, cardsJob, jobRow{state: "pending", class: "io", attempts: 1})
	if cards, _ := f.stages(t, grant, video); cards != photos.AnalysisQueued {
		t.Fatalf("cards after a timeout = %q, want them queued again", cards)
	}
	// The Worker took the request and could not finish it.
	f.clock.Advance(time.Hour)
	f.compute.cardsErr = photos.ErrComputeFailed
	f.rounds(t, 1)
	f.wantJob(t, cardsJob, jobRow{state: "pending", class: "io", attempts: 2})
	f.compute.cardsErr = nil
	f.clock.Advance(time.Hour)
	processAI(t, f.service, f.embedder, f.gate)
	if cards, quality := f.stages(t, grant, video); cards != photos.AnalysisDone || quality != photos.AnalysisDone {
		t.Fatalf("after trying again: cards %q, quality %q", cards, quality)
	}
}

// A Catalog that cannot be read while the cards are cut is a fault of
// storage, not of the video: tried again.
func TestACatalogFaultWhileCuttingCardsIsTriedAgain(t *testing.T) {
	f, video, grant, _ := cardsFixture(t)
	db := catalog(t, f.root)
	if _, err := db.Exec("ALTER TABLE video_windows RENAME TO video_windows_away"); err != nil {
		t.Fatal(err)
	}
	f.rounds(t, 1)
	f.wantJob(t, cardsJob, jobRow{state: "pending", class: "io", attempts: 1})
	if f.compute.cut != 0 {
		t.Fatalf("asked the Worker for cards %d times without their windows", f.compute.cut)
	}
	if _, err := db.Exec("ALTER TABLE video_windows_away RENAME TO video_windows"); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(time.Hour)
	processAI(t, f.service, f.embedder, f.gate)
	if cards, quality := f.stages(t, grant, video); cards != photos.AnalysisDone || quality != photos.AnalysisDone {
		t.Fatalf("after the Catalog came back: cards %q, quality %q", cards, quality)
	}
}

// Cards are cut from the windows of one analysis only: not while the
// windows are queued again, and not from windows of two identities.
func TestCardsAreNotCutFromWindowsBeingRedoneOrOfTwoIdentities(t *testing.T) {
	f, video, grant, object := cardsFixture(t)
	db := catalog(t, f.root)
	// A new model queued the windows again; they wait, as for a Worker
	// that is away.
	if _, err := db.Exec(`INSERT INTO jobs(object_id, derivation, state, not_before, created_at)
		VALUES(?, 'montage-windows/v1', 'pending', '9999-01-01T00:00:00.000000000Z', '2026-10-08T09:00:00.000000000Z')`, object); err != nil {
		t.Fatal(err)
	}
	if worked, err := f.service.ProcessAIJob(context.Background(), f.embedder, f.gate); worked || err != nil {
		t.Fatalf("a round with the windows queued again = %v, %v", worked, err)
	}
	f.wantJob(t, cardsJob, jobRow{state: "pending"})
	if _, err := db.Exec("DELETE FROM jobs WHERE derivation = 'montage-windows/v1'"); err != nil {
		t.Fatal(err)
	}
	// Windows a chunk of another model overwrote in part.
	if _, err := db.Exec("UPDATE video_windows SET identity = 'another' WHERE idx < 5"); err != nil {
		t.Fatal(err)
	}
	f.rounds(t, 1)
	f.wantJob(t, cardsJob, jobRow{state: "pending", class: "io", attempts: 1})
	if f.compute.cut != 0 {
		t.Fatalf("asked the Worker for cards %d times from windows of two identities or being redone", f.compute.cut)
	}
	if _, err := db.Exec("UPDATE video_windows SET identity = (SELECT identity FROM video_windows WHERE idx = 34)"); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(time.Hour)
	processAI(t, f.service, f.embedder, f.gate)
	if cards, _ := f.stages(t, grant, video); cards != photos.AnalysisDone || f.compute.cut != 1 {
		t.Fatalf("with windows of one identity: cards %q, cut %d times", cards, f.compute.cut)
	}
}

// Cards cut, or a quality measured, for a video that went meanwhile are not
// stored: nothing would remove them.
func TestCardsAndQualityOfAVideoThatWentAreNotStored(t *testing.T) {
	ctx := context.Background()
	t.Run("purged while its cards were cut", func(t *testing.T) {
		f, video, _, object := cardsFixture(t)
		f.compute.hook = func(context.Context, string) error {
			if _, err := f.service.Trash(ctx, namedAlice, video.ID); err != nil {
				t.Error(err)
			}
			if err := f.service.Purge(ctx, namedAlice, video.ID); err != nil {
				t.Error(err)
			}
			return nil
		}
		if _, err := f.service.ProcessAIJob(ctx, f.embedder, f.gate); err != nil {
			t.Fatalf("the round its video was purged in: %v", err)
		}
		f.compute.hook = nil
		processAI(t, f.service, f.embedder, f.gate)
		if cards, jobs := f.count(t, "SELECT COUNT(*) FROM video_cards WHERE object_id = ?", object), f.montageJobs(t); cards != 0 || jobs != 0 {
			t.Fatalf("after the purge: %d cards, %d montage jobs", cards, jobs)
		}
	})
	t.Run("pruned while a card was measured", func(t *testing.T) {
		f, _, grant, object := cardsFixture(t)
		f.rounds(t, 1)
		f.compute.hook = func(_ context.Context, op string) error {
			if op != "quality" {
				return nil
			}
			// The project is deleted, and a week passes.
			if err := f.service.RevokeGrant(ctx, grant.ID); err != nil {
				t.Error(err)
			}
			if _, err := f.service.PruneMontageAnalysis(ctx); err != nil {
				t.Error(err)
			}
			f.clock.Advance(7*24*time.Hour + time.Minute)
			if removed, err := f.service.PruneMontageAnalysis(ctx); err != nil || removed != 1 {
				t.Errorf("PruneMontageAnalysis() a week later = %d, %v", removed, err)
			}
			return nil
		}
		if _, err := f.service.ProcessAIJob(ctx, f.embedder, f.gate); err != nil {
			t.Fatalf("the round its analysis was pruned in: %v", err)
		}
		f.compute.hook = nil
		processAI(t, f.service, f.embedder, f.gate)
		if qualities, jobs := f.count(t, "SELECT COUNT(*) FROM video_card_quality WHERE object_id = ?", object), f.montageJobs(t); qualities != 0 || jobs != 0 {
			t.Fatalf("after the prune: %d measured cards, %d montage jobs", qualities, jobs)
		}
	})
}

// An FFmpeg that hands over one frame more than the range has does not
// shift the moves: the chunks still share exactly one frame.
func TestAnExtraShakeFrameIsLeftOut(t *testing.T) {
	f, video, grant, _ := cardsFixture(t)
	f.video.mu.Lock()
	f.video.extra = true
	f.video.mu.Unlock()
	processAI(t, f.service, f.embedder, f.gate)
	if _, quality := f.stages(t, grant, video); quality != photos.AnalysisDone {
		t.Fatalf("quality = %q", quality)
	}
	// Cards of 34 and 36 seconds, ten frames a second.
	if len(f.compute.moves) != 2 || f.compute.moves[0] != 34*10-1 || f.compute.moves[1] != 36*10-1 {
		t.Fatalf("moves = %v, want one between each pair of a card's frames", f.compute.moves)
	}
}

// The queued cards and quality of a video no grant refers to any more go
// at once rather than with the weekly prune, and what was done stays for a
// grant that asks again.
func TestQueuedCardWorkGoesWithTheLastGrant(t *testing.T) {
	ctx := context.Background()
	f, video, grant, _ := cardsFixture(t)
	owner := granting(namedAlice)
	again := func(project string) photos.Grant {
		t.Helper()
		grant, err := f.service.RegisterGrant(ctx, owner, project, []string{video.ID})
		if err != nil {
			t.Fatal(err)
		}
		if err := f.service.AnalyzeGrant(ctx, owner, grant.ID, []string{video.ID}, false); err != nil {
			t.Fatal(err)
		}
		return grant
	}
	idle := func(what string) {
		t.Helper()
		if worked, err := f.service.ProcessAIJob(ctx, f.embedder, f.gate); worked || err != nil {
			t.Fatalf("a round after %s = %v, %v", what, worked, err)
		}
		if jobs := f.montageJobs(t); jobs != 0 {
			t.Fatalf("montage jobs left after %s = %d", what, jobs)
		}
	}
	// The project goes with the cards still to cut.
	if err := f.service.RevokeGrant(ctx, grant.ID); err != nil {
		t.Fatal(err)
	}
	idle("the grant went before the cards")
	if f.compute.cut != 0 {
		t.Fatalf("cut the cards of a video no grant refers to %d times", f.compute.cut)
	}
	// Another project asks: the cards are cut, and it goes before their quality.
	second := again("project:second")
	f.rounds(t, 2)
	if err := f.service.RevokeGrant(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	idle("the grant went before the quality")
	if f.compute.cut != 1 || f.compute.qualities != 1 {
		t.Fatalf("before the second grant went: cut %d times, %d cards measured", f.compute.cut, f.compute.qualities)
	}
	// A third finds the cards and the measured card, and has the rest done.
	third := again("project:third")
	if cards, quality := f.stages(t, third, video); cards != photos.AnalysisDone || quality != photos.AnalysisQueued {
		t.Fatalf("asked again: cards %q, quality %q", cards, quality)
	}
	processAI(t, f.service, f.embedder, f.gate)
	if _, quality := f.stages(t, third, video); quality != photos.AnalysisDone || f.compute.cut != 1 || f.compute.qualities != 2 {
		t.Fatalf("after asking again: quality %q, cut %d times, %d cards measured", quality, f.compute.cut, f.compute.qualities)
	}
}

// continuingGate lets new work start throughout, and tells running work to
// stop once stop is set: someone began to use the library, and stopped
// again before the round next asked.
type continuingGate struct {
	mu   sync.Mutex
	stop string
}

func (g *continuingGate) Open(context.Context, uint64) (bool, string) { return true, "" }

func (g *continuingGate) Continue(context.Context, uint64) (bool, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stop == "", g.stop
}

func (g *continuingGate) set(stop string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stop = stop
}

// cardsAndAPhoto is cardsFixture with a photo waiting for its vector.
func cardsAndAPhoto(t *testing.T) (f montageFixture, video photos.Asset, grant photos.Grant) {
	t.Helper()
	f = newMontageFixture(t)
	f.enableCompute(t)
	private, _ := libraries(t, f.service, namedAlice)
	video = importPhoto(t, f.service, namedAlice, private.ID, "", "trip.mp4", mp4Bytes(1))
	importPhoto(t, f.service, namedAlice, private.ID, "", "a.png", encodePNG(1))
	processAll(t, f.service)
	grant, _ = f.grant(t, namedAlice, "project:cards", video)
	f.rounds(t, 2)
	if f.embedder.embedded != 35 {
		t.Fatalf("embedded %d images for 35 windows", f.embedder.embedded)
	}
	return f, video, grant
}

// Cards hung up end the round, as a hung-up chunk of windows does: the
// photos' own work waits too, and the cards keep their place and attempt.
func TestHungUpCardsEndTheRound(t *testing.T) {
	ctx := context.Background()
	f, video, grant := cardsAndAPhoto(t)
	gate := &continuingGate{}
	f.compute.hook = func(called context.Context, _ string) error {
		gate.set("foreground")
		<-called.Done()
		return called.Err()
	}
	if worked, err := f.service.ProcessAIJob(ctx, f.embedder, gate); worked || err != nil {
		t.Fatalf("a round whose cards were hung up = %v, %v", worked, err)
	}
	if f.embedder.embedded != 35 {
		t.Fatalf("embedded %d images in the round the cards were hung up in; the photo went on", f.embedder.embedded)
	}
	if status := aiStatus(t, f.service, namedAlice); status.State != photos.AIPaused || status.Reason != "foreground" {
		t.Fatalf("status after hanging up = %+v", status)
	}
	f.wantJob(t, cardsJob, jobRow{state: "pending"})
	f.compute.hook = nil
	gate.set("")
	processAI(t, f.service, f.embedder, gate)
	if cards, quality := f.stages(t, grant, video); cards != photos.AnalysisDone || quality != photos.AnalysisDone || f.embedder.embedded != 36 {
		t.Fatalf("after resuming: cards %q, quality %q, %d images embedded", cards, quality, f.embedder.embedded)
	}
}

// A compute or FFmpeg Worker that is away keeps the cards waiting without
// spending their attempts, and does not keep the photos' own work waiting
// with them.
func TestPhotosGoOnWhileAWorkerOfTheCardsIsAway(t *testing.T) {
	f, video, grant := cardsAndAPhoto(t)
	f.compute.cardsErr = photos.ErrComputeUnavailable
	processAI(t, f.service, f.embedder, f.gate)
	if f.embedder.embedded != 36 {
		t.Fatalf("embedded %d images with the compute Worker away; the photo waited for it", f.embedder.embedded)
	}
	// The cards wait a minute before the Worker is asked again.
	f.wantJob(t, cardsJob, jobRow{state: "pending"})
	if f.compute.cut != 1 {
		t.Fatalf("asked a Worker that is away %d times within the minute", f.compute.cut)
	}
	f.compute.cardsErr = nil
	f.clock.Advance(time.Minute)
	f.rounds(t, 1)
	// The FFmpeg Worker is away for the first card's frames.
	f.video.mu.Lock()
	f.video.failing = ffmpegworker.ErrUnavailable
	f.video.mu.Unlock()
	processAI(t, f.service, f.embedder, f.gate)
	f.wantJob(t, qualityJob, jobRow{state: "pending"})
	if f.compute.qualities != 0 {
		t.Fatalf("measured %d cards without their frames", f.compute.qualities)
	}
	f.video.mu.Lock()
	f.video.failing = nil
	f.video.mu.Unlock()
	f.clock.Advance(time.Minute)
	processAI(t, f.service, f.embedder, f.gate)
	if cards, quality := f.stages(t, grant, video); cards != photos.AnalysisDone || quality != photos.AnalysisDone {
		t.Fatalf("with the Workers back: cards %q, quality %q", cards, quality)
	}
}

// An original that cannot be opened for a fault of storage is tried again;
// only one that is gone fails the quality for good, until a retry.
func TestAnOriginalThatCannotBeOpenedIsToldFromOneThatIsGone(t *testing.T) {
	ctx := context.Background()
	f, video, grant, object := cardsFixture(t)
	f.rounds(t, 1)
	original := filepath.Join(f.root, "objects", object[0:2], object[2:4], object)
	content, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	// A link to itself opens with an error that is not "does not exist".
	if err := os.Remove(original); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(original), original); err != nil {
		t.Fatal(err)
	}
	f.rounds(t, 1)
	f.wantJob(t, qualityJob, jobRow{state: "pending", class: "io", attempts: 1})
	if err := os.Remove(original); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(time.Hour)
	f.rounds(t, 1)
	f.wantJob(t, qualityJob, jobRow{state: "failed", class: "missing_original", attempts: 2})
	if err := os.WriteFile(original, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.service.AnalyzeGrant(ctx, granting(namedAlice), grant.ID, []string{video.ID}, true); err != nil {
		t.Fatal(err)
	}
	processAI(t, f.service, f.embedder, f.gate)
	if _, quality := f.stages(t, grant, video); quality != photos.AnalysisDone {
		t.Fatalf("quality with the original back = %q", quality)
	}
}

// A video without cards is done, and stays done over a restart: nothing is
// cut or measured again.
func TestAVideoWithoutCardsStaysDoneOverARestart(t *testing.T) {
	f, video, grant, _ := cardsFixture(t)
	f.compute.empty = true
	processAI(t, f.service, f.embedder, f.gate)
	before, _ := f.analysis(t, namedAlice, grant.ID, video.ID)
	if before.Cards != photos.AnalysisDone || before.Quality != photos.AnalysisDone || before.CardsIdentity == "" ||
		before.QualityIdentity == "" || before.CardCount != 0 {
		t.Fatalf("a video without cards = %+v", before)
	}
	f.restart(t)
	after, _ := f.analysis(t, namedAlice, grant.ID, video.ID)
	if after.Cards != photos.AnalysisDone || after.Quality != photos.AnalysisDone || after.CardsIdentity != before.CardsIdentity ||
		after.QualityIdentity != before.QualityIdentity || after.CardCount != 0 {
		t.Fatalf("after a restart = %+v, before it %+v", after, before)
	}
	processAI(t, f.service, f.embedder, f.gate)
	if f.compute.cut != 1 || f.montageJobs(t) != 0 {
		t.Fatalf("after a restart: cut %d times, %d montage jobs", f.compute.cut, f.montageJobs(t))
	}
}

// A new model makes the windows again, and with them the cards and their
// quality: until then neither counts as done, and their identities name
// the windows they came from.
func TestANewModelCutsAndMeasuresTheCardsAgain(t *testing.T) {
	f, video, grant, _ := cardsFixture(t)
	processAI(t, f.service, f.embedder, f.gate)
	before, _ := f.analysis(t, namedAlice, grant.ID, video.ID)
	if before.Quality != photos.AnalysisDone || !strings.Contains(before.CardsIdentity, "fake-1"+fakeWindows) ||
		!strings.HasSuffix(before.QualityIdentity, before.CardsIdentity) {
		t.Fatalf("with the first model = %+v", before)
	}
	// A new model comes with a release, which restarts the photo service.
	f.restart(t)
	f.embedder.model = "fake-2"
	f.rounds(t, 1)
	if cards, quality := f.stages(t, grant, video); cards != photos.AnalysisQueued || quality != photos.AnalysisQueued {
		t.Fatalf("while the windows are made again: cards %q, quality %q", cards, quality)
	}
	processAI(t, f.service, f.embedder, f.gate)
	after, _ := f.analysis(t, namedAlice, grant.ID, video.ID)
	if after.Cards != photos.AnalysisDone || after.Quality != photos.AnalysisDone || len(after.CardList) != 2 ||
		!strings.Contains(after.CardsIdentity, "fake-2"+fakeWindows) || !strings.HasSuffix(after.QualityIdentity, after.CardsIdentity) {
		t.Fatalf("with the new model = %+v", after)
	}
	for _, card := range after.CardList {
		if card.Quality == nil {
			t.Fatalf("a card of the new model without its quality: %+v", card)
		}
	}
	if f.compute.cut != 2 || f.compute.qualities != 4 {
		t.Fatalf("cut %d times, %d cards measured; want both done once for each model", f.compute.cut, f.compute.qualities)
	}
}
