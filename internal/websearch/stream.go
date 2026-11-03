package websearch

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Update is part of a page, sent as the tab's engines answer: the window
// shows the results of the engines that have answered and adds the others'
// below them as they come.
type Update struct {
	// Results come after those of the updates before, the engines that
	// answered since taking turns in the tab's order.
	Results []Result `json:"results"`
	// Found are results sent before that more engines have found since,
	// with every engine that found them.
	Found        []Found  `json:"found"`
	Answers      []Answer `json:"answers"`
	Infobox      *Infobox `json:"infobox,omitempty"`
	Suggestions  []string `json:"suggestions"`
	Unresponsive []string `json:"unresponsive"`
	// Paused are the engines the search left out because their site blocked
	// them, all in the first update; Busy those it left out because they had
	// been asked too often just before (pace.go).
	Paused []PausedEngine `json:"paused"`
	Busy   []string       `json:"busy"`
	// Pending counts the engines still searching or waiting for their turn;
	// none in the last update.
	Pending int `json:"pending"`
}

// Found names the engines that found a result sent before.
type Found struct {
	URL     string   `json:"url"`
	Engines []string `json:"engines"`
}

// The first update waits a second for the fast engines, so that they take
// turns from the top; each later one gathers the engines that answer within
// a moment of each other.
const (
	firstUpdate = time.Second
	updateGap   = 300 * time.Millisecond
)

// answer is one engine's SearXNG answer; busy is an engine that was not
// asked, its turn too far off.
type answer struct {
	engine   string
	response Response
	busy     bool
	err      error
}

// Search runs one search for account and sends its results as the tab's
// engines answer, asking SearXNG for each engine on its own: SearXNG answers
// only once all the engines it was asked have. Each engine is asked at its
// own pace, and not while it is paused (pace.go). The query reaches SearXNG
// only in request bodies and is not logged. An error before anything was
// sent leaves the answer to the caller.
func (s *Service) Search(ctx context.Context, account string, query Query, send func(Update) error) error {
	if err := query.validate(); err != nil {
		return err
	}
	if err := s.enabled(ctx); err != nil {
		return err
	}
	if query.Category == Torrents {
		torrents, err := s.torrents(ctx)
		if err != nil {
			return err
		}
		if !torrents {
			return ErrTorrentsDisabled
		}
	}
	if !s.limiter(account).Allow() {
		return ErrTooFrequent
	}
	engines, err := s.catalog(ctx)
	if err != nil {
		return err
	}
	pauses, err := s.pausedEngines(ctx)
	if err != nil {
		return err
	}
	record := s.record(account, query)
	kept, found := record.begin(query.Page)
	names := engines.tabs[query.Category]
	answers := make(chan answer, len(names))
	var (
		ask    []string
		paused []PausedEngine
		total  int
	)
	for _, name := range names {
		pause, isPaused := pauses[name]
		response, isKept := kept[name]
		switch {
		case query.Page > 1 && (!engines.paging[name] || found != nil && !found[name]):
			// Only engines that found something on the page before turn it.
		case isKept:
			answers <- answer{engine: name, response: response}
			total++
		case isPaused:
			paused = append(paused, pause.shown(name))
		default:
			ask = append(ask, name)
			total++
		}
	}
	merge := newMerger(s, query.Category, names, paused)
	if total == 0 {
		return send(merge.update(nil, 0))
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for _, name := range ask {
		go func() { answers <- s.ask(ctx, record, query, name) }()
	}
	start := time.Now()
	var (
		wave   []answer
		due    <-chan time.Time
		sent   bool
		failed []error
	)
	for pending := total; pending > 0; {
		select {
		case a := <-answers:
			pending--
			if a.err != nil {
				failed = append(failed, a.err)
			}
			wave = append(wave, a)
			if pending > 0 && due == nil {
				due = time.After(max(s.updateGap, s.firstUpdate-time.Since(start)))
			}
		case <-due:
			if err := send(merge.update(wave, pending)); err != nil {
				return err
			}
			sent, wave, due = true, nil, nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// SearXNG itself did not answer: the service is down, not the engines.
	if !sent && len(failed) == total {
		return fmt.Errorf("%w: %v", ErrUnavailable, failed[0])
	}
	return send(merge.update(wave, 0))
}

// ask asks SearXNG for engine's answer once it is the engine's turn, keeps
// the answer for the same page again, and pauses the engine if its site
// blocked it.
func (s *Service) ask(ctx context.Context, record *record, query Query, engine string) answer {
	if err := s.turn(ctx, engine); err != nil {
		if errors.Is(err, errBusy) {
			return answer{engine: engine, busy: true}
		}
		return answer{engine: engine, err: err}
	}
	response, err := s.backend.Search(ctx, query.form([]string{engine}))
	if err != nil {
		return answer{engine: engine, err: err}
	}
	if _, failed := failure(engine, response); !failed {
		record.keep(query.Page, engine, response)
	}
	s.judge(ctx, engine, response)
	return answer{engine: engine, response: response}
}

// merger turns the engines' answers into updates. It remembers what it sent:
// a result an engine found after another goes out once, with both engines.
type merger struct {
	service      *Service
	category     Category
	order        []string
	sent         map[string]*Found
	paused       []PausedEngine
	started      bool
	answers      []string
	infobox      bool
	suggestions  []string
	unresponsive []string
	busy         []string
}

func newMerger(s *Service, category Category, order []string, paused []PausedEngine) *merger {
	return &merger{service: s, category: category, order: order, sent: map[string]*Found{}, paused: paused}
}

func (m *merger) update(wave []answer, pending int) Update {
	update := Update{
		Results: []Result{}, Found: []Found{}, Answers: []Answer{}, Suggestions: []string{}, Unresponsive: []string{},
		Paused: []PausedEngine{}, Busy: []string{}, Pending: pending,
	}
	if !m.started {
		m.started = true
		update.Paused = append(update.Paused, m.paused...)
	}
	// The engines of a wave in the tab's order, whenever each answered.
	slices.SortStableFunc(wave, func(a, b answer) int {
		return cmp.Compare(slices.Index(m.order, a.engine), slices.Index(m.order, b.engine))
	})
	var raws []RawResult
	for _, a := range wave {
		if a.busy {
			if l := label(a.engine); !slices.Contains(m.busy, l) {
				m.busy = append(m.busy, l)
				update.Busy = append(update.Busy, l)
			}
			continue
		}
		if a.err != nil {
			m.unresponsiveEngine(&update, a.engine)
			continue
		}
		for _, raw := range a.response.Results {
			if raw.Engine == "" {
				raw.Engine = a.engine
			}
			raws = append(raws, raw)
		}
		for _, pair := range a.response.Unresponsive {
			if len(pair) > 0 {
				m.unresponsiveEngine(&update, pair[0])
			}
		}
		m.extras(&update, a.response)
	}
	found := map[string]bool{}
	for _, raw := range interleave(raws, m.order) {
		result, ok := m.service.result(m.category, raw)
		if !ok {
			continue
		}
		key := resultKey(result.URL)
		earlier := m.sent[key]
		if earlier == nil {
			m.sent[key] = &Found{URL: result.URL, Engines: slices.Clone(result.Engines)}
			update.Results = append(update.Results, result)
			continue
		}
		// Found in this update, or found again by another engine.
		for _, engine := range result.Engines {
			if !slices.Contains(earlier.Engines, engine) {
				earlier.Engines = append(earlier.Engines, engine)
				found[key] = true
			}
		}
	}
	for i := range update.Results {
		key := resultKey(update.Results[i].URL)
		update.Results[i].Engines = slices.Clone(m.sent[key].Engines)
		delete(found, key)
	}
	for key := range found {
		update.Found = append(update.Found, Found{URL: m.sent[key].URL, Engines: slices.Clone(m.sent[key].Engines)})
	}
	slices.SortFunc(update.Found, func(a, b Found) int { return strings.Compare(a.URL, b.URL) })
	return update
}

func (m *merger) unresponsiveEngine(update *Update, name string) {
	if l := label(name); !slices.Contains(m.unresponsive, l) {
		m.unresponsive = append(m.unresponsive, l)
		update.Unresponsive = append(update.Unresponsive, l)
	}
}

// extras are the answers, infobox and suggestions SearXNG adds to an
// engine's results; each engine's request runs SearXNG's plugins again, so
// an answer such as a calculation comes once per engine.
func (m *merger) extras(update *Update, response Response) {
	for _, raw := range response.Answers {
		if answer, ok := decodeAnswer(raw); ok && !slices.Contains(m.answers, answer.Text) {
			m.answers = append(m.answers, answer.Text)
			update.Answers = append(update.Answers, answer)
		}
	}
	if !m.infobox {
		if box := m.service.infobox(response.Infoboxes); box != nil {
			m.infobox, update.Infobox = true, box
		}
	}
	for _, suggestion := range response.Suggestions {
		if suggestion = strings.TrimSpace(suggestion); suggestion != "" && !slices.Contains(m.suggestions, suggestion) {
			m.suggestions = append(m.suggestions, suggestion)
			update.Suggestions = append(update.Suggestions, suggestion)
		}
	}
}

// resultKey is what makes two engines' results the same, as SearXNG merges
// them: the address without its scheme, a leading www., a trailing slash or
// a fragment.
func resultKey(address string) string {
	parsed, err := url.Parse(address)
	if err != nil {
		return address
	}
	host := strings.TrimPrefix(strings.ToLower(parsed.Host), "www.")
	return host + strings.TrimSuffix(parsed.EscapedPath(), "/") + "?" + parsed.RawQuery
}
