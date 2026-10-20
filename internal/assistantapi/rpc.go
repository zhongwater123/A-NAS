package assistantapi

import (
	"encoding/json"
	"fmt"
	"regexp"
	"unicode/utf8"
)

// The JSON-RPC surface of `hermes serve` that the assistant window may use
// (ADR 0024 decision 9; the allowlist and its reasons are in
// docs/research/hermes-agent-runtime.md, "白名单（第一版）"). Everything not
// listed here is refused by the Product Service, whatever Hermes would do.

// frame is one JSON-RPC message on /api/ws: a request, a response, an event
// (method "event") or a request from the server.
type frame struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

func (f frame) isEvent() bool         { return f.Method == "event" }
func (f frame) isServerRequest() bool { return f.Method != "" && f.Method != "event" && len(f.ID) > 0 }
func (f frame) isResponse() bool      { return f.Method == "" && len(f.ID) > 0 }

type event struct {
	Type      string          `json:"type"`
	SessionID string          `json:"session_id,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Seq       *int64          `json:"seq,omitempty"`
}

// JSON-RPC error codes the Product Service answers with itself.
const (
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeUnavailable    = -32000
)

var (
	// Hermes's own id forms: a runtime session id, a stored session id (a
	// conversation, or a run of a scheduled task: cron_<task>_<time>) and a
	// request from the server. Matching them exactly also shuts out Hermes's
	// lookups by title or by id prefix.
	runtimeIDPattern = regexp.MustCompile(`^[0-9a-f]{8}$`)
	storedIDPattern  = regexp.MustCompile(`^(?:[0-9]{8}_[0-9]{6}_[0-9a-f]{6}|cron_[0-9a-f]{12}_[0-9]{8}_[0-9]{6})$`)
	serverIDPattern  = regexp.MustCompile(`^srq-[0-9a-f]{12}$`)
	requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)
)

type check func(json.RawMessage) bool

func pattern(re *regexp.Regexp) check {
	return func(raw json.RawMessage) bool {
		var value string
		return json.Unmarshal(raw, &value) == nil && re.MatchString(value)
	}
}

func text(minRunes, maxBytes int) check {
	return func(raw json.RawMessage) bool {
		var value string
		if json.Unmarshal(raw, &value) != nil || len(value) > maxBytes || !utf8.ValidString(value) {
			return false
		}
		return utf8.RuneCountInString(value) >= minRunes
	}
}

func title(raw json.RawMessage) bool {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	count := utf8.RuneCountInString(value)
	return count >= 1 && count <= maxTitleRunes
}

func boolean(raw json.RawMessage) bool {
	var value bool
	return json.Unmarshal(raw, &value) == nil
}

func count(limit int64) check {
	return func(raw json.RawMessage) bool {
		var value int64
		return json.Unmarshal(raw, &value) == nil && value >= 0 && value <= limit
	}
}

var (
	runtimeID = pattern(runtimeIDPattern)
	storedID  = pattern(storedIDPattern)
	cols      = count(1000)
)

type method struct {
	params   map[string]check
	required []string
}

// clientMethods are forwarded to Hermes once their parameters pass. Any other
// key, including profile, source, cwd and model, is refused.
var clientMethods = map[string]method{
	"gateway.ping":        {},
	"ping":                {},
	"session.active_list": {},
	// No source: a desktop session would add tools the managed policy cannot
	// turn off; the session is tui, the default.
	"session.create": {params: map[string]check{"cols": cols}},
	"session.resume": {params: map[string]check{"session_id": storedID, "cols": cols, "omit_messages": boolean, "defer_history": boolean},
		required: []string{"session_id"}},
	"session.activate": {params: map[string]check{"session_id": runtimeID, "cols": cols, "omit_messages": boolean},
		required: []string{"session_id"}},
	"session.events.since": {params: map[string]check{"session_id": runtimeID, "last_seen": count(1 << 50)},
		required: []string{"session_id", "last_seen"}},
	"prompt.submit": {params: map[string]check{"session_id": runtimeID, "text": text(1, maxInputBytes)},
		required: []string{"session_id", "text"}},
	"session.interrupt": {params: map[string]check{"session_id": runtimeID}, required: []string{"session_id"}},
	"session.title":     {params: map[string]check{"session_id": runtimeID, "title": title}, required: []string{"session_id", "title"}},
	"session.close":     {params: map[string]check{"session_id": runtimeID}, required: []string{"session_id"}},
}

// localMethods are answered by the Product Service: it already declared to
// Hermes that it answers the server's requests, and approvals are its own.
var localMethods = map[string]bool{"client.capabilities": true, "approval.pending": true, "approval.received": true}

// checkParams returns the decoded parameters, or why they are refused.
func checkParams(name string, raw json.RawMessage) (map[string]json.RawMessage, error) {
	rule, ok := clientMethods[name]
	if !ok {
		return nil, fmt.Errorf("method %s is not available", name)
	}
	params := map[string]json.RawMessage{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, fmt.Errorf("params must be an object")
		}
	}
	for key, value := range params {
		valid, known := rule.params[key]
		if !known {
			return nil, fmt.Errorf("parameter %s is not accepted", key)
		}
		if !valid(value) {
			return nil, fmt.Errorf("parameter %s is invalid", key)
		}
	}
	for _, key := range rule.required {
		if _, ok := params[key]; !ok {
			return nil, fmt.Errorf("parameter %s is required", key)
		}
	}
	return params, nil
}

// forwardedEvents reach the window; the other events Hermes sends (subagents,
// voice, desktop control, billing, cron and more) are dropped. The approval
// events are not among them: cards are the Product Service's own.
var forwardedEvents = map[string]bool{
	"gateway.ready": true, "message.start": true, "message.delta": true, "message.interim": true, "message.complete": true,
	"reasoning.delta": true, "reasoning.available": true, "thinking.delta": true,
	"tool.generating": true, "tool.start": true, "tool.complete": true,
	"todo.updated": true, "status.update": true, "error": true,
	"session.info": true, "session.usage": true, "session.title": true, "sessions.changed": true, "session.reclaimed": true,
	// What the background review changed in memory and skills, in Hermes's words.
	"review.summary": true,
}

// hiddenInfo are the parts of a live session's info the window has no use
// for and the member need not see: the system prompt, the MCP servers and
// Hermes's own update state.
var hiddenInfo = []string{"system_prompt", "mcp_servers", "update_command", "update_behind"}

// sanitizeInfo removes hiddenInfo and reports whether the session's approvals
// were switched off, which the managed policy forbids.
func sanitizeInfo(info map[string]json.RawMessage) (bypassed bool) {
	for _, key := range hiddenInfo {
		delete(info, key)
	}
	var yolo bool
	if raw, ok := info["yolo"]; ok && json.Unmarshal(raw, &yolo) == nil && yolo {
		return true
	}
	var mode string
	if raw, ok := info["approval_mode"]; ok && json.Unmarshal(raw, &mode) == nil && mode != "" && mode != "manual" {
		return true
	}
	return false
}

func rpcError(id json.RawMessage, code int, message string) frame {
	data, _ := json.Marshal(map[string]any{"code": code, "message": message})
	return frame{JSONRPC: "2.0", ID: id, Error: data}
}

func rpcResult(id json.RawMessage, result any) frame {
	data, _ := json.Marshal(result)
	return frame{JSONRPC: "2.0", ID: id, Result: data}
}

func eventFrame(kind, sessionID string, payload any) frame {
	data, _ := json.Marshal(map[string]any{"type": kind, "session_id": sessionID, "payload": payload})
	return frame{JSONRPC: "2.0", Method: "event", Params: data}
}
