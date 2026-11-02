package websearch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Fake answers like SearXNG without reaching any engine, for development and
// the Fake host state: a few results per engine, built from the query, with
// Bing's engines reported as unresponsive.
type Fake struct {
	// Delay is how long an engine takes to answer, so that the window can be
	// seen filling in; none when nil.
	Delay func(engine string) time.Duration
}

// FakeDelay makes each engine answer within two seconds, at its own pace.
func FakeDelay(engine string) time.Duration {
	sum := sha256.Sum256([]byte(engine))
	return time.Duration(sum[0]) * 8 * time.Millisecond
}

// fakeEngines are a few of SearXNG's engines for every tab.
var fakeEngines = []EngineInfo{
	{Name: "baidu", Categories: []string{"general"}}, {Name: "bing", Categories: []string{"general", "web"}},
	{Name: "sogou", Categories: []string{"general"}}, {Name: "steam"},
	{Name: "baidu images", Categories: []string{"images"}}, {Name: "bing images", Categories: []string{"images", "web"}},
	{Name: "bilibili", Categories: []string{"videos"}}, {Name: "youtube", Categories: []string{"videos", "music"}},
	{Name: "sogou wechat", Categories: []string{"news"}}, {Name: "bing news", Categories: []string{"news", "web"}},
	{Name: "soundcloud", Categories: []string{"music"}}, {Name: "openstreetmap", Categories: []string{"map"}},
	{Name: "arxiv", Categories: []string{"science", "scientific publications"}},
	{Name: "github", Categories: []string{"it", "repos"}}, {Name: "stackoverflow", Categories: []string{"it", "q&a"}},
	{Name: "mastodon users", Categories: []string{"social media"}},
	{Name: "piratebay", Categories: []string{"files"}}, {Name: "nyaa", Categories: []string{"files"}},
}

func (Fake) Engines(context.Context) ([]EngineInfo, error) { return fakeEngines, nil }

// Search answers for the one engine the Service asks at a time.
func (f Fake) Search(ctx context.Context, form url.Values) (Response, error) {
	name := form.Get("engines")
	if f.Delay != nil {
		select {
		case <-time.After(f.Delay(name)):
		case <-ctx.Done():
			return Response{}, ctx.Err()
		}
	}
	query := form.Get("q")
	page, _ := strconv.Atoi(form.Get("pageno"))
	response := Response{Suggestions: []string{query + " 是什么", query + " 教程"}}
	if strings.HasPrefix(name, "bing") {
		response.Unresponsive = [][]string{{name, "timeout"}}
		return response, nil
	}
	var categories []string
	for _, info := range fakeEngines {
		if info.Name == name {
			categories = info.Categories
		}
	}
	for i := range 4 {
		n := (page-1)*4 + i + 1
		published := fmt.Sprintf("2026-10-%02dT08:00:00", 1+n%28)
		result := RawResult{
			URL:   fmt.Sprintf("https://example.org/%s/%s/%d", url.PathEscape(name), url.PathEscape(query), n),
			Title: fmt.Sprintf("%s：%s的第 %d 条结果", query, label(name), n), Content: "这是开发模式的示例结果，没有访问任何搜索引擎。",
			Engine: name, Engines: []string{name}, Positions: []int{i + 1},
			PublishedDate: &published, Thumbnail: fmt.Sprintf("https://images.example.org/%s-%d.png", url.PathEscape(name), n),
		}
		switch {
		case slices.Contains(categories, "images"):
			result.ThumbnailSrc, result.Resolution, result.Source = result.Thumbnail, "1200 x 800", "example.org"
		case slices.Contains(categories, "videos"):
			result.Length, result.Author = strconv.Itoa(60+n*37), "示例作者"
		case slices.Contains(categories, "files"):
			result.MagnetLink = fmt.Sprintf("magnet:?xt=urn:btih:%040d&dn=example-%d", n, n)
			result.Filesize, result.Seed, result.Leech = fmt.Sprintf("%d.5 MB", 100*n), strconv.Itoa(20-n), strconv.Itoa(n)
			result.Thumbnail = ""
		}
		response.Results = append(response.Results, result)
	}
	if page == 1 && name == "baidu" {
		response.Infoboxes = []RawInfobox{{Infobox: query, Content: "开发模式下的示例摘要。", ID: "https://example.org/wiki"}}
	}
	return response, nil
}

// Thumbnail draws a small picture whose color follows the address.
func (Fake) Thumbnail(_ context.Context, imageURL, _ string) (Image, error) {
	sum := sha256.Sum256([]byte(imageURL))
	picture := image.NewRGBA(image.Rect(0, 0, 160, 100))
	fill := color.RGBA{R: sum[0], G: sum[1], B: sum[2], A: 255}
	for y := range 100 {
		for x := range 160 {
			picture.Set(x, y, fill)
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, picture); err != nil {
		return Image{}, err
	}
	return Image{ContentType: "image/png", Body: encoded.Bytes()}, nil
}

var _ Backend = Fake{}

// MemorySwitch holds the administrator's switch in memory, for development.
type MemorySwitch struct {
	mu      sync.Mutex
	enabled bool
}

func NewMemorySwitch(enabled bool) *MemorySwitch { return &MemorySwitch{enabled: enabled} }

func (m *MemorySwitch) WebSearch(context.Context) (ServiceState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stateLocked(), nil
}

func (m *MemorySwitch) SetWebSearch(_ context.Context, enabled bool) (ServiceState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enabled = enabled
	return m.stateLocked(), nil
}

func (m *MemorySwitch) stateLocked() ServiceState {
	if m.enabled {
		return ServiceState{Enabled: true, Running: StateRunning}
	}
	return ServiceState{Enabled: false, Running: StateStopped}
}

var _ Switch = (*MemorySwitch)(nil)
