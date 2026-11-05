package websearch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"testing/synctest"
	"time"

	"go.yaml.in/yaml/v4"
)

func TestChallengeSuspensionsAreOwnedByANAS(t *testing.T) {
	contents, err := os.ReadFile("../../deploy/search/settings.yml")
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Search struct {
			Suspended map[string]int `yaml:"suspended_times"`
		} `yaml:"search"`
	}
	if err := yaml.Unmarshal(contents, &settings); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"SearxEngineAccessDenied", "SearxEngineCaptcha", "SearxEngineTooManyRequests", "cf_SearxEngineCaptcha", "cf_SearxEngineAccessDenied", "recaptcha_SearxEngineCaptcha"} {
		if seconds, ok := settings.Search.Suspended[name]; !ok || seconds != 0 {
			t.Errorf("%s still inherits or enables SearXNG's hidden suspension", name)
		}
	}
}

func TestOneAccountCannotReserveBothFutureWebsiteTurns(t *testing.T) {
	s := New(Options{Backend: &recordingBackend{}, Switch: NewMemorySwitch(true)})
	synctest.Test(t, func(t *testing.T) {
		s.pace = pacing{interval: 10 * time.Second, burst: 1, maxWait: 20 * time.Second}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		release, err := s.turn(ctx, "u1", "google")
		if err != nil {
			t.Fatal(err)
		}
		release()
		first, second := make(chan error, 1), make(chan error, 1)
		go func() {
			release, err := s.turn(ctx, "u1", "google images")
			if release != nil {
				release()
			}
			first <- err
		}()
		synctest.Wait()
		if release, err := s.turn(ctx, "u1", "google news"); !errors.Is(err, errBusy) {
			if release != nil {
				release()
			}
			t.Fatalf("same account reserved another website turn: %v", err)
		}
		go func() {
			release, err := s.turn(ctx, "u2", "google news")
			if release != nil {
				release()
			}
			second <- err
		}()
		synctest.Wait()
		time.Sleep(10 * time.Second)
		if err := <-first; err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Second)
		if err := <-second; err != nil {
			t.Fatalf("another member lost their turn: %v", err)
		}
	})
}

func TestCancelledSearchesKeepInflightCapacityButNeverDispatchQueuedRequests(t *testing.T) {
	backend := &recordingBackend{hold: map[string]chan struct{}{}}
	s := New(Options{Backend: backend, Switch: NewMemorySwitch(true), Secret: []byte("test"), Unpaced: true})
	synctest.Test(t, func(t *testing.T) {
		s.requests = make(chan struct{}, maxSearchRequests)
		hold := make(chan struct{})
		for i := range maxSearchRequests + 10 {
			name := fmt.Sprintf("engine-%03d", i)
			backend.engines = append(backend.engines, EngineInfo{Name: name, Categories: []string{"general"}})
			backend.hold[name] = hold
		}
		ctx, cancel := context.WithCancel(context.Background())
		old := make(chan error, 1)
		go func() {
			old <- s.Search(ctx, "u1", Query{Text: "old", Category: Web}, func(Update) error { return nil })
		}()
		synctest.Wait()
		if got := len(backend.asked()); got != maxSearchRequests {
			t.Fatalf("inflight requests = %d, want %d", got, maxSearchRequests)
		}
		cancel()
		if err := <-old; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		fresh := make(chan error, 1)
		go func() { _, err := search(s, "u2", Query{Text: "new", Category: Web}); fresh <- err }()
		synctest.Wait()
		if got := len(backend.asked()); got != maxSearchRequests {
			t.Fatalf("cancel prematurely released inflight capacity: %d", got)
		}
		if _, err := s.Thumbnail(context.Background(), "https://example.org/image.png", s.sign("https://example.org/image.png")); err != nil {
			t.Fatalf("thumbnail cannot use reserved headroom: %v", err)
		}
		close(hold)
		if err := <-fresh; err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		oldCount := 0
		for _, form := range backend.forms {
			if form.Get("q") == "old" {
				oldCount++
			}
		}
		if oldCount != maxSearchRequests {
			t.Fatalf("cancelled queued requests reached backend: %d", oldCount)
		}
	})
}

func TestQueuedWebsiteRequestRechecksPauseBeforeDispatch(t *testing.T) {
	backend := &recordingBackend{engines: []EngineInfo{{Name: "google images", Categories: []string{"images"}}}}
	s := New(Options{Backend: backend, Switch: NewMemorySwitch(true)})
	synctest.Test(t, func(t *testing.T) {
		s.pace = pacing{interval: 10 * time.Second, burst: 1, maxWait: 20 * time.Second}
		release, err := s.turn(context.Background(), "u1", "google")
		if err != nil {
			t.Fatal(err)
		}
		release()
		done := make(chan shown, 1)
		go func() {
			page, err := search(s, "u2", Query{Text: "queued", Category: Images})
			if err != nil {
				t.Error(err)
			}
			done <- page
		}()
		synctest.Wait()
		s.judge(context.Background(), "google", Response{Unresponsive: [][]string{{"google", "CAPTCHA"}}})
		time.Sleep(10 * time.Second)
		page := <-done
		if len(backend.asked()) != 0 || len(page.Paused) != 1 || page.Paused[0] != "Google" {
			t.Fatalf("queued sibling bypassed pause: calls %v, paused %v", backend.asked(), page.Paused)
		}
	})
}

func TestLegacyEnginePausesMergeAndDoNotReturnAfterRestart(t *testing.T) {
	s := newTestService(t, &recordingBackend{}, NewMemorySwitch(true))
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	for name, pause := range map[string]Pause{
		"google":        {Reason: PauseCaptcha, Strikes: 1, Until: now.Add(time.Hour)},
		"google images": {Reason: PauseDenied, Strikes: 2, Until: now.Add(2 * time.Hour)},
	} {
		if err := s.settings.SetPause(ctx, name, pause); err != nil {
			t.Fatal(err)
		}
	}
	s.now = func() time.Time { return now }
	status, err := s.Status(ctx)
	if err != nil || len(status.Paused) != 1 || status.Paused[0].Engine != "Google" || !status.Paused[0].Until.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("migration: %+v, %v", status.Paused, err)
	}
	now = now.Add(2 * time.Hour)
	s.judge(ctx, "google news", Response{})
	again := New(Options{Settings: s.settings})
	pauses, err := again.pausedEngines(ctx)
	stored, storeErr := s.settings.Pauses(ctx)
	if err != nil || storeErr != nil || len(pauses) != 0 || len(stored) != 0 {
		t.Fatalf("legacy pause resurrected: %v %v, %v %v", pauses, stored, err, storeErr)
	}
}

func TestInflightAnswersCannotExtendOrClearAnActivePause(t *testing.T) {
	for _, reason := range []string{"CAPTCHA", ""} {
		t.Run(reason, func(t *testing.T) {
			s := newTestService(t, &recordingBackend{}, NewMemorySwitch(true))
			now := time.Date(2026, 11, 5, 1, 0, 0, 0, time.UTC)
			s.now = func() time.Time { return now }
			ctx := context.Background()
			s.judge(ctx, "baidu", Response{Unresponsive: [][]string{{"baidu", "CAPTCHA"}}})
			response := Response{}
			if reason != "" {
				response.Unresponsive = [][]string{{"baidu", reason}}
			}
			s.judge(ctx, "baidu", response)
			pauses, err := s.pausedEngines(ctx)
			if err != nil || pauses["baidu"].Strikes != 1 || !pauses["baidu"].Until.Equal(now.Add(time.Hour)) {
				t.Fatalf("inflight answer changed first pause: %+v, %v", pauses, err)
			}
		})
	}
}

func TestWebsitePauseAndPaceApplyAcrossTabs(t *testing.T) {
	t.Run("pause", func(t *testing.T) {
		backend := &recordingBackend{}
		s := newTestService(t, backend, NewMemorySwitch(true))
		s.judge(context.Background(), "baidu", Response{Unresponsive: [][]string{{"baidu", "CAPTCHA"}}})
		page, err := search(s, "u1", Query{Text: "天气", Category: Images})
		if err != nil || len(page.Paused) != 1 || page.Paused[0] != "百度" {
			t.Fatalf("image tab does not show website pause: %+v, %v", page.Paused, err)
		}
		for _, name := range backend.asked() {
			if name == "baidu images" {
				t.Fatal("paused website was asked through images")
			}
		}
	})
	t.Run("pace", func(t *testing.T) {
		backend := &recordingBackend{}
		s := newTestService(t, backend, NewMemorySwitch(true))
		s.pace = pacing{interval: time.Hour, burst: 1}
		if _, err := search(s, "u1", Query{Text: "天气", Category: Web}); err != nil {
			t.Fatal(err)
		}
		page, err := search(s, "u2", Query{Text: "天气", Category: Images})
		if err != nil || len(page.Busy) != 2 {
			t.Fatalf("image tab bypassed website pace: busy %v, %v", page.Busy, err)
		}
	})
}

func TestSiblingBlocksAreShownOnceAsAWebsiteInTheSameSearch(t *testing.T) {
	backend := &recordingBackend{
		engines: []EngineInfo{
			{Name: "google images", Categories: []string{"images"}},
			{Name: "google cse images", Categories: []string{"images"}},
		},
		responses: map[string]Response{
			"google images":     {Unresponsive: [][]string{{"google images", "CAPTCHA"}}},
			"google cse images": {Unresponsive: [][]string{{"google cse images", "CAPTCHA"}}},
		},
	}
	s := newTestService(t, backend, NewMemorySwitch(true))
	page, err := search(s, "u1", Query{Text: "weather", Category: Images})
	if err != nil || len(page.Paused) != 1 || page.Paused[0] != "Google" || len(page.Unresponsive) != 0 {
		t.Fatalf("website block: paused %v, unresponsive %v, %v", page.Paused, page.Unresponsive, err)
	}
}
