package workspace

import (
	"sync"
	"time"
)

// SelectionLife is how long a member's selection stays what their agent sees.
const SelectionLife = 30 * time.Minute

// ViewingLife is how long a page stays what a member's desktop shows unless
// the desktop says so again: it does every minute while the page is open, so
// a tab closed without a word is forgotten soon.
const ViewingLife = 3 * time.Minute

// Viewing is the page and version a member's desktop shows.
type Viewing struct {
	PageID  string
	Version int
	// Since is when the desktop began to show this version; Seen is when it
	// last said it still does.
	Since time.Time
	Seen  time.Time
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

// live is what members are looking at now.
type live struct {
	mu         sync.Mutex
	viewing    map[string]Viewing
	selections map[string]Selection
}

// SetViewing records the page and version accountID's desktop shows.
func (s *Store) SetViewing(accountID, pageID string, version int) {
	s.live.mu.Lock()
	defer s.live.mu.Unlock()
	if s.live.viewing == nil {
		s.live.viewing = map[string]Viewing{}
	}
	now := s.now()
	viewing := s.live.viewing[accountID]
	if viewing.PageID != pageID || viewing.Version != version || now.Sub(viewing.Seen) >= ViewingLife {
		viewing = Viewing{PageID: pageID, Version: version, Since: now}
	}
	viewing.Seen = now
	s.live.viewing[accountID] = viewing
}

// ClearViewing forgets that accountID's desktop shows pageID; what it says
// of another page stays.
func (s *Store) ClearViewing(accountID, pageID string) {
	s.live.mu.Lock()
	defer s.live.mu.Unlock()
	if s.live.viewing[accountID].PageID == pageID {
		delete(s.live.viewing, accountID)
	}
}

// Viewing returns what accountID's desktop last said it shows, while it said
// so within ViewingLife.
func (s *Store) Viewing(accountID string) (Viewing, bool) {
	s.live.mu.Lock()
	defer s.live.mu.Unlock()
	viewing, ok := s.live.viewing[accountID]
	if !ok {
		return Viewing{}, false
	}
	if s.now().Sub(viewing.Seen) >= ViewingLife {
		delete(s.live.viewing, accountID)
		return Viewing{}, false
	}
	return viewing, true
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

func (l *live) forgetAccount(accountID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.viewing, accountID)
	delete(l.selections, accountID)
}

func (l *live) forgetPage(pageID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for account, viewing := range l.viewing {
		if viewing.PageID == pageID {
			delete(l.viewing, account)
		}
	}
	for account, selection := range l.selections {
		if selection.PageID == pageID {
			delete(l.selections, account)
		}
	}
}
