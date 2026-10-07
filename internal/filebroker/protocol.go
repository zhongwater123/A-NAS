package filebroker

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"syscall"

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
)

var knownOps = map[string]bool{
	opLstat: true, opScan: true, opMkdir: true, opMkdirAll: true, opRename: true, opRemove: true,
	opRemoveAll: true, opOpen: true, opCreateTemp: true, opCopy: true, opUsage: true,
}

type request struct {
	// Token is sent by the Product Service and removed before the request
	// reaches a worker.
	Token  string         `json:"token,omitempty"`
	Op     string         `json:"op"`
	Path   string         `json:"path,omitempty"`
	Target string         `json:"target,omitempty"`
	Prefix string         `json:"prefix,omitempty"`
	Skip   files.ScanSkip `json:"skip,omitempty"`
}

type response struct {
	Error   *wireError        `json:"error,omitempty"`
	Info    *files.FileInfo   `json:"info,omitempty"`
	Entries []files.ScanEntry `json:"entries,omitempty"`
	Path    string            `json:"path,omitempty"`
	Size    int64             `json:"size,omitempty"`
}

const (
	codeNotFound          = "not_found"
	codeExists            = "exists"
	codePermission        = "permission"
	codeForbidden         = "forbidden"
	codeUnauthenticated   = "unauthenticated"
	codeUnsupported       = "unsupported"
	codeInvalidName       = "invalid_name"
	codeNoSpace           = "no_space"
	codeVolumeUnavailable = "volume_unavailable"
	codeUnavailable       = "unavailable"
	codeInvalidRequest    = "invalid_request"
	codeFailed            = "failed"
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
		return target == fs.ErrNotExist
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
	case codeNoSpace:
		return target == files.ErrInsufficientSpace
	case codeVolumeUnavailable:
		return target == files.ErrVolumeUnavailable
	case codeUnavailable:
		return target == ErrUnavailable
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
	case errors.Is(err, files.ErrInvalidName):
		code = codeInvalidName
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT):
		code = codeNoSpace
	case errors.Is(err, syscall.ENOTEMPTY):
		code = codeExists
	}
	return &wireError{Code: code, Message: err.Error()}
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
