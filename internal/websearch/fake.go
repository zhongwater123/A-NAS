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
	"strconv"
	"strings"
	"sync"
)

// Fake answers like SearXNG without reaching any engine, for development and
// the Fake host state: a few results per tab, built from the query, with one
// engine of each tab reported as unresponsive.
type Fake struct{}

func (Fake) Search(_ context.Context, form url.Values) (Response, error) {
	query := form.Get("q")
	page, _ := strconv.Atoi(form.Get("pageno"))
	names := strings.Split(form.Get("engines"), ",")
	response := Response{Suggestions: []string{query + " 是什么", query + " 教程"}}
	if len(names) > 1 {
		response.Unresponsive = [][]string{{names[len(names)-1], "timeout"}}
	}
	for i := range 8 {
		n := (page-1)*8 + i + 1
		name := names[i%max(len(names)-1, 1)]
		published := fmt.Sprintf("2026-10-%02dT08:00:00", 1+n%28)
		result := RawResult{
			URL: fmt.Sprintf("https://example.org/%s/%d", url.PathEscape(query), n), Engines: []string{name},
			Title: fmt.Sprintf("%s：第 %d 条结果", query, n), Content: "这是开发模式的示例结果，没有访问任何搜索引擎。",
			PublishedDate: &published, Thumbnail: fmt.Sprintf("https://images.example.org/%d.png", n),
		}
		switch {
		case strings.Contains(name, "images"):
			result.ThumbnailSrc, result.Resolution, result.Source = result.Thumbnail, "1200 x 800", "example.org"
		case strings.Contains(form.Get("engines"), "bilibili"):
			result.Length, result.Author = strconv.Itoa(60+n*37), "示例作者"
		case strings.Contains(form.Get("engines"), "piratebay"):
			result.MagnetLink = fmt.Sprintf("magnet:?xt=urn:btih:%040d&dn=example-%d", n, n)
			result.Filesize, result.Seed, result.Leech = fmt.Sprintf("%d.5 MB", 100*n), strconv.Itoa(20-n), strconv.Itoa(n)
			result.Thumbnail = ""
		}
		response.Results = append(response.Results, result)
	}
	if page == 1 && !strings.Contains(form.Get("engines"), "piratebay") {
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
