// Package websearch is A-NAS web search (docs/specs/web-search.md, ADR 0030):
// it asks the built-in SearXNG service for one tab's engines, maps SearXNG's
// results to the product's shape, signs the thumbnail addresses SearXNG will
// fetch, and limits how fast each account searches. The Host Agent holds the
// administrator's switch, which stops the service; the torrent tab's switch
// is the Product Service's own.
package websearch

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/time/rate"
)

// Category is a tab of the web search window.
type Category string

const (
	Web      Category = "web"
	Images   Category = "images"
	Videos   Category = "videos"
	News     Category = "news"
	Torrents Category = "torrents"
)

// Categories are the tabs in the order the window shows them.
var Categories = []Category{Web, Images, Videos, News, Torrents}

// engine is one SearXNG engine: its name in deploy/search/settings.yml and
// the name the window shows. A platform's engine names the site a result
// plays on; an aggregator's results are named by their address.
type engine struct {
	name, label string
	platform    bool
}

// engines are each tab's engines, which deploy/search/settings.yml keeps
// loaded; the list follows the measurements on the Experimental NAS
// (docs/research/web-search-engines.md).
var engines = map[Category][]engine{
	Web: {
		{name: "baidu", label: "百度"}, {name: "bing", label: "必应"}, {name: "sogou", label: "搜狗"},
		{name: "360search", label: "360 搜索"}, {name: "quark", label: "夸克"}, {name: "yandex", label: "Yandex"},
	},
	Images: {
		{name: "baidu images", label: "百度图片"}, {name: "bing images", label: "必应图片"},
		{name: "sogou images", label: "搜狗图片"}, {name: "quark images", label: "夸克图片"},
		{name: "yandex images", label: "Yandex 图片"},
	},
	Videos: {
		{name: "bilibili", label: "哔哩哔哩", platform: true}, {name: "iqiyi", label: "爱奇艺", platform: true},
		{name: "acfun", label: "AcFun", platform: true}, {name: "sogou videos", label: "搜狗视频"},
		{name: "360search videos", label: "360 视频"}, {name: "bing videos", label: "必应视频"},
		{name: "youtube", label: "YouTube", platform: true}, {name: "dailymotion", label: "Dailymotion", platform: true},
	},
	News: {{name: "bing news", label: "必应资讯"}, {name: "sogou wechat", label: "搜狗微信"}},
	Torrents: {
		{name: "piratebay", label: "海盗湾"}, {name: "1337x", label: "1337x"}, {name: "nyaa", label: "nyaa"},
		{name: "bt4g", label: "BT4G"}, {name: "btdigg", label: "BTDigg"}, {name: "solidtorrents", label: "SolidTorrents"},
		{name: "kickass", label: "Kickass"}, {name: "tokyotoshokan", label: "TokyoToshokan"},
	},
}

// Engines are the names the window shows for a tab's engines, in order.
func Engines(category Category) []string {
	labels := make([]string, 0, len(engines[category]))
	for _, e := range engines[category] {
		labels = append(labels, e.label)
	}
	return labels
}

func lookupEngine(name string) (engine, bool) {
	for _, list := range engines {
		for _, e := range list {
			if e.name == name {
				return e, true
			}
		}
	}
	return engine{}, false
}

// TimeRange limits the news tab to recent results.
type TimeRange string

const (
	AnyTime TimeRange = ""
	Day     TimeRange = "day"
	Week    TimeRange = "week"
	Month   TimeRange = "month"
	Year    TimeRange = "year"
)

// MaxQueryLength bounds a search in characters; engines cut longer ones.
const MaxQueryLength = 400

// MaxPage is the last page the window asks for.
const MaxPage = 20

// Query is one search of one tab.
type Query struct {
	Text      string    `json:"q"`
	Category  Category  `json:"category"`
	Page      int       `json:"page"`
	TimeRange TimeRange `json:"timeRange,omitempty"`
}

// ErrInvalidQuery rejects a query the window would not send.
var ErrInvalidQuery = errors.New("invalid web search query")

func (q *Query) validate() error {
	q.Text = strings.TrimSpace(q.Text)
	if q.Page == 0 {
		q.Page = 1
	}
	_, known := engines[q.Category]
	switch {
	case q.Text == "" || utf8.RuneCountInString(q.Text) > MaxQueryLength || !utf8.ValidString(q.Text):
		return fmt.Errorf("%w: the text is empty or too long", ErrInvalidQuery)
	case !known:
		return fmt.Errorf("%w: unknown category %q", ErrInvalidQuery, q.Category)
	case q.Page < 1 || q.Page > MaxPage:
		return fmt.Errorf("%w: page %d", ErrInvalidQuery, q.Page)
	}
	switch q.TimeRange {
	case AnyTime, Day, Week, Month, Year:
	default:
		return fmt.Errorf("%w: unknown time range %q", ErrInvalidQuery, q.TimeRange)
	}
	return nil
}

// form is the POST body SearXNG receives: only this tab's engines, since
// naming the category as well would ask every engine of it.
func (q Query) form() url.Values {
	names := make([]string, 0, len(engines[q.Category]))
	for _, e := range engines[q.Category] {
		names = append(names, e.name)
	}
	form := url.Values{
		"q": {q.Text}, "format": {"json"}, "engines": {strings.Join(names, ",")},
		"pageno": {strconv.Itoa(q.Page)}, "language": {"zh-CN"}, "safesearch": {"1"},
	}
	if q.TimeRange != AnyTime {
		form.Set("time_range", string(q.TimeRange))
	}
	return form
}

// Backend is the SearXNG service: SearXNG over its socket in production, a
// fake in development.
type Backend interface {
	// Search posts form to SearXNG's /search and decodes its JSON answer.
	Search(ctx context.Context, form url.Values) (Response, error)
	// Thumbnail fetches an image through SearXNG's image proxy, which
	// accepts only an address signed with the shared secret.
	Thumbnail(ctx context.Context, imageURL, signature string) (Image, error)
}

// Image is a thumbnail's bytes and media type.
type Image struct {
	ContentType string
	Body        []byte
}

// ServiceState is the administrator's switch and what systemd reports of
// the service.
type ServiceState struct {
	Enabled bool `json:"enabled"`
	// Running is running, starting, stopped or failed.
	Running string `json:"running"`
}

// The states Running takes.
const (
	StateRunning  = "running"
	StateStarting = "starting"
	StateStopped  = "stopped"
	StateFailed   = "failed"
)

// Switch turns the service on and off; the Host Agent holds it.
type Switch interface {
	WebSearch(ctx context.Context) (ServiceState, error)
	SetWebSearch(ctx context.Context, enabled bool) (ServiceState, error)
}

// Errors the API answers with their own codes.
var (
	ErrDisabled         = errors.New("web search is turned off")
	ErrTorrentsDisabled = errors.New("torrent search is turned off")
	ErrUnavailable      = errors.New("web search is unavailable")
	ErrTooFrequent      = errors.New("too many searches")
	ErrNotSigned        = errors.New("thumbnail address is not signed")
)

// The pace each account may search at: SearXNG asks every engine of the tab
// once per search, and the family shares one public address, which engines
// answer with a CAPTCHA when it searches like a robot.
const (
	searchInterval = 2 * time.Second
	searchBurst    = 10
)

// Options configures a Service.
type Options struct {
	Backend  Backend
	Switch   Switch
	Settings *Store
	// Secret is SearXNG's secret, which signs thumbnail addresses.
	Secret []byte
	// ThumbnailPath is where the API serves thumbnails.
	ThumbnailPath string
}

// Service answers the web search window.
type Service struct {
	backend       Backend
	power         Switch
	settings      *Store
	secret        []byte
	thumbnailPath string
	limitersMu    sync.Mutex
	limiters      map[string]*rate.Limiter
}

func New(options Options) *Service {
	return &Service{
		backend: options.Backend, power: options.Switch, settings: options.Settings,
		secret: options.Secret, thumbnailPath: options.ThumbnailPath, limiters: map[string]*rate.Limiter{},
	}
}

// Status is what the desktop needs to know: whether to show web search and
// its torrent tab.
type Status struct {
	ServiceState
	Torrents   bool       `json:"torrents"`
	Categories []Category `json:"categories"`
}

func (s *Service) Status(ctx context.Context) (Status, error) {
	state, err := s.state(ctx)
	if err != nil {
		return Status{}, err
	}
	torrents, err := s.torrents(ctx)
	if err != nil {
		return Status{}, err
	}
	status := Status{ServiceState: state, Torrents: torrents, Categories: []Category{}}
	if state.Enabled {
		for _, category := range Categories {
			if category != Torrents || torrents {
				status.Categories = append(status.Categories, category)
			}
		}
	}
	return status, nil
}

func (s *Service) state(ctx context.Context) (ServiceState, error) {
	if s.power == nil {
		return ServiceState{}, ErrUnavailable
	}
	state, err := s.power.WebSearch(ctx)
	if err != nil {
		return ServiceState{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return state, nil
}

func (s *Service) torrents(ctx context.Context) (bool, error) {
	if s.settings == nil {
		return false, nil
	}
	return s.settings.Torrents(ctx)
}

// Settings are the administrator's two switches.
type Settings struct {
	Enabled  bool `json:"enabled"`
	Torrents bool `json:"torrents"`
}

// SetSettings turns web search and its torrent tab on or off.
func (s *Service) SetSettings(ctx context.Context, settings Settings) (Status, error) {
	if s.settings == nil {
		return Status{}, ErrUnavailable
	}
	if err := s.settings.SetTorrents(ctx, settings.Torrents); err != nil {
		return Status{}, err
	}
	if s.power == nil {
		return Status{}, ErrUnavailable
	}
	if _, err := s.power.SetWebSearch(ctx, settings.Enabled); err != nil {
		return Status{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return s.Status(ctx)
}

// Search runs one search for account. The query reaches SearXNG only in
// the request body and is not logged.
func (s *Service) Search(ctx context.Context, account string, query Query) (Page, error) {
	if err := query.validate(); err != nil {
		return Page{}, err
	}
	if err := s.enabled(ctx); err != nil {
		return Page{}, err
	}
	if query.Category == Torrents {
		torrents, err := s.torrents(ctx)
		if err != nil {
			return Page{}, err
		}
		if !torrents {
			return Page{}, ErrTorrentsDisabled
		}
	}
	if !s.limiter(account).Allow() {
		return Page{}, ErrTooFrequent
	}
	if s.backend == nil {
		return Page{}, ErrUnavailable
	}
	response, err := s.backend.Search(ctx, query.form())
	if err != nil {
		return Page{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return s.page(query.Category, response), nil
}

// Thumbnail fetches a thumbnail this service signed.
func (s *Service) Thumbnail(ctx context.Context, imageURL, signature string) (Image, error) {
	if len(s.secret) == 0 || !hmac.Equal([]byte(signature), []byte(s.sign(imageURL))) {
		return Image{}, ErrNotSigned
	}
	if err := s.enabled(ctx); err != nil {
		return Image{}, err
	}
	if s.backend == nil {
		return Image{}, ErrUnavailable
	}
	image, err := s.backend.Thumbnail(ctx, imageURL, signature)
	if errors.Is(err, ErrNotFound) {
		return Image{}, err
	}
	if err != nil {
		return Image{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return image, nil
}

// enabled refuses while the administrator has turned web search off, so
// that nothing reaches the stopped service.
func (s *Service) enabled(ctx context.Context) error {
	state, err := s.state(ctx)
	if err != nil {
		return err
	}
	if !state.Enabled {
		return ErrDisabled
	}
	return nil
}

func (s *Service) limiter(account string) *rate.Limiter {
	s.limitersMu.Lock()
	defer s.limitersMu.Unlock()
	limiter := s.limiters[account]
	if limiter == nil {
		limiter = rate.NewLimiter(rate.Every(searchInterval), searchBurst)
		s.limiters[account] = limiter
	}
	return limiter
}

// sign is SearXNG's own signature of an image address (searx.webutils.new_hmac):
// HMAC-SHA256 of the address with the shared secret, in hex.
func (s *Service) sign(imageURL string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(imageURL))
	return hex.EncodeToString(mac.Sum(nil))
}

// thumbnail is where the window loads an image address from: this service's
// own path, so the desktop's content policy stays closed to other hosts.
func (s *Service) thumbnail(imageURL string) string {
	imageURL = absoluteImage(imageURL)
	if imageURL == "" || len(s.secret) == 0 {
		return ""
	}
	return s.thumbnailPath + "?" + url.Values{"url": {imageURL}, "h": {s.sign(imageURL)}}.Encode()
}

// ErrNotFound is an image the remote site does not have.
var ErrNotFound = errors.New("not found")
