package assistantapi

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// submitTool is the A-NAS MCP tool 提交更改 as Hermes names it.
const submitTool = "mcp__anas__submit_changes"

// keepCards is how long a decided card stays, so another window or a reload
// still shows how it ended.
const keepCards = time.Hour

// approvalWait is the approvals.timeout the managed policy sets: how long
// Hermes waits for the member to answer a card.
const approvalWait = 24 * time.Hour

// staleCard is beyond approvalWait.
const staleCard = approvalWait + time.Hour

// Card statuses. awaiting is the only one the member can act on.
const (
	cardAwaiting = "awaiting"
	cardApproved = "approved"
	cardRejected = "rejected"
	cardExpired  = "expired"
	cardStopped  = "stopped"
)

// card is one approval Hermes asked for, paired with the call of 提交更改 it
// is about (ADR 0022 decision 4).
type card struct {
	RequestID       string          `json:"requestId"`
	SessionID       string          `json:"sessionId"`
	StoredSessionID string          `json:"storedSessionId,omitempty"`
	Status          string          `json:"status"`
	Change          json.RawMessage `json:"change"`
	CreatedAt       int64           `json:"createdAt"`

	changeID string
	toolID   string
	// confirming is set when the member confirmed: only then can the call's
	// completion make the card applied or failed.
	confirming bool
	// owner is the connection Hermes asked on; any connection can answer.
	owner   *upstream
	decided time.Time
}

// liveSession is what the Product Service follows of one runtime session:
// whether a turn is running and the call of 提交更改 under way in it.
type liveSession struct {
	stored string
	turn   bool
	// submit is the arguments of the call of 提交更改 that started and has
	// neither completed nor been paired with an approval.
	submit   *startedCall
	stopping bool
	owner    *upstream
}

type startedCall struct {
	toolID string
	args   json.RawMessage
}

// member holds one account's live sessions, cards and connections. Every
// connection of the account sees the same state, so a card raised on one
// can be answered from another window.
type member struct {
	account   string
	mu        sync.Mutex
	sessions  map[string]*liveSession
	cards     map[string]*card
	upstreams map[*upstream]struct{}
}

func (h *handler) member(account string) *member {
	h.membersMu.Lock()
	defer h.membersMu.Unlock()
	m, ok := h.members[account]
	if !ok {
		m = &member{account: account, sessions: map[string]*liveSession{}, cards: map[string]*card{}, upstreams: map[*upstream]struct{}{}}
		h.members[account] = m
	}
	return m
}

func (m *member) session(id string) *liveSession {
	s, ok := m.sessions[id]
	if !ok {
		s = &liveSession{}
		m.sessions[id] = s
	}
	return s
}

// bind records which connection a session's events now go to, and its stored id.
func (m *member) bind(id, stored string, owner *upstream) {
	if !runtimeIDPattern.MatchString(id) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.session(id)
	if stored != "" {
		s.stored = stored
	}
	if owner != nil {
		s.owner = owner
	}
}

func (m *member) openTurn(id string, owner *upstream) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.session(id)
	s.turn, s.stopping, s.owner = true, false, owner
}

func (m *member) stopping(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		s.stopping = true
	}
}

// busy reports whether a turn runs on a session whose events come to u: u
// must stay open until it ends, or the instance exits idle under it.
func (m *member) busy(u *upstream) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		if s.owner == u && s.turn {
			return true
		}
	}
	return false
}

// observe follows a turn through Hermes's events: when it runs, the call of
// 提交更改 under way, and when it ends. It returns the cards that changed.
func (h *handler) observe(m *member, u *upstream, ev event) {
	var payload struct {
		ToolID     string          `json:"tool_id"`
		Name       string          `json:"name"`
		Args       json.RawMessage `json:"args"`
		ID         string          `json:"id"`
		RequestIDs []string        `json:"request_ids"`
	}
	_ = json.Unmarshal(ev.Payload, &payload)
	var ended []*card
	var completed *card
	m.mu.Lock()
	s := m.sessions[ev.SessionID]
	if s == nil && runtimeIDPattern.MatchString(ev.SessionID) {
		s = m.session(ev.SessionID)
	}
	if s != nil {
		s.owner = u
	}
	switch ev.Type {
	case "message.start":
		if s != nil {
			s.turn = true
		}
	case "tool.start":
		if s != nil && s.turn && payload.Name == submitTool && len(payload.Args) > 0 && string(payload.Args) != "null" {
			s.submit = &startedCall{toolID: payload.ToolID, args: payload.Args}
		}
	case "tool.complete":
		if s != nil && payload.Name == submitTool {
			if s.submit != nil && s.submit.toolID == payload.ToolID {
				s.submit = nil
			}
			for _, c := range m.cards {
				// Hermes may finish the call before the member's request has
				// recorded the card as approved.
				if c.SessionID == ev.SessionID && c.toolID == payload.ToolID && c.confirming && (c.Status == cardApproved || c.Status == "deciding") {
					completed = c
				}
			}
		}
	case "message.complete":
		if s != nil {
			// The turn is over: nothing it asked for can be approved any more,
			// and an approval arriving now (the background review) is refused.
			status := cardExpired
			if s.stopping {
				status = cardStopped
			}
			s.turn, s.submit, s.stopping = false, nil, false
			ended = m.endCards(func(c *card) bool { return c.SessionID == ev.SessionID }, status)
		}
	case "request.cancel":
		ended = m.endCards(func(c *card) bool { return c.srqMatches(payload.ID) }, m.cancelStatus(ev.SessionID))
	case "approval.cancelled":
		ids := map[string]bool{}
		for _, id := range payload.RequestIDs {
			ids[id] = true
		}
		ended = m.endCards(func(c *card) bool { return ids[c.RequestID] }, m.cancelStatus(ev.SessionID))
	}
	m.mu.Unlock()
	for _, c := range ended {
		h.expire(m, u, c)
	}
	if completed != nil {
		go h.refresh(m, u, completed)
	}
}

func (c *card) srqMatches(id string) bool {
	return c.owner != nil && c.owner.serverRequestOf(c.RequestID) == id
}

// cancelStatus names a card Hermes withdrew: stopped when the member stopped
// the turn, expired when the time ran out.
func (m *member) cancelStatus(session string) string {
	if s, ok := m.sessions[session]; ok && s.stopping {
		return cardStopped
	}
	return cardExpired
}

// endCards marks the awaiting cards that match as ended; the caller expires
// them with the executor outside the lock.
func (m *member) endCards(match func(*card) bool, status string) []*card {
	var ended []*card
	for _, c := range m.cards {
		if c.Status == cardAwaiting && match(c) {
			c.Status, c.decided = status, time.Now()
			ended = append(ended, c)
		}
	}
	return ended
}

// approval handles Hermes asking whether a tool may run. Only an approval
// that pairs with the call of 提交更改 under way in a running turn of the
// same session becomes a card; every other approval is denied without being
// shown, so the member's confirmation only ever means "change my files".
func (h *handler) approval(m *member, u *upstream, request frame) {
	var params struct {
		SessionID   string   `json:"session_id"`
		RequestID   string   `json:"request_id"`
		PatternKey  string   `json:"pattern_key"`
		PatternKeys []string `json:"pattern_keys"`
	}
	_ = json.Unmarshal(request.Params, &params)
	elicitation := params.PatternKey == "mcp_elicitation"
	for _, key := range params.PatternKeys {
		elicitation = elicitation || key == "mcp_elicitation"
	}
	m.mu.Lock()
	s := m.sessions[params.SessionID]
	var call *startedCall
	if s != nil && s.turn && s.submit != nil && elicitation && requestIDPattern.MatchString(params.RequestID) {
		call, s.submit = s.submit, nil
	}
	var stored string
	if s != nil {
		stored = s.stored
	}
	m.mu.Unlock()
	if call == nil {
		h.logger.Warn("assistant approval refused: not a call of 提交更改 in a running turn",
			"account", m.account, "session", params.SessionID, "pattern", params.PatternKey)
		u.answer(request.ID, "deny")
		return
	}
	u.rememberServerRequest(params.RequestID, request.ID)
	go func() {
		ctx, cancel := context.WithTimeout(accounts.WithSessionToken(context.Background(), u.session), requestTimeout)
		defer cancel()
		change, err := h.options.Changes.Prepare(ctx, m.account, Submission{SessionID: params.SessionID, RequestID: params.RequestID, Args: call.args})
		switch {
		case errors.Is(err, ErrInvalidSubmission):
			// No card: the tool runs and tells the assistant what is wrong,
			// rather than the assistant hearing that the member said no.
			u.answer(request.ID, "once")
			return
		case err != nil:
			h.logger.Warn("assistant change could not be prepared", "account", m.account, "error", err)
			u.answer(request.ID, "deny")
			return
		}
		c := &card{RequestID: params.RequestID, SessionID: params.SessionID, StoredSessionID: stored, Status: cardAwaiting,
			Change: change.Raw, CreatedAt: time.Now().Unix(), changeID: change.ID, toolID: call.toolID, owner: u}
		m.mu.Lock()
		m.prune()
		m.cards[c.RequestID] = c
		m.mu.Unlock()
		h.broadcast(m, c)
	}()
}

// decide carries out the member's answer to a card, from their own signed-in
// request: the executor records the grant against that session before Hermes
// hears "once", and Hermes's own approval executes nothing.
func (h *handler) decide(ctx context.Context, m *member, requestID string, confirm bool) (card, error) {
	m.mu.Lock()
	m.prune()
	c, ok := m.cards[requestID]
	if !ok {
		m.mu.Unlock()
		return card{}, ErrChangeNotFound
	}
	if c.Status != cardAwaiting {
		view := *c
		m.mu.Unlock()
		return view, ErrChangeDecided
	}
	c.Status, c.confirming = "deciding", confirm
	m.mu.Unlock()
	decision, choice, status := Reject, "deny", cardRejected
	if confirm {
		decision, choice, status = Approve, "once", cardApproved
	}
	change, err := h.options.Changes.Decide(ctx, m.account, c.changeID, decision)
	if err != nil {
		m.mu.Lock()
		if c.Status == "deciding" {
			c.Status = cardAwaiting
		}
		view := *c
		m.mu.Unlock()
		return view, err
	}
	resolved, err := h.respond(ctx, m, c, choice)
	// Hermes no longer waits (timed out or stopped): the grant must not outlive
	// the question.
	gone := err != nil || !resolved
	m.mu.Lock()
	c.decided = time.Now()
	c.Change = change.Raw
	if gone {
		c.Status = cardExpired
	} else if c.Status == "deciding" {
		c.Status = status
	}
	m.mu.Unlock()
	if gone && confirm {
		if change, expireErr := h.options.Changes.Decide(ctx, m.account, c.changeID, Expire); expireErr == nil {
			m.mu.Lock()
			c.Change = change.Raw
			m.mu.Unlock()
		}
	}
	h.broadcast(m, c)
	m.mu.Lock()
	view := *c
	m.mu.Unlock()
	if gone {
		return view, errApprovalGone
	}
	return view, nil
}

var errApprovalGone = errors.New("the assistant no longer waits for this approval")

// respond answers Hermes with approval.respond on the connection the approval
// came on, or any other of the account's: Hermes takes it from any.
func (h *handler) respond(ctx context.Context, m *member, c *card, choice string) (bool, error) {
	m.mu.Lock()
	candidates := []*upstream{}
	if c.owner != nil {
		candidates = append(candidates, c.owner)
	}
	for u := range m.upstreams {
		if u != c.owner {
			candidates = append(candidates, u)
		}
	}
	m.mu.Unlock()
	var lastErr error = errApprovalGone
	for _, u := range candidates {
		if u.isClosed() {
			continue
		}
		result, err := u.call(ctx, "approval.respond", map[string]any{"session_id": c.SessionID, "request_id": c.RequestID, "choice": choice})
		if err != nil {
			lastErr = err
			continue
		}
		var answer struct {
			Resolved int `json:"resolved"`
		}
		_ = json.Unmarshal(result, &answer)
		return answer.Resolved > 0, nil
	}
	return false, lastErr
}

// expire ends a change the executor still holds for a card Hermes withdrew.
func (h *handler) expire(m *member, u *upstream, c *card) {
	go func() {
		ctx, cancel := context.WithTimeout(accounts.WithSessionToken(context.Background(), u.session), requestTimeout)
		defer cancel()
		if change, err := h.options.Changes.Decide(ctx, m.account, c.changeID, Expire); err == nil {
			m.mu.Lock()
			c.Change = change.Raw
			m.mu.Unlock()
		} else if !errors.Is(err, ErrChangeDecided) {
			h.logger.Warn("assistant change could not be expired", "account", m.account, "error", err)
		}
		h.broadcast(m, c)
	}()
}

// refresh reads the executor's outcome once the tool applied a change.
func (h *handler) refresh(m *member, u *upstream, c *card) {
	ctx, cancel := context.WithTimeout(accounts.WithSessionToken(context.Background(), u.session), requestTimeout)
	defer cancel()
	change, err := h.options.Changes.Get(ctx, m.account, c.changeID)
	if err != nil {
		h.logger.Warn("assistant change outcome unreadable", "account", m.account, "error", err)
		return
	}
	m.mu.Lock()
	c.Change = change.Raw
	if change.Status == "applied" || change.Status == "failed" {
		c.Status = change.Status
	}
	m.mu.Unlock()
	h.broadcast(m, c)
}

// list returns the account's cards, newest first.
func (m *member) list() []card {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prune()
	cards := make([]card, 0, len(m.cards))
	for _, c := range m.cards {
		cards = append(cards, *c)
	}
	for i := 1; i < len(cards); i++ {
		for j := i; j > 0 && cards[j].CreatedAt > cards[j-1].CreatedAt; j-- {
			cards[j], cards[j-1] = cards[j-1], cards[j]
		}
	}
	return cards
}

func (m *member) prune() {
	for id, c := range m.cards {
		// Hermes stopped waiting long ago even if its cancel never arrived, as
		// when the instance stopped while the card was open.
		if c.Status == cardAwaiting && time.Since(time.Unix(c.CreatedAt, 0)) > staleCard {
			c.Status, c.decided = cardExpired, time.Now()
		}
		if !c.decided.IsZero() && time.Since(c.decided) > keepCards {
			delete(m.cards, id)
		}
	}
}

// broadcast tells every window of the account how a card stands.
func (h *handler) broadcast(m *member, c *card) {
	m.mu.Lock()
	view := *c
	targets := make([]*upstream, 0, len(m.upstreams))
	for u := range m.upstreams {
		targets = append(targets, u)
	}
	m.mu.Unlock()
	message := eventFrame("anas.approval", view.SessionID, view)
	for _, u := range targets {
		u.toBrowser(message)
	}
}
