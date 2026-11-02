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
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/time/rate"

	"github.com/zhongwater123/A-NAS/internal/websearchswitch"
)

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
	known := slices.Contains(Categories, q.Category)
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

// form is the POST body SearXNG receives: the tab's engines by name, which
// SearXNG asks whether or not they are among its defaults, and never a
// category, which would ask only those.
func (q Query) form(names []string) url.Values {
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
	// Engines lists the engines SearXNG loaded, from its /config.
	Engines(ctx context.Context) ([]EngineInfo, error)
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
// the service; Switch turns it on and off, and the Host Agent holds it.
type (
	ServiceState = websearchswitch.State
	Switch       = websearchswitch.Switch
)

// The states ServiceState.Running takes.
const (
	StateRunning  = websearchswitch.Running
	StateStarting = websearchswitch.Starting
	StateStopped  = websearchswitch.Stopped
	StateFailed   = websearchswitch.Failed
)

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

// switchMaxAge is how long thumbnails trust the last read of the switch: a
// page of results loads dozens at once, and each read is a Host Agent call
// that runs systemctl.
const switchMaxAge = 5 * time.Second

// The engines SearXNG loaded change only with a release, so they are read
// again every ten minutes, or half a minute after a read failed.
const (
	catalogMaxAge = 10 * time.Minute
	catalogRetry  = 30 * time.Second
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
	// The switch as last read, which thumbnails reuse for switchMaxAge.
	stateMu   sync.Mutex
	lastState ServiceState
	readAt    time.Time
	now       func() time.Time
	// The engines as last read from SearXNG.
	catalogMu sync.Mutex
	engines   catalog
	enginesAt time.Time
	// When the updates of a search go out (stream.go).
	firstUpdate, updateGap time.Duration
}

func New(options Options) *Service {
	return &Service{
		backend: options.Backend, power: options.Switch, settings: options.Settings,
		secret: options.Secret, thumbnailPath: options.ThumbnailPath, limiters: map[string]*rate.Limiter{},
		now: time.Now, firstUpdate: firstUpdate, updateGap: updateGap,
	}
}

// Status is what the desktop needs to know: whether to show web search and
// its torrent tab.
type Status struct {
	ServiceState
	Torrents   bool       `json:"torrents"`
	Categories []Category `json:"categories"`
	// Engines are every tab's engines, as the window names them; empty until
	// SearXNG has been read while it ran.
	Engines map[Category][]string `json:"engines"`
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
	status := Status{ServiceState: state, Torrents: torrents, Categories: []Category{}, Engines: map[Category][]string{}}
	// A stopped service has no engines to list; the last read stands.
	engines := s.cachedCatalog()
	if state.Enabled {
		if read, err := s.catalog(ctx); err == nil {
			engines = read
		}
	}
	for _, category := range Categories {
		status.Engines[category] = engines.labels(category)
	}
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
	s.remember(state)
	return state, nil
}

func (s *Service) remember(state ServiceState) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.lastState, s.readAt = state, s.now()
}

// recentState is the switch as read within switchMaxAge, or read again.
func (s *Service) recentState(ctx context.Context) (ServiceState, error) {
	s.stateMu.Lock()
	state, fresh := s.lastState, !s.readAt.IsZero() && s.now().Sub(s.readAt) < switchMaxAge
	s.stateMu.Unlock()
	if fresh {
		return state, nil
	}
	return s.state(ctx)
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

// SetSettings turns web search and its torrent tab on or off. The Host
// Agent's switch goes first: when it cannot be reached, nothing changes.
func (s *Service) SetSettings(ctx context.Context, settings Settings) (Status, error) {
	if s.settings == nil || s.power == nil {
		return Status{}, ErrUnavailable
	}
	state, err := s.power.SetWebSearch(ctx, settings.Enabled)
	if err != nil {
		return Status{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	s.remember(state)
	if err := s.settings.SetTorrents(ctx, settings.Torrents); err != nil {
		return Status{}, err
	}
	return s.Status(ctx)
}

// catalog is the engines SearXNG loaded, read again once catalogMaxAge has
// passed. A failed read keeps the last one and tries again after
// catalogRetry; with none to keep, the service is unavailable.
func (s *Service) catalog(ctx context.Context) (catalog, error) {
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()
	if s.engines != nil && s.now().Sub(s.enginesAt) < catalogMaxAge {
		return s.engines, nil
	}
	if s.backend == nil {
		return nil, ErrUnavailable
	}
	infos, err := s.backend.Engines(ctx)
	if err != nil {
		if s.engines != nil {
			s.enginesAt = s.now().Add(catalogRetry - catalogMaxAge)
			return s.engines, nil
		}
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	s.engines, s.enginesAt = newCatalog(infos), s.now()
	return s.engines, nil
}

func (s *Service) cachedCatalog() catalog {
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()
	return s.engines
}

// Thumbnail fetches a thumbnail this service signed.
func (s *Service) Thumbnail(ctx context.Context, imageURL, signature string) (Image, error) {
	if len(s.secret) == 0 || !hmac.Equal([]byte(signature), []byte(s.sign(imageURL))) {
		return Image{}, ErrNotSigned
	}
	state, err := s.recentState(ctx)
	if err != nil {
		return Image{}, err
	}
	if !state.Enabled {
		return Image{}, ErrDisabled
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
