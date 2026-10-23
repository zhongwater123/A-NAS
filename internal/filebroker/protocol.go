package filebroker

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"syscall"
	"time"

	"github.com/zhongwater123/A-NAS/internal/assistantchanges"
	"github.com/zhongwater123/A-NAS/internal/files"
)

// ErrUnavailable reports that the File Broker or a worker could not be reached.
var ErrUnavailable = errors.New("file broker is unavailable")

const (
	opLstat      = "lstat"
	opScan       = "scan"
	opMkdir      = "mkdir"
	opMkdirAll   = "mkdir_all"
	opRename     = "rename"
	opRemove     = "remove"
	opRemoveAll  = "remove_all"
	opOpen       = "open"
	opCreateTemp = "create_temp"
	opCopy       = "copy"
	opUsage      = "usage"
	opSetModTime = "set_mod_time"
	// opCancel goes from the broker to a worker only: it stops the request
	// the worker is running, which still answers.
	opCancel = "cancel"
)

var knownOps = map[string]bool{
	opLstat: true, opScan: true, opMkdir: true, opMkdirAll: true, opRename: true, opRemove: true,
	opRemoveAll: true, opOpen: true, opCreateTemp: true, opCopy: true, opUsage: true, opSetModTime: true,
}

type request struct {
	ShareEpoch string `json:"shareEpoch,omitempty"`
	ShareID    string `json:"shareId,omitempty"`
	// Token is sent by the Product Service and removed before the request
	// reaches a worker.
	Token  string         `json:"token,omitempty"`
	Op     string         `json:"op"`
	Path   string         `json:"path,omitempty"`
	Target string         `json:"target,omitempty"`
	Prefix string         `json:"prefix,omitempty"`
	Skip   files.ScanSkip `json:"skip"`
	// Paged says the caller reads a scan in pages. A Product Service from
	// before pages gets the scan in one message, never a truncated one.
	Paged bool `json:"paged,omitempty"`
	// ModTime is the modification time set_mod_time gives a file.
	ModTime time.Time `json:"modTime,omitzero"`
	Cols    uint16    `json:"cols,omitempty"`
	Rows    uint16    `json:"rows,omitempty"`
	// Change carries an operation on the assistant's changes.
	Change *changeRequest `json:"change,omitempty"`
	// Fresh asks for a new login to the assistant: the last access token was
	// refused (the instance restarted and signs with a new key).
	Fresh bool `json:"fresh,omitempty"`
}

type changeRequest struct {
	ID        string `json:"id,omitempty"`
	Decision  string `json:"decision,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	RequestID string `json:"requestId,omitempty"`
	// Arguments are the call of 提交更改 as Hermes reported it starting.
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type response struct {
	// More says another message of the same reply follows: a scan's entries
	// travel in pages, and only the last message carries an error or a file.
	More    bool              `json:"more,omitempty"`
	Error   *wireError        `json:"error,omitempty"`
	Info    *files.FileInfo   `json:"info,omitempty"`
	Entries []files.ScanEntry `json:"entries,omitempty"`
	Path    string            `json:"path,omitempty"`
	Size    int64             `json:"size,omitempty"`
	// ExitCode ends a terminal session.
	ExitCode *int `json:"exitCode,omitempty"`
	// Assistant comes with a connection to the member's assistant.
	Assistant *assistantGrant `json:"assistant,omitempty"`
	// Change is a change of the assistant's as its card shows it.
	Change json.RawMessage `json:"changeCard,omitempty"`
}

type assistantGrant struct {
	Username string `json:"username"`
	// Token is an access token of Hermes's own login to the instance.
	Token string `json:"token"`
}

const (
	codeNotFound          = "not_found"
	codeExists            = "exists"
	codePermission        = "permission"
	codeForbidden         = "forbidden"
	codeUnauthenticated   = "unauthenticated"
	codeUnsupported       = "unsupported"
	codeInvalidName       = "invalid_name"
	codeNameTooLong       = "name_too_long"
	codeNoSpace           = "no_space"
	codeVolumeUnavailable = "volume_unavailable"
	codeUnavailable       = "unavailable"
	codeInvalidRequest    = "invalid_request"
	codeFailed            = "failed"
	codeCanceled          = "canceled"
	// codeChangeInvalid says why a submission of the assistant cannot be applied.
	codeChangeInvalid = "change_invalid"
	codeChangeDecided = "change_decided"
)

type wireError struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

func (e *wireError) Error() string {
	if e.Message == "" {
		return "file broker: " + e.Code
	}
	return "file broker: " + e.Code + ": " + e.Message
}

// Is lets callers keep using the standard sentinels: a kernel permission
// denial is both fs.ErrPermission and files.ErrForbidden.
func (e *wireError) Is(target error) bool {
	switch e.Code {
	case codeNotFound:
		return target == fs.ErrNotExist || target == assistantchanges.ErrNotFound
	case codeChangeInvalid:
		return target == assistantchanges.ErrInvalid
	case codeChangeDecided:
		return target == assistantchanges.ErrDecided
	case codeExists:
		return target == fs.ErrExist
	case codePermission:
		return target == fs.ErrPermission || target == files.ErrForbidden
	case codeForbidden, codeUnauthenticated:
		return target == files.ErrForbidden
	case codeUnsupported:
		return target == files.ErrUnsupportedType
	case codeInvalidName:
		return target == files.ErrInvalidName
	case codeNameTooLong:
		return target == files.ErrInvalidName || target == files.ErrNameTooLong || target == syscall.ENAMETOOLONG
	case codeNoSpace:
		return target == files.ErrInsufficientSpace
	case codeVolumeUnavailable:
		return target == files.ErrVolumeUnavailable
	case codeUnavailable:
		return target == ErrUnavailable
	case codeCanceled:
		return target == context.Canceled
	}
	return false
}

func encodeError(err error) *wireError {
	if err == nil {
		return nil
	}
	code := codeFailed
	switch {
	case errors.Is(err, fs.ErrNotExist):
		code = codeNotFound
	case errors.Is(err, fs.ErrExist):
		code = codeExists
	case errors.Is(err, fs.ErrPermission):
		code = codePermission
	case errors.Is(err, files.ErrForbidden):
		code = codeForbidden
	case errors.Is(err, files.ErrUnsupportedType), errors.Is(err, syscall.ELOOP), errors.Is(err, syscall.ENOTDIR):
		code = codeUnsupported
	case errors.Is(err, files.ErrNameTooLong), errors.Is(err, syscall.ENAMETOOLONG):
		code = codeNameTooLong
	case errors.Is(err, files.ErrInvalidName):
		code = codeInvalidName
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT):
		code = codeNoSpace
	case errors.Is(err, syscall.EROFS):
		// Only the worker sees the real mount: the Product Service's sandbox
		// shows the volume read-only even when it is writable.
		code = codeVolumeUnavailable
	case errors.Is(err, syscall.ENOTEMPTY):
		code = codeExists
	case errors.Is(err, context.Canceled):
		code = codeCanceled
	}
	return &wireError{Code: code, Message: err.Error()}
}

// pages gathers the entries of a scan that arrives in several messages.
type pages []files.ScanEntry

func (p *pages) add(page response) { *p = append(*p, page.Entries...) }

// complete puts the entries gathered so far before the final message's.
func (p pages) complete(final response) response {
	if final.Error == nil && len(p) > 0 {
		final.Entries = append(p, final.Entries...)
	}
	return final
}

// execute runs one request against fsys. The worker calls it as the user;
// tests call it directly.
func execute(ctx context.Context, fsys files.FileSystem, req request) (response, *os.File) {
	var result response
	var file *os.File
	var err error
	switch req.Op {
	case opLstat:
		var info files.FileInfo
		if info, err = fsys.Lstat(ctx, req.Path); err == nil {
			result.Info = &info
		}
	case opScan:
		result.Entries, err = fsys.Scan(ctx, req.Path, req.Skip)
	case opMkdir:
		err = fsys.Mkdir(ctx, req.Path)
	case opMkdirAll:
		err = fsys.MkdirAll(ctx, req.Path)
	case opRename:
		err = fsys.Rename(ctx, req.Path, req.Target)
	case opRemove:
		err = fsys.Remove(ctx, req.Path)
	case opRemoveAll:
		err = fsys.RemoveAll(ctx, req.Path)
	case opOpen:
		file, err = fsys.Open(ctx, req.Path)
	case opCreateTemp:
		file, result.Path, err = fsys.CreateTemp(ctx, req.Path, req.Prefix)
	case opCopy:
		err = fsys.Copy(ctx, req.Path, req.Target)
	case opUsage:
		result.Size, err = fsys.Usage(ctx, req.Path)
	case opSetModTime:
		if req.ModTime.IsZero() {
			result.Error = &wireError{Code: codeInvalidRequest, Message: "modification time is missing"}
			return result, nil
		}
		err = fsys.SetModTime(ctx, req.Path, req.ModTime)
	default:
		err = errors.New("unknown file operation")
		result.Error = &wireError{Code: codeInvalidRequest, Message: err.Error()}
		return result, nil
	}
	if err != nil {
		closeFile(file)
		result = response{Error: encodeError(err)}
		return result, nil
	}
	return result, file
}
