// Package wire is the protocol between the Product Service, the root file
// broker and its workers: requests, replies and error codes, framed as a
// length, a JSON body and at most one passed descriptor. The client in
// internal/filebroker and the broker in internal/filebroker/rootbroker both
// speak it, and neither links the other (#221).
package wire

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"syscall"
	"time"

	"github.com/zhongwater123/A-NAS/internal/filesystem"
)

var (
	// ErrUnavailable reports that the File Broker or a worker could not be reached.
	ErrUnavailable = errors.New("file broker is unavailable")
	// The executor's refusals of the assistant's changes as the Product
	// Service sees them; internal/assistantchanges has the executor's own.
	ErrChangeNotFound = errors.New("no such change")
	ErrChangeInvalid  = errors.New("the change was refused")
	ErrChangeDecided  = errors.New("the change was already decided")
)

const (
	OpLstat      = "lstat"
	OpScan       = "scan"
	OpMkdir      = "mkdir"
	OpMkdirAll   = "mkdir_all"
	OpRename     = "rename"
	OpRemove     = "remove"
	OpRemoveAll  = "remove_all"
	OpOpen       = "open"
	OpCreateTemp = "create_temp"
	OpCopy       = "copy"
	OpUsage      = "usage"
	OpSetModTime = "set_mod_time"
	// opCancel goes from the broker to a worker only: it stops the request
	// the worker is running, which still answers.
	OpCancel = "cancel"
)

// Operations besides the file operations: a terminal, a connection to the
// member's assistant, the sharing service's fence (internal/shareservice),
// and the executor of 提交更改 (ADR 0024 decision 4). The Product Service
// prepares, approves, rejects and expires changes with the member's session,
// which the broker checks itself (decision 10); the assistant's MCP tool
// applies them on the assistant socket, where the member is the caller's UID.
const (
	OpTerminal      = "terminal"
	OpAssistant     = "assistant"
	OpShareReset    = "share_reset"
	OpShareFence    = "share_fence"
	OpShareCheck    = "share_check"
	OpChangePrepare = "change_prepare"
	OpChangeDecide  = "change_decide"
	OpChangeGet     = "change_get"
)

// IsFileOp reports whether op is one of the file operations a worker runs.
func IsFileOp(op string) bool {
	switch op {
	case OpLstat, OpScan, OpMkdir, OpMkdirAll, OpRename, OpRemove, OpRemoveAll, OpOpen, OpCreateTemp, OpCopy, OpUsage, OpSetModTime:
		return true
	}
	return false
}

type Request struct {
	ShareEpoch string `json:"shareEpoch,omitempty"`
	ShareID    string `json:"shareId,omitempty"`
	// Token is sent by the Product Service and removed before the request
	// reaches a worker.
	Token  string              `json:"token,omitempty"`
	Op     string              `json:"op"`
	Path   string              `json:"path,omitempty"`
	Target string              `json:"target,omitempty"`
	Prefix string              `json:"prefix,omitempty"`
	Skip   filesystem.ScanSkip `json:"skip"`
	// Paged says the caller reads a scan in pages. A Product Service from
	// before pages gets the scan in one message, never a truncated one.
	Paged bool `json:"paged,omitempty"`
	// ModTime is the modification time set_mod_time gives a file.
	ModTime time.Time `json:"modTime,omitzero"`
	Cols    uint16    `json:"cols,omitempty"`
	Rows    uint16    `json:"rows,omitempty"`
	// Change carries an operation on the assistant's changes.
	Change *ChangeRequest `json:"change,omitempty"`
	// Fresh asks for a new login to the assistant: the last access token was
	// refused (the instance restarted and signs with a new key).
	Fresh bool `json:"fresh,omitempty"`
}

type ChangeRequest struct {
	ID        string `json:"id,omitempty"`
	Decision  string `json:"decision,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	RequestID string `json:"requestId,omitempty"`
	// Arguments are the call of 提交更改 as Hermes reported it starting.
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type Response struct {
	// More says another message of the same reply follows: a scan's entries
	// travel in pages, and only the last message carries an error or a file.
	More    bool                   `json:"more,omitempty"`
	Error   *Error                 `json:"error,omitempty"`
	Info    *filesystem.FileInfo   `json:"info,omitempty"`
	Entries []filesystem.ScanEntry `json:"entries,omitempty"`
	Path    string                 `json:"path,omitempty"`
	Size    int64                  `json:"size,omitempty"`
	// ExitCode ends a terminal session.
	ExitCode *int `json:"exitCode,omitempty"`
	// Assistant comes with a connection to the member's assistant.
	Assistant *AssistantGrant `json:"assistant,omitempty"`
	// Change is a change of the assistant's as its card shows it.
	Change json.RawMessage `json:"changeCard,omitempty"`
}

type AssistantGrant struct {
	Username string `json:"username"`
	// Token is an access token of Hermes's own login to the instance.
	Token string `json:"token"`
}

const (
	CodeNotFound          = "not_found"
	CodeExists            = "exists"
	CodePermission        = "permission"
	CodeForbidden         = "forbidden"
	CodeUnauthenticated   = "unauthenticated"
	CodeUnsupported       = "unsupported"
	CodeInvalidName       = "invalid_name"
	CodeNameTooLong       = "name_too_long"
	CodeNoSpace           = "no_space"
	CodeVolumeUnavailable = "volume_unavailable"
	CodeUnavailable       = "unavailable"
	CodeInvalidRequest    = "invalid_request"
	CodeFailed            = "failed"
	CodeCanceled          = "canceled"
	// codeChangeInvalid says why a submission of the assistant cannot be applied.
	CodeChangeInvalid = "change_invalid"
	CodeChangeDecided = "change_decided"
)

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

func (e *Error) Error() string {
	if e.Message == "" {
		return "file broker: " + e.Code
	}
	return "file broker: " + e.Code + ": " + e.Message
}

// Is lets callers keep using the standard sentinels: a kernel permission
// denial is both fs.ErrPermission and filesystem.ErrForbidden.
func (e *Error) Is(target error) bool {
	switch e.Code {
	case CodeNotFound:
		return target == fs.ErrNotExist || target == ErrChangeNotFound
	case CodeChangeInvalid:
		return target == ErrChangeInvalid
	case CodeChangeDecided:
		return target == ErrChangeDecided
	case CodeExists:
		return target == fs.ErrExist
	case CodePermission:
		return target == fs.ErrPermission || target == filesystem.ErrForbidden
	case CodeForbidden, CodeUnauthenticated:
		return target == filesystem.ErrForbidden
	case CodeUnsupported:
		return target == filesystem.ErrUnsupportedType
	case CodeInvalidName:
		return target == filesystem.ErrInvalidName
	case CodeNameTooLong:
		return target == filesystem.ErrInvalidName || target == filesystem.ErrNameTooLong || target == syscall.ENAMETOOLONG
	case CodeNoSpace:
		return target == filesystem.ErrInsufficientSpace
	case CodeVolumeUnavailable:
		return target == filesystem.ErrVolumeUnavailable
	case CodeUnavailable:
		return target == ErrUnavailable
	case CodeCanceled:
		return target == context.Canceled
	}
	return false
}

// Pages gathers the entries of a scan that arrives in several messages.
type Pages []filesystem.ScanEntry

func (p *Pages) Add(page Response) { *p = append(*p, page.Entries...) }

// Complete puts the entries gathered so far before the final message's.
func (p Pages) Complete(final Response) Response {
	if final.Error == nil && len(p) > 0 {
		final.Entries = append(p, final.Entries...)
	}
	return final
}
