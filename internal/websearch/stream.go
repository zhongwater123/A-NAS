package websearch

import (
	"cmp"
	"context"
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
	// Pending counts the tab's engines still searching; none in the last
	// update.
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

// answer is one engine's SearXNG answer.
type answer struct {
	engine   string
	response Response
	err      error
}

// Search runs one search for account and sends its results as the tab's
// engines answer, asking SearXNG for each engine on its own: SearXNG answers
// only once all the engines it was asked have. The query reaches SearXNG
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
	names := engines[query.Category]
	merge := newMerger(s, query.Category, names)
	if len(names) == 0 {
		return send(merge.update(nil, 0))
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	answers := make(chan answer, len(names))
	for _, name := range names {
		go func() {
			response, err := s.backend.Search(ctx, query.form([]string{name}))
			answers <- answer{engine: name, response: response, err: err}
		}()
	}
	start := time.Now()
	var (
		wave   []answer
		due    <-chan time.Time
		sent   bool
		failed []error
	)
	for pending := len(names); pending > 0; {
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
	if !sent && len(failed) == len(names) {
		return fmt.Errorf("%w: %v", ErrUnavailable, failed[0])
	}
	return send(merge.update(wave, 0))
}

// merger turns the engines' answers into updates. It remembers what it sent:
// a result an engine found after another goes out once, with both engines.
type merger struct {
	service      *Service
	category     Category
	order        []string
	sent         map[string]*Found
	answers      []string
	infobox      bool
	suggestions  []string
	unresponsive []string
}

func newMerger(s *Service, category Category, order []string) *merger {
	return &merger{service: s, category: category, order: order, sent: map[string]*Found{}}
}

func (m *merger) update(wave []answer, pending int) Update {
	update := Update{
		Results: []Result{}, Found: []Found{}, Answers: []Answer{}, Suggestions: []string{}, Unresponsive: []string{},
		Pending: pending,
	}
	// The engines of a wave in the tab's order, whenever each answered.
	slices.SortStableFunc(wave, func(a, b answer) int {
		return cmp.Compare(slices.Index(m.order, a.engine), slices.Index(m.order, b.engine))
	})
	var raws []RawResult
	for _, a := range wave {
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
