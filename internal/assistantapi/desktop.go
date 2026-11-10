package assistantapi

import (
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"
)

// VNAS operates the WebOS desktop of the member it is talking with (ADR 0024
// decision 8): the assistant's `desktop` MCP server
// (ai/assistant-spike/anas_desktop_mcp.py) has tools that read the page as a
// list of controls, act on a control and look at a picture of the page. Their
// calls come the companion tools' way (companion.go) and go, as anas.desktop
// events, to the window that is the conversation's browser and sent its
// prompt, which carries them out on the page it shows and answers with
// desktop.result.
//
// What a step may change is decided in that window, where the page's writes
// wait for the member (web/src/assistant/webosControl.ts); nothing here does.
// The tools are not Hermes's own browser tools: upstream removed the protocol
// that let a client serve them (docs/research/hermes-agent-runtime.md).

const (
	// desktopPrefix is how Hermes names the desktop server's tools.
	desktopPrefix = "mcp__desktop__"
	// maxDesktopResult bounds the result of one command the window carries
	// out: a picture of the page is the largest, a PNG the window keeps under
	// 4 MiB of base64.
	maxDesktopResult = 5 << 20
	// desktopRate is how many commands a member's windows get a minute: a
	// task on the desktop is a snapshot and a step, over and over.
	desktopRate = 120
	// maxDesktopText bounds what one step types, and maxDesktopName a control's
	// ref or a key's name.
	maxDesktopText = 4000
	maxDesktopName = 32
	// maxDesktopFailure bounds what the window says of a step it did not take.
	maxDesktopFailure = 500
)

// desktopReceipt is how long a command waits for the window's answer: the
// window asks the member about a step for 25 s at most, and then answers.
var desktopReceipt = 30 * time.Second

// desktopFailed is the status of a step the window did not take; its message
// says why. A step it took has the status "ok".
const desktopFailed = "failed"

var desktopFamily = &windowTools{prefix: desktopPrefix, tools: desktopTools, event: "anas.desktop", cancel: "anas.desktop.cancel",
	receipt: &desktopReceipt, rate: desktopRate, answer: desktopAnswer, refuse: desktopRefusal}

// desktopTools are the tools of the desktop server with their arguments. The
// window never leaves WebOS, so there is no tool that opens an address or goes
// back, and none that runs code in the page.
var desktopTools = map[string]toolRule{
	"snapshot": {args: map[string]companionParam{"full": flagParam}},
	"click":    {args: map[string]companionParam{"ref": textParam(desktopName)}, required: []string{"ref"}},
	"type": {args: map[string]companionParam{"ref": textParam(desktopName), "text": textParam(desktopText)},
		required: []string{"ref", "text"}},
	"press":  {args: map[string]companionParam{"key": textParam(desktopName)}, required: []string{"key"}},
	"scroll": {args: map[string]companionParam{"direction": textParam(oneOf("up", "down"))}, required: []string{"direction"}},
	"look":   {args: map[string]companionParam{"annotate": flagParam}},
}

// desktopName accepts a control's ref or a key's name; which refs and keys
// there are is the window's to say.
func desktopName(value any) (any, string) {
	if s, ok := plainText(value, maxDesktopName, false); ok {
		return s, ""
	}
	return nil, fmt.Sprintf("应为 1–%d 个字符", maxDesktopName)
}

// desktopText accepts what a step types into a control; nothing clears it.
func desktopText(value any) (any, string) {
	s, ok := value.(string)
	if !ok || len(s) > maxDesktopText || !utf8.ValidString(s) {
		return nil, fmt.Sprintf("应为不超过 %d 字节的文本", maxDesktopText)
	}
	return s, ""
}

func desktopRefusal(tool string, extra []string) string {
	if len(extra) > 0 {
		return fmt.Sprintf("%s 不接受参数 %s", tool, extra[0])
	}
	return ""
}

// desktopResultParams is what a window may answer a desktop command with.
var desktopResultParams = method{params: map[string]check{"session_id": runtimeID, "command_id": pattern(commandIDPattern),
	"ok": boolean, "result": object(maxDesktopResult), "error": object(4096)},
	required: []string{"session_id", "command_id", "ok"}}

// desktopAnswer turns a window's answer into the tool's result: what the
// step returned as the window gave it, or why the window did not take it.
func desktopAnswer(ok bool, raw json.RawMessage) map[string]any {
	if ok {
		return map[string]any{"status": "ok", "result": raw}
	}
	var failure struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &failure)
	message := failure.Message
	if message == "" {
		message = "这一步没有完成"
	}
	if utf8.RuneCountInString(message) > maxDesktopFailure {
		message = string([]rune(message)[:maxDesktopFailure])
	}
	return map[string]any{"status": desktopFailed, "message": message}
}

// attachParams is what a window sends to become a conversation's browser.
var attachParams = method{params: map[string]check{"session_id": runtimeID}, required: []string{"session_id"}}

// attachDesktop makes the window the browser of a conversation it opened: the
// window the conversation's steps on the desktop, and the companion's
// commands, go to. A window attaches before each prompt it sends, so they go
// to the device the member is using; while a turn runs, its browser stays the
// window it has, so that a step and its confirmation never move to another
// device (#160).
func (u *upstream) attachDesktop(f frame) frame {
	params, err := attachParams.check(f.Params)
	if err != nil {
		return rpcError(f.ID, codeInvalidParams, err.Error())
	}
	session := unquote(params["session_id"])
	if !u.watches(session) {
		return rpcError(f.ID, codeInvalidParams, "this window has not opened the conversation")
	}
	if !u.m.becomeBrowser(session, u) {
		return rpcError(f.ID, codeInvalidParams, "another window is the browser of the turn under way")
	}
	return rpcResult(f.ID, map[string]bool{"attached": true})
}
