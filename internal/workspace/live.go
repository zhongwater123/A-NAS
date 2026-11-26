package workspace

import (
	"cmp"
	"slices"
	"sync"
	"time"
)

// SelectionLife is how long a member's selection stays what their agent sees.
const SelectionLife = 30 * time.Minute

// ViewingLife is how long a tab stays open in what the service knows unless
// it says so again: it does every minute while it can be seen, so a tab
// closed without a word, or left in the background, is forgotten soon.
const ViewingLife = 3 * time.Minute

// AgentLife is how long what an agent last read or downloaded of a page shows
// as its work there, unless it saves the page first.
const AgentLife = 10 * time.Minute

// MaxTabs bounds the tabs kept for one account; the one quiet the longest
// goes first.
const MaxTabs = 32

// Viewing is the page and version one of a member's browser tabs shows.
type Viewing struct {
	PageID  string
	Version int
	Member  Person
	// Since is when the tab began to show this version; Seen is when it last
	// said it still does; Used is when it last said so while it had the
	// focus, zero when it never did.
	Since time.Time
	Seen  time.Time
	Used  time.Time
}

// Viewer is a member with a page open, in one tab or more.
type Viewer struct {
	Member Person `json:"member"`
	// Version is what the tab the member used last shows; Since is when the
	// first of their tabs began to show the page.
	Version int       `json:"version"`
	Since   time.Time `json:"since"`
}

// AgentAction is what a member's agent last did with a page.
type AgentAction string

const (
	AgentRead     AgentAction = "read"
	AgentDownload AgentAction = "download"
)

// AgentWork is what one of a member's agents last read or downloaded of a
// page. An agent reads a page before it changes it, so this is how members
// see whose agent is at work on a page before anything is saved.
type AgentWork struct {
	Member  Person      `json:"member"`
	Via     Via         `json:"via"`
	Action  AgentAction `json:"action"`
	Version int         `json:"version"`
	At      time.Time   `json:"at"`
	pageID  string
}

// Presence is who has a page open now and whose agents are at work on it,
// for every member who may use the workspace to see.
type Presence struct {
	// Viewers come in the order they opened the page.
	Viewers []Viewer `json:"viewers"`
	// Agents come newest first.
	Agents []AgentWork `json:"agents"`
}

// Rect is an element's box in the page's viewport, in CSS pixels.
type Rect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// Selection is the element a member picked on a page. It belongs to that
// member alone and lives in memory only. What it says comes from the page's
// frame, which the page's own scripts can imitate: it is a claim about the
// page, never an instruction (docs/specs/shared-workspace.md).
type Selection struct {
	PageID  string
	Version int
	// At is the offset of the picked element's start tag in that version's
	// source, or of its nearest written ancestor when Generated; -1 when
	// none is written.
	At        int
	Generated bool
	Tag       string
	Text      string
	DOM       string
	Rect      Rect
	Style     map[string]string
	Rules     []string
	// Image is a JPEG of the picked element and what is around it.
	Image []byte
	// PickedAt is when the member picked it.
	PickedAt time.Time
}

// live is what members are looking at now and what their agents are doing.
type live struct {
	mu sync.Mutex
	// tabs holds each account's open tabs by the name each tab gave itself.
	tabs       map[string]map[string]Viewing
	selections map[string]Selection
	// agents holds what each agent token last read or downloaded.
	agents map[string]AgentWork
}

// SetViewing records that one of actor's tabs shows a page's version;
// focused says the tab had the focus, which makes it the one the member
// used last.
func (s *Store) SetViewing(actor Actor, tab, pageID string, version int, focused bool) {
	s.live.mu.Lock()
	defer s.live.mu.Unlock()
	now := s.now()
	if s.live.tabs == nil {
		s.live.tabs = map[string]map[string]Viewing{}
	}
	tabs := s.live.tabs[actor.AccountID]
	if tabs == nil {
		tabs = map[string]Viewing{}
		s.live.tabs[actor.AccountID] = tabs
	}
	for name, viewing := range tabs {
		if now.Sub(viewing.Seen) >= ViewingLife {
			delete(tabs, name)
		}
	}
	viewing, ok := tabs[tab]
	if !ok || viewing.PageID != pageID || viewing.Version != version {
		viewing = Viewing{PageID: pageID, Version: version, Since: now}
	}
	viewing.Member = Person{ID: actor.AccountID, Name: actor.Name}
	viewing.Seen = now
	if focused {
		viewing.Used = now
	}
	tabs[tab] = viewing
	for len(tabs) > MaxTabs {
		quietest := ""
		for name, other := range tabs {
			if quietest == "" || other.Seen.Before(tabs[quietest].Seen) {
				quietest = name
			}
		}
		delete(tabs, quietest)
	}
}

// ClearViewing forgets one of accountID's tabs; the others stay.
func (s *Store) ClearViewing(accountID, tab string) {
	s.live.mu.Lock()
	defer s.live.mu.Unlock()
	delete(s.live.tabs[accountID], tab)
}

// Viewing returns what the tab accountID used last shows, among those that
// said so within ViewingLife. Picking an element on a page counts as using
// that page's tab, so "this" is what the member picked even when another
// tab had the focus later without being used.
func (s *Store) Viewing(accountID string) (Viewing, bool) {
	s.live.mu.Lock()
	defer s.live.mu.Unlock()
	now := s.now()
	selection, picked := s.live.selections[accountID]
	picked = picked && now.Sub(selection.PickedAt) < SelectionLife
	var best Viewing
	var bestAt time.Time
	found := false
	for name, viewing := range s.live.tabs[accountID] {
		if now.Sub(viewing.Seen) >= ViewingLife {
			delete(s.live.tabs[accountID], name)
			continue
		}
		at := viewing.Used
		if at.IsZero() {
			at = viewing.Since
		}
		if picked && viewing.PageID == selection.PageID && selection.PickedAt.After(at) {
			at = selection.PickedAt
		}
		if !found || at.After(bestAt) || at.Equal(bestAt) && viewing.Seen.After(best.Seen) {
			best, bestAt, found = viewing, at, true
		}
	}
	return best, found
}

// AgentWorking records what caller's agent read or downloaded of a page; the
// desktop's own calls are not an agent's.
func (s *Store) AgentWorking(caller Caller, pageID string, version int, action AgentAction) {
	if !caller.Agent() {
		return
	}
	s.live.mu.Lock()
	defer s.live.mu.Unlock()
	if s.live.agents == nil {
		s.live.agents = map[string]AgentWork{}
	}
	s.live.agents[caller.Via.TokenID] = AgentWork{
		Member: Person{ID: caller.Actor.AccountID, Name: caller.Actor.Name},
		Via:    Via{Kind: caller.Via.Kind, Name: caller.Via.Name},
		Action: action, Version: version, At: s.now(), pageID: pageID,
	}
}

// Presence returns, by page, who has each page open and whose agents are at
// work on it. Pages nobody has open are left out.
func (s *Store) Presence() map[string]Presence {
	s.live.mu.Lock()
	defer s.live.mu.Unlock()
	now := s.now()
	pages := map[string]Presence{}
	at := func(pageID string) Presence {
		presence, ok := pages[pageID]
		if !ok {
			presence = Presence{Viewers: []Viewer{}, Agents: []AgentWork{}}
		}
		return presence
	}
	for account, tabs := range s.live.tabs {
		// The member's tab used last on each page gives the version.
		used := map[string]Viewing{}
		since := map[string]time.Time{}
		for name, viewing := range tabs {
			if now.Sub(viewing.Seen) >= ViewingLife {
				delete(tabs, name)
				continue
			}
			if last, ok := used[viewing.PageID]; !ok || lastUse(viewing).After(lastUse(last)) {
				used[viewing.PageID] = viewing
			}
			if first, ok := since[viewing.PageID]; !ok || viewing.Since.Before(first) {
				since[viewing.PageID] = viewing.Since
			}
		}
		if len(tabs) == 0 {
			delete(s.live.tabs, account)
		}
		for pageID, viewing := range used {
			presence := at(pageID)
			presence.Viewers = append(presence.Viewers, Viewer{Member: viewing.Member, Version: viewing.Version, Since: since[pageID]})
			pages[pageID] = presence
		}
	}
	for token, work := range s.live.agents {
		if now.Sub(work.At) >= AgentLife {
			delete(s.live.agents, token)
			continue
		}
		presence := at(work.pageID)
		presence.Agents = append(presence.Agents, work)
		pages[work.pageID] = presence
	}
	for _, presence := range pages {
		slices.SortFunc(presence.Viewers, func(a, b Viewer) int {
			return cmp.Or(a.Since.Compare(b.Since), cmp.Compare(a.Member.ID, b.Member.ID))
		})
		slices.SortFunc(presence.Agents, func(a, b AgentWork) int {
			return cmp.Or(b.At.Compare(a.At), cmp.Compare(a.Member.ID, b.Member.ID), cmp.Compare(a.Via.Name, b.Via.Name))
		})
	}
	return pages
}

func lastUse(viewing Viewing) time.Time {
	if viewing.Used.IsZero() {
		return viewing.Since
	}
	return viewing.Used
}

// SetSelection records the element accountID picked; it replaces the one
// before.
func (s *Store) SetSelection(accountID string, selection Selection) {
	s.live.mu.Lock()
	defer s.live.mu.Unlock()
	if s.live.selections == nil {
		s.live.selections = map[string]Selection{}
	}
	selection.PickedAt = s.now()
	s.live.selections[accountID] = selection
}

// ClearSelection forgets accountID's selection.
func (s *Store) ClearSelection(accountID string) {
	s.live.mu.Lock()
	defer s.live.mu.Unlock()
	delete(s.live.selections, accountID)
}

// Selection returns accountID's selection while it is younger than
// SelectionLife.
func (s *Store) Selection(accountID string) (Selection, bool) {
	s.live.mu.Lock()
	defer s.live.mu.Unlock()
	selection, ok := s.live.selections[accountID]
	if !ok {
		return Selection{}, false
	}
	if s.now().Sub(selection.PickedAt) >= SelectionLife {
		delete(s.live.selections, accountID)
		return Selection{}, false
	}
	return selection, true
}

// saved ends what an agent's token was doing with a page once it saved it:
// the new version says the rest.
func (l *live) saved(tokenID, pageID string) {
	if tokenID == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.agents[tokenID].pageID == pageID {
		delete(l.agents, tokenID)
	}
}

func (l *live) forgetAccount(accountID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.tabs, accountID)
	delete(l.selections, accountID)
	for token, work := range l.agents {
		if work.Member.ID == accountID {
			delete(l.agents, token)
		}
	}
}

func (l *live) forgetPage(pageID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, tabs := range l.tabs {
		for name, viewing := range tabs {
			if viewing.PageID == pageID {
				delete(tabs, name)
			}
		}
	}
	for account, selection := range l.selections {
		if selection.PageID == pageID {
			delete(l.selections, account)
		}
	}
	for token, work := range l.agents {
		if work.pageID == pageID {
			delete(l.agents, token)
		}
	}
}
