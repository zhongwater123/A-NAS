package websearch

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

// recordingBackend answers with response and remembers what it was asked.
type recordingBackend struct {
	response Response
	forms    []url.Values
	images   []string
}

func (r *recordingBackend) Search(_ context.Context, form url.Values) (Response, error) {
	r.forms = append(r.forms, form)
	return r.response, nil
}

func (r *recordingBackend) Thumbnail(_ context.Context, imageURL, signature string) (Image, error) {
	r.images = append(r.images, imageURL+" "+signature)
	return Image{ContentType: "image/jpeg", Body: []byte("jpeg")}, nil
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

func TestSearchAsksOnlyTheTabsEnginesInTheBody(t *testing.T) {
	backend := &recordingBackend{}
	service := newTestService(t, backend, NewMemorySwitch(true))
	if _, err := service.Search(context.Background(), "u1", Query{Text: "  台风  ", Category: News, Page: 2, TimeRange: Week}); err != nil {
		t.Fatal(err)
	}
	form := backend.forms[0]
	if form.Get("q") != "台风" || form.Get("engines") != "bing news,sogou wechat" || form.Has("categories") ||
		form.Get("pageno") != "2" || form.Get("time_range") != "week" || form.Get("format") != "json" || form.Get("safesearch") != "1" {
		t.Fatalf("form = %v", form)
	}
}

func TestResultsAreMappedForEachTab(t *testing.T) {
	published := "2026-10-25T06:59:20"
	backend := &recordingBackend{response: Response{
		Results: []RawResult{
			{URL: "http://www.bilibili.com/video/av1", Title: "流浪地球3", Content: "-", Engine: "bilibili", Engines: []string{"bilibili"},
				PublishedDate: &published, Thumbnail: "//i1.hdslb.com/a.jpg", Length: "233.0", Author: "笙筌"},
			{URL: "https://www.youtube.com/watch?v=x", Title: "The Wandering Earth", Engines: []string{"youtube", "bing videos"},
				Thumbnail: "https://i.ytimg.com/vi/x/hq.jpg", Length: "2:16:29"},
			{URL: "https://v.sogou.com/vc/np?link=1", Title: "资讯", Engines: []string{"sogou videos"}, Length: 20.0},
			{URL: "javascript:alert(1)", Title: "dropped", Engines: []string{"acfun"}},
		},
		Unresponsive: [][]string{{"iqiyi", "unexpected crash"}, {"iqiyi", "timeout"}, {"unknown", "x"}},
		Suggestions:  []string{" 流浪地球 2 ", ""},
	}}
	service := newTestService(t, backend, NewMemorySwitch(true))
	page, err := service.Search(context.Background(), "u1", Query{Text: "流浪地球", Category: Videos})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 3 {
		t.Fatalf("results = %+v, want the javascript: address dropped", page.Results)
	}
	bilibili, youtube, sogou := page.Results[0], page.Results[1], page.Results[2]
	if bilibili.Platform != "哔哩哔哩" || bilibili.Content != "" || bilibili.Domain != "bilibili.com" || *bilibili.DurationSeconds != 233 ||
		bilibili.PublishedAt == nil || bilibili.PublishedAt.Day() != 25 ||
		!strings.Contains(bilibili.Thumbnail, url.QueryEscape("https://i1.hdslb.com/a.jpg")) {
		t.Fatalf("bilibili = %+v", bilibili)
	}
	if youtube.Platform != "YouTube" || *youtube.DurationSeconds != 8189 || strings.Join(youtube.Engines, ",") != "YouTube,必应视频" {
		t.Fatalf("youtube = %+v", youtube)
	}
	if sogou.Platform != "v.sogou.com" || *sogou.DurationSeconds != 20 || sogou.Thumbnail != "" {
		t.Fatalf("sogou = %+v", sogou)
	}
	if strings.Join(page.Unresponsive, ",") != "爱奇艺" || strings.Join(page.Suggestions, ",") != "流浪地球 2" {
		t.Fatalf("unresponsive %v, suggestions %v", page.Unresponsive, page.Suggestions)
	}
}

func TestTorrentsNeedTheirSwitchAndKeepTheirLinks(t *testing.T) {
	backend := &recordingBackend{response: Response{Results: []RawResult{{
		URL: "https://thepiratebay.org/description.php?id=5421757", Title: "Big.Buck.Bunny.1080p", Engines: []string{"piratebay"},
		MagnetLink: "magnet:?xt=urn:btih:9EE3&dn=Big.Buck.Bunny", Filesize: "885.68 MB", Seed: "1", Leech: 0.0,
	}, {
		URL: "https://example.org/t", Title: "no magnet", Engines: []string{"nyaa"}, MagnetLink: "javascript:x", Filesize: 1024.0,
	}}}}
	service := newTestService(t, backend, NewMemorySwitch(true))
	ctx := context.Background()
	query := Query{Text: "big buck bunny", Category: Torrents}
	if _, err := service.Search(ctx, "u1", query); !errors.Is(err, ErrTorrentsDisabled) {
		t.Fatalf("search with the tab off = %v", err)
	}
	if _, err := service.SetSettings(ctx, Settings{Enabled: true, Torrents: true}); err != nil {
		t.Fatal(err)
	}
	page, err := service.Search(ctx, "u1", query)
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

func TestTheSwitchKeepsSearchesFromTheService(t *testing.T) {
	backend := &recordingBackend{}
	power := NewMemorySwitch(false)
	service := newTestService(t, backend, power)
	ctx := context.Background()
	if _, err := service.Search(ctx, "u1", Query{Text: "天气", Category: Web}); !errors.Is(err, ErrDisabled) {
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
	ctx := context.Background()
	for i := range searchBurst {
		if _, err := service.Search(ctx, "u1", Query{Text: "天气", Category: Web}); err != nil {
			t.Fatalf("search %d = %v", i+1, err)
		}
	}
	if _, err := service.Search(ctx, "u1", Query{Text: "天气", Category: Web}); !errors.Is(err, ErrTooFrequent) {
		t.Fatalf("search %d = %v, want ErrTooFrequent", searchBurst+1, err)
	}
	if _, err := service.Search(ctx, "u2", Query{Text: "天气", Category: Web}); err != nil {
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
		if _, err := service.Search(context.Background(), "u1", query); !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("%+v = %v, want ErrInvalidQuery", query, err)
		}
	}
}
