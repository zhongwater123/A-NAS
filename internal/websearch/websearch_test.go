package websearch

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// testEngines are the engines the first release loaded, as SearXNG lists them.
var testEngines = []EngineInfo{
	{Name: "baidu", Categories: []string{"general"}}, {Name: "sogou", Categories: []string{"general"}},
	{Name: "360search", Categories: []string{"general"}}, {Name: "quark", Categories: []string{"general"}},
	{Name: "yandex", Categories: []string{"general"}},
	{Name: "baidu images", Categories: []string{"images"}}, {Name: "quark images", Categories: []string{"images"}},
	{Name: "bilibili", Categories: []string{"videos"}}, {Name: "iqiyi", Categories: []string{"videos"}},
	{Name: "acfun", Categories: []string{"videos"}}, {Name: "sogou videos", Categories: []string{"videos"}},
	{Name: "360search videos", Categories: []string{"videos"}}, {Name: "youtube", Categories: []string{"videos", "music"}},
	{Name: "dailymotion", Categories: []string{"videos"}},
	{Name: "sogou wechat", Categories: []string{"news"}}, {Name: "piratebay", Categories: []string{"files"}},
}

// recordingBackend answers each engine as told and remembers what it was
// asked; the Service asks the engines of a search at once.
type recordingBackend struct {
	mu sync.Mutex
	// responses answer each engine; one without finds nothing. failures fail
	// an engine's request, and hold keeps its answer until the channel closes.
	responses map[string]Response
	failures  map[string]error
	hold      map[string]chan struct{}
	forms     []url.Values
	images    []string
	// engines answers /config, testEngines when nil; failing makes it fail.
	engines     []EngineInfo
	failing     bool
	engineReads int
}

func (r *recordingBackend) Engines(context.Context) ([]EngineInfo, error) {
	r.engineReads++
	if r.failing {
		return nil, errors.New("SearXNG is restarting")
	}
	if r.engines != nil {
		return r.engines, nil
	}
	return testEngines, nil
}

func (r *recordingBackend) Search(ctx context.Context, form url.Values) (Response, error) {
	name := form.Get("engines")
	r.mu.Lock()
	r.forms = append(r.forms, form)
	hold, response, err := r.hold[name], r.responses[name], r.failures[name]
	r.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return Response{}, ctx.Err()
		}
	}
	return response, err
}

func (r *recordingBackend) Thumbnail(_ context.Context, imageURL, signature string) (Image, error) {
	r.images = append(r.images, imageURL+" "+signature)
	return Image{ContentType: "image/jpeg", Body: []byte("jpeg")}, nil
}

// asked are the engines the searches asked, one a request, by name.
func (r *recordingBackend) asked() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var names []string
	for _, form := range r.forms {
		names = append(names, form.Get("engines"))
	}
	slices.Sort(names)
	return names
}

func newTestService(t *testing.T, backend Backend, power Switch) *Service {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "web-search.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return New(Options{Backend: backend, Switch: power, Settings: store, Secret: []byte("test secret"), ThumbnailPath: "/thumb"})
}

// shown is what the window shows once a search's updates have all come.
type shown struct {
	updates      []Update
	Results      []Result
	Unresponsive []string
	Suggestions  []string
}

func search(service *Service, account string, query Query) (shown, error) {
	var page shown
	err := service.Search(context.Background(), account, query, func(update Update) error {
		page.updates = append(page.updates, update)
		page.Results = append(page.Results, update.Results...)
		for _, found := range update.Found {
			for i := range page.Results {
				if page.Results[i].URL == found.URL {
					page.Results[i].Engines = found.Engines
				}
			}
		}
		page.Unresponsive = append(page.Unresponsive, update.Unresponsive...)
		page.Suggestions = append(page.Suggestions, update.Suggestions...)
		return nil
	})
	return page, err
}

func TestSearchAsksEachEngineOfTheTabInTheBody(t *testing.T) {
	backend := &recordingBackend{}
	service := newTestService(t, backend, NewMemorySwitch(true))
	if _, err := search(service, "u1", Query{Text: "  台风  ", Category: News, Page: 2, TimeRange: Week}); err != nil {
		t.Fatal(err)
	}
	form := backend.forms[0]
	if form.Get("q") != "台风" || form.Get("engines") != "sogou wechat" || form.Has("categories") ||
		form.Get("pageno") != "2" || form.Get("time_range") != "week" || form.Get("format") != "json" || form.Get("safesearch") != "1" {
		t.Fatalf("form = %v", form)
	}
	if _, err := search(service, "u1", Query{Text: "台风", Category: Web}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(backend.asked(), ","); got != "360search,baidu,quark,sogou,sogou wechat,yandex" {
		t.Fatalf("asked %s, want each web engine on its own", got)
	}
}

func TestResultsAreMappedForEachTab(t *testing.T) {
	published := "2026-10-25T06:59:20"
	youtube := RawResult{URL: "https://www.youtube.com/watch?v=x", Title: "The Wandering Earth", Engine: "youtube",
		Thumbnail: "https://i.ytimg.com/vi/x/hq.jpg", Length: "2:16:29", Positions: []int{1}}
	backend := &recordingBackend{responses: map[string]Response{
		"bilibili": {Results: []RawResult{{URL: "http://www.bilibili.com/video/av1", Title: "流浪地球3", Content: "-",
			Engine: "bilibili", PublishedDate: &published, Thumbnail: "//i1.hdslb.com/a.jpg", Length: "233.0", Author: "笙筌"}}},
		// 搜狗视频 finds YouTube's video as well, after one of its own.
		"sogou videos": {Results: []RawResult{
			{URL: "https://v.sogou.com/vc/np?link=1", Title: "资讯", Engine: "sogou videos", Length: 20.0, Positions: []int{1}},
			{URL: "https://youtube.com/watch?v=x", Engine: "sogou videos", Positions: []int{2}},
		}},
		"youtube": {Results: []RawResult{youtube}, Suggestions: []string{" 流浪地球 2 ", ""}},
		"acfun":   {Results: []RawResult{{URL: "javascript:alert(1)", Title: "dropped", Engine: "acfun"}}},
		"iqiyi":   {Unresponsive: [][]string{{"iqiyi", "unexpected crash"}, {"iqiyi", "timeout"}, {"unknown", "x"}}},
	}}
	service := newTestService(t, backend, NewMemorySwitch(true))
	page, err := search(service, "u1", Query{Text: "流浪地球", Category: Videos})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 3 {
		t.Fatalf("results = %+v, want the javascript: address dropped and YouTube's video once", page.Results)
	}
	// The engines take turns in the tab's order: 哔哩哔哩, then 搜狗视频, then YouTube.
	bilibili, sogou, video := page.Results[0], page.Results[1], page.Results[2]
	if bilibili.Platform != "哔哩哔哩" || bilibili.Content != "" || bilibili.Domain != "bilibili.com" || *bilibili.DurationSeconds != 233 ||
		bilibili.PublishedAt == nil || bilibili.PublishedAt.Day() != 25 ||
		!strings.Contains(bilibili.Thumbnail, url.QueryEscape("https://i1.hdslb.com/a.jpg")) {
		t.Fatalf("bilibili = %+v", bilibili)
	}
	if video.Platform != "YouTube" || *video.DurationSeconds != 8189 || strings.Join(video.Engines, ",") != "YouTube,搜狗视频" {
		t.Fatalf("youtube = %+v", video)
	}
	if sogou.Platform != "v.sogou.com" || *sogou.DurationSeconds != 20 || sogou.Thumbnail != "" {
		t.Fatalf("sogou = %+v", sogou)
	}
	if strings.Join(page.Unresponsive, ",") != "爱奇艺,unknown" || strings.Join(page.Suggestions, ",") != "流浪地球 2" {
		t.Fatalf("unresponsive %v, suggestions %v", page.Unresponsive, page.Suggestions)
	}
}

func TestTorrentsNeedTheirSwitchAndKeepTheirLinks(t *testing.T) {
	backend := &recordingBackend{responses: map[string]Response{"piratebay": {Results: []RawResult{{
		URL: "https://thepiratebay.org/description.php?id=5421757", Title: "Big.Buck.Bunny.1080p", Engines: []string{"piratebay"},
		MagnetLink: "magnet:?xt=urn:btih:9EE3&dn=Big.Buck.Bunny", Filesize: "885.68 MB", Seed: "1", Leech: 0.0,
	}, {
		URL: "https://example.org/t", Title: "no magnet", Engines: []string{"piratebay"}, MagnetLink: "javascript:x", Filesize: 1024.0,
	}}}}}
	service := newTestService(t, backend, NewMemorySwitch(true))
	ctx := context.Background()
	query := Query{Text: "big buck bunny", Category: Torrents}
	if _, err := search(service, "u1", query); !errors.Is(err, ErrTorrentsDisabled) {
		t.Fatalf("search with the tab off = %v", err)
	}
	if _, err := service.SetSettings(ctx, Settings{Enabled: true, Torrents: true}); err != nil {
		t.Fatal(err)
	}
	page, err := search(service, "u1", query)
	if err != nil {
		t.Fatal(err)
	}
	first, second := page.Results[0], page.Results[1]
	if first.Magnet == "" || *first.SizeBytes != 928702791 || *first.Seeders != 1 || *first.Leechers != 0 {
		t.Fatalf("first = %+v", first)
	}
	if second.Magnet != "" || *second.SizeBytes != 1024 || second.Seeders != nil {
		t.Fatalf("second = %+v", second)
	}
}

func TestResultsComeAsTheEnginesAnswer(t *testing.T) {
	result := func(engine string, n int) RawResult {
		return RawResult{URL: fmt.Sprintf("https://example.org/%s/%d", engine, n), Title: engine, Engine: engine, Positions: []int{n}}
	}
	quark, yandex := make(chan struct{}), make(chan struct{})
	backend := &recordingBackend{
		responses: map[string]Response{
			"baidu":     {Results: []RawResult{result("baidu", 1), result("baidu", 2)}},
			"sogou":     {Results: []RawResult{result("sogou", 1)}, Suggestions: []string{"天气预报"}},
			"360search": {Unresponsive: [][]string{{"360search", "CAPTCHA"}}},
			// 夸克 also finds 百度's first result, by another spelling.
			"quark": {Results: []RawResult{
				{URL: "http://www.example.org/baidu/1/", Engine: "quark", Positions: []int{1}}, result("quark", 2),
			}, Suggestions: []string{"天气预报", "天气"}},
		},
		failures: map[string]error{"yandex": errors.New("connection reset")},
		hold:     map[string]chan struct{}{"quark": quark, "yandex": yandex},
	}
	service := newTestService(t, backend, NewMemorySwitch(true))
	service.firstUpdate, service.updateGap = 50*time.Millisecond, 10*time.Millisecond
	var got []string
	err := service.Search(context.Background(), "u1", Query{Text: "天气", Category: Web}, func(update Update) error {
		var results, found []string
		for _, r := range update.Results {
			results = append(results, strings.TrimPrefix(r.URL, "https://example.org/"))
		}
		for _, f := range update.Found {
			found = append(found, strings.TrimPrefix(f.URL, "https://example.org/")+":"+strings.Join(f.Engines, "、"))
		}
		got = append(got, fmt.Sprintf("results=%s found=%s unresponsive=%s suggestions=%s pending=%d",
			strings.Join(results, " "), strings.Join(found, " "), strings.Join(update.Unresponsive, "、"),
			strings.Join(update.Suggestions, "、"), update.Pending))
		// The slow engines answer only after the window has shown the others.
		switch len(got) {
		case 1:
			close(quark)
		case 2:
			close(yandex)
		}
		return nil
	})
	want := []string{
		"results=baidu/1 sogou/1 baidu/2 found= unresponsive=360 搜索 suggestions=天气预报 pending=2",
		"results=quark/2 found=baidu/1:百度、夸克 unresponsive= suggestions=天气 pending=1",
		"results= found= unresponsive=Yandex suggestions= pending=0",
	}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("updates: %v\n%s\nwant\n%s", err, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestAServiceThatDoesNotAnswerIsUnavailable(t *testing.T) {
	down := errors.New("connection refused")
	backend := &recordingBackend{failures: map[string]error{}}
	for _, name := range []string{"baidu", "sogou", "360search", "quark", "yandex"} {
		backend.failures[name] = down
	}
	service := newTestService(t, backend, NewMemorySwitch(true))
	page, err := search(service, "u1", Query{Text: "天气", Category: Web})
	if !errors.Is(err, ErrUnavailable) || len(page.updates) != 0 {
		t.Fatalf("search = %v with %d updates, want ErrUnavailable and none", err, len(page.updates))
	}
	// One engine that fails is only unresponsive.
	delete(backend.failures, "baidu")
	page, err = search(service, "u1", Query{Text: "天气", Category: Web})
	if err != nil || strings.Join(page.Unresponsive, "、") != "搜狗、360 搜索、夸克、Yandex" {
		t.Fatalf("search = %v, unresponsive %v", err, page.Unresponsive)
	}
}

func TestALeavingWindowStopsTheSearch(t *testing.T) {
	hold := make(chan struct{})
	defer close(hold)
	backend := &recordingBackend{hold: map[string]chan struct{}{}}
	for _, name := range []string{"baidu", "sogou", "360search", "quark", "yandex"} {
		backend.hold[name] = hold
	}
	service := newTestService(t, backend, NewMemorySwitch(true))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := service.Search(ctx, "u1", Query{Text: "天气", Category: Web}, func(Update) error { return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("search = %v, want it to end with the request", err)
	}
}

func TestEnginesFindTheSameResultBySpelling(t *testing.T) {
	same := resultKey("https://www.example.org/a/b/?x=1#top")
	for _, address := range []string{"http://example.org/a/b?x=1", "https://EXAMPLE.org/a/b/?x=1"} {
		if resultKey(address) != same {
			t.Errorf("%s is another result than %s", address, same)
		}
	}
	for _, address := range []string{"https://example.org/a/b?x=2", "https://example.org/a/c?x=1", "https://example.com/a/b?x=1"} {
		if resultKey(address) == same {
			t.Errorf("%s is the same result as %s", address, same)
		}
	}
}

func TestTheSwitchKeepsSearchesFromTheService(t *testing.T) {
	backend := &recordingBackend{}
	power := NewMemorySwitch(false)
	service := newTestService(t, backend, power)
	ctx := context.Background()
	if _, err := search(service, "u1", Query{Text: "天气", Category: Web}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("search = %v, want ErrDisabled", err)
	}
	signature := service.sign("https://example.org/a.png")
	if _, err := service.Thumbnail(ctx, "https://example.org/a.png", signature); !errors.Is(err, ErrDisabled) {
		t.Fatalf("thumbnail = %v, want ErrDisabled", err)
	}
	if len(backend.forms) != 0 || len(backend.images) != 0 {
		t.Fatalf("the stopped service was asked: %v %v", backend.forms, backend.images)
	}
	status, err := service.Status(ctx)
	if err != nil || status.Enabled || len(status.Categories) != 0 {
		t.Fatalf("status = %+v, %v", status, err)
	}
}

func TestThumbnailsCarrySearXNGsOwnSignature(t *testing.T) {
	backend := &recordingBackend{}
	service := newTestService(t, backend, NewMemorySwitch(true))
	// hmac.new(b"test secret", b"https://example.org/a.png", hashlib.sha256).hexdigest(),
	// as searx.webutils.new_hmac computes it.
	const want = "250e373a32d4086dc9dc8adf53797b98adb99632e970e3d890453357624e1b8f"
	if got := service.sign("https://example.org/a.png"); got != want {
		t.Fatalf("signature = %s, want %s", got, want)
	}
	if got := service.thumbnail("https://example.org/a.png"); got != "/thumb?h="+want+"&url=https%3A%2F%2Fexample.org%2Fa.png" {
		t.Fatalf("thumbnail = %s", got)
	}
	if service.thumbnail("data:image/png;base64,AAAA") != "" || service.thumbnail("ftp://example.org/a.png") != "" {
		t.Fatal("only web addresses get thumbnails")
	}
	ctx := context.Background()
	if _, err := service.Thumbnail(ctx, "https://example.org/b.png", want); !errors.Is(err, ErrNotSigned) {
		t.Fatalf("another address with this signature = %v", err)
	}
	if _, err := service.Thumbnail(ctx, "https://example.org/a.png", want); err != nil || backend.images[0] != "https://example.org/a.png "+want {
		t.Fatalf("thumbnail = %v, asked %v", err, backend.images)
	}
}

func TestEachAccountSearchesAtItsOwnPace(t *testing.T) {
	service := newTestService(t, &recordingBackend{}, NewMemorySwitch(true))
	for i := range searchBurst {
		if _, err := search(service, "u1", Query{Text: "天气", Category: Web}); err != nil {
			t.Fatalf("search %d = %v", i+1, err)
		}
	}
	if _, err := search(service, "u1", Query{Text: "天气", Category: Web}); !errors.Is(err, ErrTooFrequent) {
		t.Fatalf("search %d = %v, want ErrTooFrequent", searchBurst+1, err)
	}
	if _, err := search(service, "u2", Query{Text: "天气", Category: Web}); err != nil {
		t.Fatalf("another account = %v", err)
	}
}

func TestQueriesTheWindowWouldNotSendAreRefused(t *testing.T) {
	service := newTestService(t, &recordingBackend{}, NewMemorySwitch(true))
	for _, query := range []Query{
		{Text: " ", Category: Web},
		{Text: strings.Repeat("字", MaxQueryLength+1), Category: Web},
		{Text: "x", Category: "maps"},
		{Text: "x", Category: Web, Page: MaxPage + 1},
		{Text: "x", Category: News, TimeRange: "decade"},
	} {
		if _, err := search(service, "u1", query); !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("%+v = %v, want ErrInvalidQuery", query, err)
		}
	}
}

// countingSwitch counts the reads of the switch and can refuse to change it.
type countingSwitch struct {
	*MemorySwitch
	reads   int
	refuses bool
}

func (c *countingSwitch) WebSearch(ctx context.Context) (ServiceState, error) {
	c.reads++
	return c.MemorySwitch.WebSearch(ctx)
}

func (c *countingSwitch) SetWebSearch(ctx context.Context, enabled bool) (ServiceState, error) {
	if c.refuses {
		return ServiceState{}, errors.New("the Host Agent is restarting")
	}
	return c.MemorySwitch.SetWebSearch(ctx, enabled)
}

func TestThumbnailsReuseARecentReadOfTheSwitch(t *testing.T) {
	power := &countingSwitch{MemorySwitch: NewMemorySwitch(true)}
	service := newTestService(t, &recordingBackend{}, power)
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return clock }
	ctx := context.Background()
	address := "https://example.org/a.png"
	for range 30 {
		if _, err := service.Thumbnail(ctx, address, service.sign(address)); err != nil {
			t.Fatal(err)
		}
	}
	if power.reads != 1 {
		t.Fatalf("a page of thumbnails read the switch %d times, want once", power.reads)
	}
	clock = clock.Add(switchMaxAge)
	if _, err := service.Thumbnail(ctx, address, service.sign(address)); err != nil || power.reads != 2 {
		t.Fatalf("after %v: %v, %d reads", switchMaxAge, err, power.reads)
	}
	// A search reads the switch afresh, and turning it off is seen at once.
	if _, err := search(service, "u1", Query{Text: "天气", Category: Web}); err != nil || power.reads != 3 {
		t.Fatalf("search: %v, %d reads", err, power.reads)
	}
	if _, err := service.SetSettings(ctx, Settings{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Thumbnail(ctx, address, service.sign(address)); !errors.Is(err, ErrDisabled) {
		t.Fatalf("thumbnail after turning off = %v, want ErrDisabled", err)
	}
}

func TestNothingChangesWhenTheSwitchCannotBeSet(t *testing.T) {
	power := &countingSwitch{MemorySwitch: NewMemorySwitch(true), refuses: true}
	service := newTestService(t, &recordingBackend{}, power)
	ctx := context.Background()
	if _, err := service.SetSettings(ctx, Settings{Enabled: false, Torrents: true}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("SetSettings = %v, want ErrUnavailable", err)
	}
	if torrents, err := service.settings.Torrents(ctx); err != nil || torrents {
		t.Fatalf("torrent tab = %v, %v; want it left off", torrents, err)
	}
}

func TestDatesWithoutAZoneAreThisMachinesTime(t *testing.T) {
	evening := "2026-10-30T20:00:00"
	if got := publishedAt(&evening); got == nil || !got.Equal(time.Date(2026, 10, 30, 20, 0, 0, 0, time.Local)) {
		t.Fatalf("naive date = %v, want 20:00 local time", got)
	}
	zoned := "2026-10-30T20:00:00+00:00"
	if got := publishedAt(&zoned); got == nil || !got.Equal(time.Date(2026, 10, 30, 20, 0, 0, 0, time.UTC)) {
		t.Fatalf("zoned date = %v", got)
	}
	for _, text := range []string{"None", "", "1970-01-01T00:00:00"} {
		if got := publishedAt(&text); got != nil {
			t.Errorf("%q = %v, want no date", text, got)
		}
	}
}

func TestSizesAreReadInPowersOf1024(t *testing.T) {
	for text, want := range map[string]int64{
		"885.68 MB": 928702791, "1.5GiB": 1610612736, "700 KB": 716800, "12 B": 12, "3 tb": 3 << 40, "42": 42,
	} {
		if got := size(text); got == nil || *got != want {
			t.Errorf("%q = %v, want %d", text, got, want)
		}
	}
	for _, text := range []string{"", "MB", "1,5 GB", "2 PB", "-3 MB"} {
		if got := size(text); got != nil {
			t.Errorf("%q = %d, want no size", text, *got)
		}
	}
}

func TestTabsFollowSearXNGsCategories(t *testing.T) {
	tabs := newCatalog([]EngineInfo{
		{Name: "wikipedia", Categories: []string{"general"}}, {Name: "baidu", Categories: []string{"general"}},
		{Name: "startpage images", Categories: []string{"images", "web"}},
		{Name: "youtube", Categories: []string{"videos", "music"}},
		{Name: "chefkoch"}, {Name: "kickass", Categories: []string{"files"}},
		{Name: "wikinews", Categories: []string{"news", "wikimedia"}},
		{Name: "arxiv", Categories: []string{"science", "scientific publications"}},
		{Name: "crates.io", Categories: []string{"it", "packages", "cargo"}},
		{Name: "alpha", Categories: []string{"general", "blogs"}}, {Name: "bing", Categories: []string{"general", "web"}},
	})
	for category, want := range map[Category]string{
		// 百度 and 必应 first, 维基百科 after the large engines, then by name.
		Web: "baidu,bing,wikipedia,alpha,chefkoch", Images: "startpage images", Videos: "youtube", Music: "youtube",
		News: "wikinews", Science: "arxiv", IT: "crates.io", Torrents: "kickass", Map: "", Social: "",
	} {
		if got := strings.Join(tabs[category], ","); got != want {
			t.Errorf("%s = %q, want %q", category, got, want)
		}
	}
	if got := strings.Join(tabs.labels(Web), "、"); got != "百度、必应、维基百科、alpha、chefkoch" {
		t.Errorf("labels = %s", got)
	}
}

func TestEnginesTakeTurns(t *testing.T) {
	result := func(engine string, positions ...int) RawResult {
		return RawResult{URL: "https://example.org/" + engine, Engine: engine, Positions: positions}
	}
	// As SearXNG grouped them: all of AcFun's first.
	results := []RawResult{
		result("acfun", 1), result("acfun", 3), result("acfun", 2), result("acfun", 4),
		result("bilibili", 1, 2), result("bilibili", 2), result("youtube", 1), result("odd"),
	}
	var got []string
	for _, raw := range interleave(results, []string{"bilibili", "iqiyi", "acfun", "youtube"}) {
		got = append(got, fmt.Sprint(raw.Engine, raw.Positions))
	}
	want := "bilibili[1 2] acfun[1] youtube[1] odd[] bilibili[2] acfun[2] acfun[3] acfun[4]"
	if strings.Join(got, " ") != want {
		t.Fatalf("order = %s\nwant    %s", strings.Join(got, " "), want)
	}
}

func TestTheEnginesAreReadFromSearXNGAndKept(t *testing.T) {
	backend := &recordingBackend{}
	service := newTestService(t, backend, NewMemorySwitch(true))
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return clock }
	ctx := context.Background()
	status, err := service.Status(ctx)
	if err != nil || strings.Join(status.Engines[Videos], "、") != "哔哩哔哩、爱奇艺、AcFun、搜狗视频、360 视频、YouTube、Dailymotion" ||
		strings.Join(status.Engines[Music], "、") != "YouTube" || len(status.Engines[Map]) != 0 {
		t.Fatalf("status = %+v, %v", status.Engines, err)
	}
	for range 3 {
		if _, err := search(service, "u1", Query{Text: "天气", Category: Web}); err != nil {
			t.Fatal(err)
		}
	}
	if backend.engineReads != 1 {
		t.Fatalf("read /config %d times, want once", backend.engineReads)
	}
	// After ten minutes it is read again; a failed read keeps the engines it had.
	clock = clock.Add(catalogMaxAge)
	backend.failing = true
	if _, err := search(service, "u1", Query{Text: "天气", Category: Web}); err != nil || backend.engineReads != 2 {
		t.Fatalf("search while SearXNG restarts: %v, %d reads", err, backend.engineReads)
	}
	if _, err := search(service, "u1", Query{Text: "天气", Category: Web}); err != nil || backend.engineReads != 2 {
		t.Fatalf("right after a failed read: %v, %d reads", err, backend.engineReads)
	}
	clock = clock.Add(catalogRetry)
	backend.failing = false
	if _, err := search(service, "u1", Query{Text: "天气", Category: Web}); err != nil || backend.engineReads != 3 {
		t.Fatalf("after %v: %v, %d reads", catalogRetry, err, backend.engineReads)
	}
}

func TestNothingIsSearchedWithoutTheEngines(t *testing.T) {
	backend := &recordingBackend{failing: true}
	service := newTestService(t, backend, NewMemorySwitch(true))
	ctx := context.Background()
	if _, err := search(service, "u1", Query{Text: "天气", Category: Web}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("search = %v, want ErrUnavailable", err)
	}
	if status, err := service.Status(ctx); err != nil || len(status.Engines[Web]) != 0 || len(status.Categories) == 0 {
		t.Fatalf("status = %+v, %v", status, err)
	}
	// A tab SearXNG loaded no engine for has no results, and asks nothing.
	backend.failing = false
	page, err := search(service, "u1", Query{Text: "天气", Category: Map})
	if err != nil || len(page.updates) != 1 || page.updates[0].Pending != 0 || len(page.Results) != 0 || len(backend.forms) != 0 {
		t.Fatalf("map tab: %+v, %v, asked %v", page, err, backend.forms)
	}
}
