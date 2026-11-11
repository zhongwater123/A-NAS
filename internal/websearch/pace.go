package websearch

// The family shares one public address. A search site that sees it search
// faster than a person, or keep at it once challenged, blocks it, and the
// family's own phones and computers meet the CAPTCHA too. So each website is
// asked as one person at a keyboard would ask: a few searches in a row at
// once, as when refining one, then one every ten seconds at uneven moments,
// whoever in the family searches; the next page only where the last had
// results; the same page again from what it answered; and once its site
// blocks it, not for an hour, then two, up to a day.

import (
	"context"
	"errors"
	"maps"
	"math/rand/v2"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// pacing is how often a website is asked, across its engines and accounts.
type pacing struct {
	// Once burst requests have gone at once, an engine is asked every
	// interval; a request that waited for its turn goes up to jitter later.
	interval time.Duration
	burst    int
	jitter   time.Duration
	// maxWait is the longest a search waits for an engine's turn; past it,
	// the engine sits the search out.
	maxWait time.Duration
}

var humanPace = pacing{interval: 10 * time.Second, burst: 3, jitter: 3 * time.Second, maxWait: 20 * time.Second}

// errBusy is an engine whose turn is too far off.
var errBusy = errors.New("the engine was asked too recently")

type turnKey struct{ account, site string }

// turn reserves one website's turn. An account may hold only one future
// turn on that site, leaving the next one for other members. release must
// be called when that request completes or is abandoned.
func (s *Service) turn(ctx context.Context, account, engine string) (release func(), err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := turnKey{account: account, site: website(engine)}
	s.pacesMu.Lock()
	if s.waiting[key] {
		s.pacesMu.Unlock()
		return nil, errBusy
	}
	limiter := s.paces[key.site]
	if limiter == nil {
		limiter = rate.NewLimiter(rate.Every(s.pace.interval), s.pace.burst)
		s.paces[key.site] = limiter
	}
	reservation := limiter.Reserve()
	delay := reservation.Delay()
	if delay > s.pace.maxWait {
		reservation.Cancel()
		s.pacesMu.Unlock()
		return nil, errBusy
	}
	if delay == 0 {
		s.pacesMu.Unlock()
		return func() {}, nil
	}
	s.waiting[key] = true
	s.pacesMu.Unlock()
	release = func() {
		s.pacesMu.Lock()
		delete(s.waiting, key)
		s.pacesMu.Unlock()
	}
	if s.pace.jitter > 0 {
		//nolint:gosec // G404: the jitter only spreads an engine's requests in time; nothing rests on guessing it.
		delay += rand.N(s.pace.jitter)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		return release, nil
	case <-ctx.Done():
		reservation.Cancel()
		release()
		return nil, ctx.Err()
	}
}

// PauseReason is how a site blocked an engine.
type PauseReason string

const (
	PauseCaptcha    PauseReason = "captcha"
	PauseDenied     PauseReason = "denied"
	PauseTooMany    PauseReason = "too_many"
	PauseUnreadable PauseReason = "unreadable"
)

// blocks are the reasons SearXNG gives for an engine (searx/webutils.py, in
// English as settings.yml sets) that pause it. A page the engine cannot read
// is often a challenge it does not recognize. SearXNG's "Suspended: …" is its
// own pause after one of these, which says nothing new.
var blocks = map[string]PauseReason{
	"CAPTCHA": PauseCaptcha, "access denied": PauseDenied, "too many requests": PauseTooMany,
	"parsing error": PauseUnreadable, "unexpected crash": PauseUnreadable,
}

// The first pause, which each block in a row doubles up to the longest.
const (
	firstPause   = time.Hour
	longestPause = 24 * time.Hour
)

func pauseAfter(strikes int) time.Duration {
	pause := firstPause
	for range strikes - 1 {
		pause *= 2
		if pause >= longestPause {
			return longestPause
		}
	}
	return pause
}

// Pause leaves an engine out of searches until Until; Strikes count its
// blocks since it last answered.
type Pause struct {
	Reason  PauseReason
	Strikes int
	Until   time.Time
}

// PausedEngine names a paused website. The JSON field keeps its legacy name.
type PausedEngine struct {
	Engine string      `json:"engine"`
	Reason PauseReason `json:"reason"`
	Until  time.Time   `json:"until"`
}

func (p Pause) shown(name string) PausedEngine {
	return PausedEngine{Engine: label(name), Reason: p.Reason, Until: p.Until}
}

// pausedEngines are the engines paused now.
func (s *Service) pausedEngines(ctx context.Context) (map[string]Pause, error) {
	s.pausesMu.Lock()
	defer s.pausesMu.Unlock()
	if err := s.readPausesLocked(ctx); err != nil {
		return nil, err
	}
	paused := map[string]Pause{}
	for name, pause := range s.pauses {
		if pause.Until.After(s.now()) {
			paused[name] = pause
		}
	}
	return paused, nil
}

// readPausesLocked reads the pauses from the store the first time.
func (s *Service) readPausesLocked(ctx context.Context) error {
	if s.pauses != nil {
		return nil
	}
	if s.settings == nil {
		s.pauses = map[string]Pause{}
		return nil
	}
	pauses, err := s.settings.Pauses(ctx)
	if err != nil {
		return err
	}
	// Older releases persisted per-engine pauses. Merge siblings atomically
	// without adding their strikes or losing a still-active restriction.
	normalized := map[string]Pause{}
	changed := false
	for engine, pause := range pauses {
		site := website(engine)
		changed = changed || site != engine
		previous := normalized[site]
		strikes := max(previous.Strikes, pause.Strikes)
		if pause.Until.After(previous.Until) {
			previous = pause
		}
		previous.Strikes = strikes
		normalized[site] = previous
	}
	if changed {
		if err := s.settings.replacePauses(ctx, normalized); err != nil {
			return err
		}
	}
	s.pauses = normalized
	return nil
}

// failure is why engine gave no results, when its answer says.
func failure(engine string, response Response) (string, bool) {
	for _, pair := range response.Unresponsive {
		if len(pair) >= 2 && pair[0] == engine {
			return pair[1], true
		}
	}
	return "", false
}

// judge pauses an engine its site blocked, longer each time in a row, and
// forgets its blocks once it answers. A search that ends meanwhile still
// keeps the pause.
func (s *Service) judge(ctx context.Context, engine string, response Response) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	reason, failed := failure(engine, response)
	engine = website(engine)
	s.pausesMu.Lock()
	defer s.pausesMu.Unlock()
	err := s.readPausesLocked(ctx)
	pause, paused := s.pauses[engine]
	block, blocked := blocks[reason]
	switch {
	case err != nil:
	case paused && pause.Until.After(s.now()):
		// Sibling requests already in flight cannot extend or clear a pause.
	case !failed && paused:
		delete(s.pauses, engine)
		if s.settings != nil {
			err = s.settings.ClearPause(ctx, engine)
		}
	case blocked:
		pause = Pause{Reason: block, Strikes: pause.Strikes + 1}
		pause.Until = s.now().Add(pauseAfter(pause.Strikes))
		s.pauses[engine] = pause
		if s.settings != nil {
			err = s.settings.SetPause(ctx, engine, pause)
		}
	}
	if err != nil {
		s.logger.Error("web search cannot keep an engine's pause", "engine", engine, "error", err)
	}
}

// An account's searches are kept a while. The same page again, as a retry
// or going back asks for it, comes from what its engines answered; the next
// page asks only the engines that found something on this one. Accounts do
// not share them: how fast a search answered would tell one person what
// another had searched.
const (
	searchMaxAge = 10 * time.Minute
	keptSearches = 32
)

type searchKey struct {
	account, text string
	category      Category
	timeRange     TimeRange
}

// record is a search's answers, each page's by engine; an engine that did
// not answer has none.
type record struct {
	mu    sync.Mutex
	pages map[int]map[string]Response
}

// record is account's search of query, kept searchMaxAge from now.
func (s *Service) record(account string, query Query) *record {
	key := searchKey{account: account, text: query.Text, category: query.Category, timeRange: query.TimeRange}
	r, ok := s.searches.Get(key)
	if !ok {
		r = &record{pages: map[int]map[string]Response{}}
	}
	s.searches.Add(key, r)
	return r
}

// begin starts a search of page: it returns the engines' answers kept for
// the page, and, when the page before was searched, the engines that found
// something there.
func (r *record) begin(page int) (kept map[string]Response, found map[string]bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pages[page] == nil {
		r.pages[page] = map[string]Response{}
	}
	if before, ok := r.pages[page-1]; ok {
		found = map[string]bool{}
		for name, response := range before {
			found[name] = len(response.Results) > 0
		}
	}
	return maps.Clone(r.pages[page]), found
}

func (r *record) keep(page int, engine string, response Response) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pages[page][engine] = response
}
