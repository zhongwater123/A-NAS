package websearch

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCommandsInTheSearchAreSearchedAsText(t *testing.T) {
	for text, want := range map[string]string{
		"!bd 天气":           "！bd 天气",
		"天气 !general":      "天气 ！general",
		"!!g 天气":           "！!g 天气",
		"!!":               "！!",
		":en weather":      "：en weather",
		"<1 天气":            "＜1 天气",
		"天气\u3000!baidu":   "天气\u3000！baidu",
		"a\x1c!baidu":      "a\x1c！baidu",
		"c:\\x 1<2 hi!":    "c:\\x 1<2 hi!",
		"css !important a": "css ！important a",
	} {
		if got := literal(text); got != want {
			t.Errorf("literal(%q) = %q, want %q", text, got, want)
		}
	}
	// "!baidu" no longer turns each engine's request into one of 百度's.
	backend := &recordingBackend{}
	service := newTestService(t, backend, NewMemorySwitch(true))
	if _, err := search(service, "u1", Query{Text: "!baidu 天气", Category: Web}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(backend.asked(), ","); got != "360search,baidu,quark,sogou,yandex" {
		t.Fatalf("asked %s", got)
	}
	for _, form := range backend.forms {
		if form.Get("q") != "！baidu 天气" {
			t.Fatalf("q = %q", form.Get("q"))
		}
	}
}

func TestEachEngineIsAskedAtItsOwnPaceWhoeverSearches(t *testing.T) {
	backend := &recordingBackend{responses: map[string]Response{"baidu": {Results: []RawResult{{URL: "https://example.org/a"}}}}}
	service := newTestService(t, backend, NewMemorySwitch(true))
	service.pace = pacing{interval: time.Hour, burst: 1, maxWait: time.Minute}
	if _, err := search(service, "u1", Query{Text: "天气", Category: Web}); err != nil {
		t.Fatal(err)
	}
	// Another account's search comes too soon after for every engine.
	page, err := search(service, "u2", Query{Text: "台风", Category: Web})
	if err != nil || len(page.Results) != 0 || strings.Join(page.Busy, "、") != "百度、搜狗、360 搜索、夸克、Yandex" {
		t.Fatalf("search = %v, busy %v, results %v", err, page.Busy, page.Results)
	}
	if len(backend.forms) != 5 || page.updates[len(page.updates)-1].Pending != 0 {
		t.Fatalf("asked %d times, last update %+v", len(backend.forms), page.updates)
	}
}

func TestASearchThatEndsGivesItsTurnBack(t *testing.T) {
	service := newTestService(t, &recordingBackend{}, NewMemorySwitch(true))
	service.pace = pacing{interval: time.Hour, burst: 1, maxWait: 2 * time.Hour}
	if _, err := search(service, "u1", Query{Text: "天气", Category: Web}); err != nil {
		t.Fatal(err)
	}
	// The next search waits an hour for each engine; the window leaves.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := service.Search(ctx, "u1", Query{Text: "台风", Category: Web}, func(Update) error { return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("search = %v", err)
	}
	// Its turn is free again: in an hour, 百度 can be asked at once.
	service.pacesMu.Lock()
	baidu := service.paces["baidu"]
	service.pacesMu.Unlock()
	for deadline := time.Now().Add(time.Second); ; {
		tokens := baidu.TokensAt(time.Now().Add(time.Hour))
		if tokens >= 0.9 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("an hour on, 百度 has %.2f turns, want the left search's back", tokens)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestTheSamePageAgainComesFromWhatTheEnginesAnswered(t *testing.T) {
	result := func(engine string) Response {
		return Response{Results: []RawResult{{URL: "https://example.org/" + engine, Engine: engine}}}
	}
	backend := &recordingBackend{
		responses: map[string]Response{"baidu": result("baidu"), "sogou": result("sogou"), "quark": result("quark")},
		failures:  map[string]error{"yandex": errors.New("connection reset")},
	}
	backend.responses["360search"] = Response{Unresponsive: [][]string{{"360search", "timeout"}}}
	service := newTestService(t, backend, NewMemorySwitch(true))
	query := Query{Text: "天气", Category: Web}
	first, err := search(service, "u1", query)
	if err != nil {
		t.Fatal(err)
	}
	// A retry asks only the engines that did not answer.
	again, err := search(service, "u1", query)
	if err != nil || len(again.Results) != len(first.Results) {
		t.Fatalf("again: %v, %d results, want %d", err, len(again.Results), len(first.Results))
	}
	if got := strings.Join(backend.asked(), ","); got != "360search,360search,baidu,quark,sogou,yandex,yandex" {
		t.Fatalf("asked %s", got)
	}
	// Another account's search of the same text asks every engine.
	if _, err := search(service, "u2", query); err != nil {
		t.Fatal(err)
	}
	if len(backend.forms) != 12 {
		t.Fatalf("asked %d times, want another account's search to ask all five", len(backend.forms))
	}
}

func TestTheNextPageAsksOnlyTheEnginesThatFoundSomething(t *testing.T) {
	backend := &recordingBackend{responses: map[string]Response{
		"baidu":     {Results: []RawResult{{URL: "https://example.org/baidu", Engine: "baidu"}}},
		"360search": {Results: []RawResult{{URL: "https://example.org/360", Engine: "360search"}}},
		// 夸克 has one page only; 搜狗 finds nothing and Yandex does not answer.
		"quark":  {Results: []RawResult{{URL: "https://example.org/quark", Engine: "quark"}}},
		"yandex": {Unresponsive: [][]string{{"yandex", "timeout"}}},
	}}
	service := newTestService(t, backend, NewMemorySwitch(true))
	if _, err := search(service, "u1", Query{Text: "天气", Category: Web}); err != nil {
		t.Fatal(err)
	}
	backend.forms = nil
	if _, err := search(service, "u1", Query{Text: "天气", Category: Web, Page: 2}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(backend.asked(), ","); got != "360search,baidu" {
		t.Fatalf("page 2 asked %s", got)
	}
	// Without the page before, such as after ten minutes, every engine that
	// has more pages is asked.
	backend.forms = nil
	if _, err := search(service, "u1", Query{Text: "台风", Category: Web, Page: 3}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(backend.asked(), ","); got != "360search,baidu,sogou,yandex" {
		t.Fatalf("page 3 of another search asked %s", got)
	}
}

func TestABlockedEngineIsPausedLongerEachTime(t *testing.T) {
	captcha := Response{Unresponsive: [][]string{{"360search", "CAPTCHA"}}}
	backend := &recordingBackend{responses: map[string]Response{"360search": captcha}}
	service := newTestService(t, backend, NewMemorySwitch(true))
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.Local)
	service.now = func() time.Time { return clock }
	ctx := context.Background()
	asked := func(text string) (shown, int) {
		t.Helper()
		before := len(backend.forms)
		page, err := search(service, "u1", Query{Text: text, Category: Web})
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, form := range backend.forms[before:] {
			if form.Get("engines") == "360search" {
				count++
			}
		}
		return page, count
	}
	if _, n := asked("天气"); n != 1 {
		t.Fatalf("360 搜索 asked %d times", n)
	}
	status, err := service.Status(ctx)
	if err != nil || len(status.Paused) != 1 || status.Paused[0] != (PausedEngine{Engine: "360 搜索", Reason: PauseCaptcha, Until: clock.Add(time.Hour)}) {
		t.Fatalf("status = %+v, %v", status.Paused, err)
	}
	// Within the hour searches leave it out, and say so in their first update.
	page, n := asked("台风")
	if n != 0 || strings.Join(page.Paused, "、") != "360 搜索" || len(page.updates[0].Paused) != 1 {
		t.Fatalf("asked %d times, paused %v", n, page.Paused)
	}
	// A pause outlives the service: the store keeps it.
	again := New(Options{Backend: backend, Switch: NewMemorySwitch(true), Settings: service.settings})
	again.now = service.now
	if paused, err := again.pausedEngines(ctx); err != nil || paused["360search"].Strikes != 1 {
		t.Fatalf("after a restart: %+v, %v", paused, err)
	}
	// Blocked again: two hours. SearXNG's own pause and a timeout change nothing.
	clock = clock.Add(time.Hour)
	if _, n := asked("台风 路径"); n != 1 {
		t.Fatalf("after the pause 360 搜索 asked %d times", n)
	}
	if paused, _ := service.pausedEngines(ctx); !paused["360search"].Until.Equal(clock.Add(2 * time.Hour)) {
		t.Fatalf("second pause = %+v", paused["360search"])
	}
	for _, reason := range []string{"Suspended: CAPTCHA", "timeout"} {
		service.judge(ctx, "360search", Response{Unresponsive: [][]string{{"360search", reason}}})
	}
	if paused, _ := service.pausedEngines(ctx); paused["360search"].Strikes != 2 {
		t.Fatalf("after SearXNG's pause and a timeout: %+v", paused["360search"])
	}
	// Once it answers, its blocks are forgotten.
	clock = clock.Add(2 * time.Hour)
	backend.mu.Lock()
	delete(backend.responses, "360search")
	backend.mu.Unlock()
	if _, n := asked("台风 风力"); n != 1 {
		t.Fatalf("after the second pause 360 搜索 asked %d times", n)
	}
	status, err = service.Status(ctx)
	if pauses, _ := service.settings.Pauses(ctx); err != nil || len(status.Paused) != 0 || len(pauses) != 0 {
		t.Fatalf("after answering: %+v, %+v, %v", status.Paused, pauses, err)
	}
}

func TestPausesDoubleUpToADay(t *testing.T) {
	var got []time.Duration
	for strikes := 1; strikes <= 7; strikes++ {
		got = append(got, pauseAfter(strikes))
	}
	want := []time.Duration{time.Hour, 2 * time.Hour, 4 * time.Hour, 8 * time.Hour, 16 * time.Hour, 24 * time.Hour, 24 * time.Hour}
	if !slices.Equal(got, want) {
		t.Fatalf("pauses = %v", got)
	}
}
