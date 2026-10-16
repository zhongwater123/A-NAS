package assistantapi

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

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

// Card kinds: what Hermes asks the member (ADR 0024 decisions 3 and 4).
const (
	// kindChange is a call of 提交更改: the executor changes the member's files.
	kindChange = "change"
	// kindCommand is a dangerous command or a script Hermes runs in the
	// sandbox, which touches nothing outside the assistant's own folder.
	kindCommand = "command"
	// kindQuestion is the clarify tool's questions.
	kindQuestion = "question"
)

// maxCommand bounds the command or script a card shows: the member must see
// all of what they allow.
const maxCommand = 256 << 10

// Card statuses. awaiting is the only one the member can act on.
const (
	cardAwaiting = "awaiting"
	cardApproved = "approved"
	cardAnswered = "answered"
	cardRejected = "rejected"
	cardExpired  = "expired"
	cardStopped  = "stopped"
)

// card is one thing Hermes asked the member in a running turn: an approval
// paired with the call of 提交更改 it is about, an approval of a command or a
// script, or the clarify tool's questions.
type card struct {
	RequestID       string          `json:"requestId"`
	Kind            string          `json:"kind"`
	SessionID       string          `json:"sessionId"`
	StoredSessionID string          `json:"storedSessionId,omitempty"`
	Status          string          `json:"status"`
	Change          json.RawMessage `json:"change,omitempty"`
	Command         *commandRequest `json:"command,omitempty"`
	Questions       []question      `json:"questions,omitempty"`
	Answers         map[string]any  `json:"answers,omitempty"`
	CreatedAt       int64           `json:"createdAt"`

	changeID string
	toolID   string
	// confirming is set when the member confirmed: only then can the call's
	// completion make the card applied or failed.
	confirming bool
	// owner is the connection Hermes asked on; any connection can answer an
	// approval, a question only the one it came on.
	owner   *upstream
	decided time.Time
}

// commandRequest is a command or script Hermes asks to run, as its approval
// shows it.
type commandRequest struct {
	Text        string `json:"text"`
	Description string `json:"description,omitempty"`
	// Script marks an execute_code script, approved as a whole.
	Script bool `json:"script,omitempty"`
	// ForSession says the member may allow it for the rest of the conversation.
	ForSession bool `json:"forSession,omitempty"`
}

// question is one of the clarify tool's questions.
type question struct {
	ID          string   `json:"id"`
	Text        string   `json:"text"`
	Choices     []string `json:"choices,omitempty"`
	MultiSelect bool     `json:"multiSelect,omitempty"`
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

// running reports a session with a turn under way, and its stored id.
func (m *member) running(session string) (bool, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[session]
	if s == nil {
		return false, ""
	}
	return s.turn, s.stored
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
				if c.Kind == kindChange && c.SessionID == ev.SessionID && c.toolID == payload.ToolID && c.confirming &&
					(c.Status == cardApproved || c.Status == "deciding") {
					completed = c
				}
			}
		}
	case "message.complete":
		if s != nil {
			// The turn is over: nothing it asked for can be answered any more,
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

// approval handles Hermes asking whether a tool may run. An approval that
// pairs with the call of 提交更改 under way in a running turn of the same
// session becomes a change card; one for a dangerous command or a script in a
// running turn becomes a command card showing it. Every other approval (the
// background review's) is denied without being shown.
func (h *handler) approval(m *member, u *upstream, request frame) {
	var params struct {
		SessionID    string   `json:"session_id"`
		RequestID    string   `json:"request_id"`
		PatternKey   string   `json:"pattern_key"`
		PatternKeys  []string `json:"pattern_keys"`
		Command      string   `json:"command"`
		Description  string   `json:"description"`
		Choices      []string `json:"choices"`
		AllowSession *bool    `json:"allow_session"`
	}
	_ = json.Unmarshal(request.Params, &params)
	elicitation := params.PatternKey == "mcp_elicitation" || slices.Contains(params.PatternKeys, "mcp_elicitation")
	valid := requestIDPattern.MatchString(params.RequestID)
	m.mu.Lock()
	s := m.sessions[params.SessionID]
	running := s != nil && s.turn
	var call *startedCall
	if running && s.submit != nil && elicitation && valid {
		call, s.submit = s.submit, nil
	}
	var stored string
	if s != nil {
		stored = s.stored
	}
	m.mu.Unlock()
	if call == nil && running && !elicitation && valid && params.Command != "" &&
		len(params.Command) <= maxCommand && utf8.ValidString(params.Command) {
		forSession := slices.Contains(params.Choices, "session") || (params.Choices == nil && (params.AllowSession == nil || *params.AllowSession))
		u.rememberServerRequest(params.RequestID, request.ID)
		h.raise(m, &card{RequestID: params.RequestID, Kind: kindCommand, SessionID: params.SessionID, StoredSessionID: stored,
			Command: &commandRequest{Text: params.Command, Description: params.Description,
				Script: params.PatternKey == "execute_code", ForSession: forSession}, owner: u})
		return
	}
	if call == nil {
		h.logger.Warn("assistant approval refused: not asked in a running turn",
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
		h.raise(m, &card{RequestID: params.RequestID, Kind: kindChange, SessionID: params.SessionID, StoredSessionID: stored,
			Change: change.Raw, changeID: change.ID, toolID: call.toolID, owner: u})
	}()
}

// question passes the clarify tool's questions in a running turn on as a
// card; the member's answers go back as the reply to Hermes's request.
func (h *handler) question(m *member, u *upstream, request frame) {
	var params struct {
		SessionID string `json:"session_id"`
		Questions []struct {
			QID         string   `json:"qid"`
			Question    string   `json:"question"`
			Choices     []string `json:"choices"`
			MultiSelect bool     `json:"multi_select"`
		} `json:"questions"`
	}
	srq := unquote(request.ID)
	valid := json.Unmarshal(request.Params, &params) == nil && len(params.Questions) > 0 && len(params.Questions) <= 5 &&
		len(request.Params) <= maxCommand
	questions := make([]question, 0, len(params.Questions))
	for _, q := range params.Questions {
		valid = valid && requestIDPattern.MatchString(q.QID) && q.Question != "" && utf8.ValidString(q.Question)
		questions = append(questions, question{ID: q.QID, Text: q.Question, Choices: q.Choices, MultiSelect: q.MultiSelect})
	}
	running, stored := m.running(params.SessionID)
	if !valid || !running {
		// Hermes takes an error as "nobody answered": the tool tells the
		// assistant at once instead of waiting out its timeout.
		_ = u.send(rpcError(request.ID, codeMethodNotFound, "not available in A-NAS"))
		return
	}
	u.rememberServerRequest(srq, request.ID)
	h.raise(m, &card{RequestID: srq, Kind: kindQuestion, SessionID: params.SessionID, StoredSessionID: stored,
		Questions: questions, owner: u})
}

// raise keeps a new card and shows it to every window of the account.
func (h *handler) raise(m *member, c *card) {
	c.Status, c.CreatedAt = cardAwaiting, time.Now().Unix()
	m.mu.Lock()
	m.prune()
	m.cards[c.RequestID] = c
	m.mu.Unlock()
	h.broadcast(m, c)
}

// errNotAnApproval and errNotAQuestion name an answer of the wrong kind.
var (
	errNotAnApproval = errors.New("this card is a question, to be answered")
	errNotAQuestion  = errors.New("this card is not a question")
	errInvalidAnswer = errors.New("the answers do not fit the questions")
)

// decide carries out the member's answer to a change or command card, from
// their own signed-in request. For a change the executor records the grant
// against that session before Hermes hears "once", and Hermes's own approval
// executes nothing; a command runs in the sandbox once Hermes hears "once",
// or "session" when the member allows it for the rest of the conversation.
// Rejecting a question skips it.
func (h *handler) decide(ctx context.Context, m *member, requestID string, confirm, forSession bool) (card, error) {
	m.mu.Lock()
	m.prune()
	c, ok := m.cards[requestID]
	if !ok {
		m.mu.Unlock()
		return card{}, ErrChangeNotFound
	}
	if c.Kind == kindQuestion {
		m.mu.Unlock()
		if confirm {
			return card{}, errNotAnApproval
		}
		return h.answerQuestion(m, requestID, nil)
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
		if forSession && c.Kind == kindCommand && c.Command.ForSession {
			choice = "session"
		}
	}
	var change Change
	if c.Kind == kindChange {
		var err error
		change, err = h.options.Changes.Decide(ctx, m.account, c.changeID, decision)
		if err != nil {
			m.mu.Lock()
			if c.Status == "deciding" {
				c.Status = cardAwaiting
			}
			view := *c
			m.mu.Unlock()
			return view, err
		}
	}
	resolved, err := h.respond(ctx, m, c, choice)
	// Hermes no longer waits (timed out or stopped): the grant must not outlive
	// the question.
	gone := err != nil || !resolved
	m.mu.Lock()
	c.decided = time.Now()
	if c.Kind == kindChange {
		c.Change = change.Raw
	}
	if gone {
		c.Status = cardExpired
	} else if c.Status == "deciding" {
		c.Status = status
	}
	m.mu.Unlock()
	if gone && confirm && c.Kind == kindChange {
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

// maxAnswer bounds one answer the member writes.
const maxAnswer = 4000

// answerQuestion replies to a question card's request on the connection
// Hermes asked on: the member's answers by question, or none to skip them.
func (h *handler) answerQuestion(m *member, requestID string, answers map[string]*string) (card, error) {
	m.mu.Lock()
	m.prune()
	c, ok := m.cards[requestID]
	if !ok {
		m.mu.Unlock()
		return card{}, ErrChangeNotFound
	}
	if c.Kind != kindQuestion {
		m.mu.Unlock()
		return card{}, errNotAQuestion
	}
	if c.Status != cardAwaiting {
		view := *c
		m.mu.Unlock()
		return view, ErrChangeDecided
	}
	for id, answer := range answers {
		known := slices.ContainsFunc(c.Questions, func(q question) bool { return q.ID == id })
		if !known || (answer != nil && (utf8.RuneCountInString(*answer) > maxAnswer || !utf8.ValidString(*answer))) {
			m.mu.Unlock()
			return card{}, errInvalidAnswer
		}
	}
	c.Status = "deciding"
	owner := c.owner
	m.mu.Unlock()
	result := map[string]any{}
	if answers != nil {
		result["answers"] = answers
	}
	var err error = errApprovalGone
	if owner != nil && !owner.isClosed() {
		err = owner.send(rpcResult(json.RawMessage(strconv.Quote(owner.serverRequestOf(requestID))), result))
	}
	m.mu.Lock()
	c.decided = time.Now()
	switch {
	case err != nil:
		c.Status = cardExpired
	case answers == nil:
		c.Status = cardRejected
	default:
		c.Status = cardAnswered
		c.Answers = map[string]any{}
		for id, answer := range answers {
			if answer != nil {
				c.Answers[id] = *answer
			} else {
				c.Answers[id] = nil
			}
		}
	}
	view := *c
	m.mu.Unlock()
	h.broadcast(m, c)
	if err != nil {
		return view, errApprovalGone
	}
	return view, nil
}

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

// expire ends what a card Hermes withdrew still holds: the executor's change.
func (h *handler) expire(m *member, u *upstream, c *card) {
	if c.Kind != kindChange {
		h.broadcast(m, c)
		return
	}
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
