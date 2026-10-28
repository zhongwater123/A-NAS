package rootbroker

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"syscall"

	"github.com/zhongwater123/A-NAS/internal/filebroker/wire"
	"github.com/zhongwater123/A-NAS/internal/unixmsg"

	"github.com/zhongwater123/A-NAS/internal/filesystem"
)

func encodeError(err error) *wire.Error {
	if err == nil {
		return nil
	}
	code := wire.CodeFailed
	switch {
	case errors.Is(err, fs.ErrNotExist):
		code = wire.CodeNotFound
	case errors.Is(err, fs.ErrExist):
		code = wire.CodeExists
	case errors.Is(err, fs.ErrPermission):
		code = wire.CodePermission
	case errors.Is(err, filesystem.ErrForbidden):
		code = wire.CodeForbidden
	case errors.Is(err, filesystem.ErrUnsupportedType), errors.Is(err, syscall.ELOOP), errors.Is(err, syscall.ENOTDIR):
		code = wire.CodeUnsupported
	case errors.Is(err, filesystem.ErrNameTooLong), errors.Is(err, syscall.ENAMETOOLONG):
		code = wire.CodeNameTooLong
	case errors.Is(err, filesystem.ErrInvalidName):
		code = wire.CodeInvalidName
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT):
		code = wire.CodeNoSpace
	case errors.Is(err, syscall.EROFS):
		// Only the worker sees the real mount: the Product Service's sandbox
		// shows the volume read-only even when it is writable.
		code = wire.CodeVolumeUnavailable
	case errors.Is(err, syscall.ENOTEMPTY):
		code = wire.CodeExists
	case errors.Is(err, context.Canceled):
		code = wire.CodeCanceled
	}
	return &wire.Error{Code: code, Message: err.Error()}
}

// execute runs one request against fsys. The worker calls it as the user;
// tests call it directly.
func execute(ctx context.Context, fsys filesystem.FileSystem, req wire.Request) (wire.Response, *os.File) {
	var result wire.Response
	var file *os.File
	var err error
	switch req.Op {
	case wire.OpLstat:
		var info filesystem.FileInfo
		if info, err = fsys.Lstat(ctx, req.Path); err == nil {
			result.Info = &info
		}
	case wire.OpScan:
		result.Entries, err = fsys.Scan(ctx, req.Path, req.Skip)
	case wire.OpMkdir:
		err = fsys.Mkdir(ctx, req.Path)
	case wire.OpMkdirAll:
		err = fsys.MkdirAll(ctx, req.Path)
	case wire.OpRename:
		err = fsys.Rename(ctx, req.Path, req.Target)
	case wire.OpRemove:
		err = fsys.Remove(ctx, req.Path)
	case wire.OpRemoveAll:
		err = fsys.RemoveAll(ctx, req.Path)
	case wire.OpOpen:
		file, err = fsys.Open(ctx, req.Path)
	case wire.OpCreateTemp:
		file, result.Path, err = fsys.CreateTemp(ctx, req.Path, req.Prefix)
	case wire.OpCopy:
		err = fsys.Copy(ctx, req.Path, req.Target)
	case wire.OpUsage:
		result.Size, err = fsys.Usage(ctx, req.Path)
	case wire.OpSetModTime:
		if req.ModTime.IsZero() {
			result.Error = &wire.Error{Code: wire.CodeInvalidRequest, Message: "modification time is missing"}
			return result, nil
		}
		err = fsys.SetModTime(ctx, req.Path, req.ModTime)
	default:
		err = errors.New("unknown file operation")
		result.Error = &wire.Error{Code: wire.CodeInvalidRequest, Message: err.Error()}
		return result, nil
	}
	if err != nil {
		wire.CloseFile(file)
		result = wire.Response{Error: encodeError(err)}
		return result, nil
	}
	return result, file
}

// WorkerArgument is the Host Agent subcommand that runs a file worker.
const WorkerArgument = "file-worker"

// workerDescriptor is the socket the broker passes as the first extra file.
const workerDescriptor = 3

// RunWorker serves file operations for the broker until it disconnects. It
// runs with the signed-in user's credentials, set by the broker before exec,
// and refuses to run as root.
func RunWorker(volumeRoot string) error {
	return runWorker(volumeRoot, filesystem.NewRootFileSystem(volumeRoot, false), wire.MaxMessage)
}

// runWorker is RunWorker over fsys with messages of up to limit bytes; test
// workers wrap the file system and lower the limit.
func runWorker(volumeRoot string, fsys filesystem.FileSystem, limit int) error {
	if os.Geteuid() == 0 || os.Getegid() == 0 {
		return errors.New("refusing to run a file worker as root")
	}
	syscall.Umask(0o007)
	if writable, ok := os.LookupEnv(ConfineVariable); ok {
		if err := confine(volumeRoot, writable); err != nil {
			return err
		}
	}
	file := os.NewFile(workerDescriptor, "file-broker")
	connection, err := net.FileConn(file)
	_ = file.Close()
	if err != nil {
		return err
	}
	conn, ok := connection.(*net.UnixConn)
	if !ok {
		_ = connection.Close()
		return errors.New("file worker descriptor is not a Unix socket")
	}
	defer conn.Close()
	return serveWorker(context.Background(), conn, fsys, limit)
}

// serveWorker answers the broker's requests one at a time. The broker sends
// the next request only after the last reply's final message, so the reader
// alongside sees nothing but cancel messages while a request runs; a cancel
// stops the request read before it, which may already be done.
func serveWorker(ctx context.Context, conn *net.UnixConn, fsys filesystem.FileSystem, limit int) error {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	type job struct {
		ctx    context.Context
		cancel context.CancelFunc
		req    wire.Request
	}
	jobs := make(chan job, 1)
	var readErr error
	go func() {
		defer close(jobs)
		cancel := context.CancelFunc(func() {})
		for {
			var req wire.Request
			extra, err := unixmsg.Read(conn, &req, limit)
			wire.CloseFile(extra)
			if err != nil {
				if !errors.Is(err, io.EOF) {
					readErr = err
				}
				// The broker is gone: nobody waits for the request running.
				stop()
				return
			}
			if req.Op == wire.OpCancel {
				cancel()
				continue
			}
			var jobCtx context.Context
			jobCtx, cancel = context.WithCancel(ctx)
			select {
			case jobs <- job{ctx: jobCtx, cancel: cancel, req: req}:
			case <-ctx.Done():
				cancel()
				return
			}
		}
	}()
	for j := range jobs {
		err := respond(j.ctx, conn, fsys, j.req, limit)
		j.cancel()
		if err != nil {
			if ctx.Err() != nil {
				// The broker went away while the reply was on its way.
				return nil
			}
			return err
		}
	}
	return readErr
}

// respond runs one request and sends its reply. Only a broken connection is
// an error: a reply that cannot be encoded within the limit becomes an error
// reply, so the worker stays up for the next request.
func respond(ctx context.Context, conn *net.UnixConn, fsys filesystem.FileSystem, req wire.Request, limit int) error {
	result, file := execute(ctx, fsys, req)
	defer wire.CloseFile(file)
	if req.Op == wire.OpScan && req.Paged && result.Error == nil {
		return sendEntries(ctx, conn, result.Entries, limit)
	}
	return sendReply(conn, result, file, limit)
}

// sendEntries sends a scan in pages of at most scanPage entries; a page over
// the message limit, as very long paths can make one, is halved until it
// fits. The broker relays each page as it comes instead of holding the whole
// tree. A cancel between pages ends the reply with an error.
func sendEntries(ctx context.Context, conn *net.UnixConn, entries []filesystem.ScanEntry, limit int) error {
	for {
		if err := ctx.Err(); err != nil {
			return sendReply(conn, wire.Response{Error: encodeError(err)}, nil, limit)
		}
		page := entries[:min(len(entries), wire.ScanPage)]
		body, err := unixmsg.Encode(wire.Response{Entries: page, More: len(page) < len(entries)}, limit)
		for errors.Is(err, wire.ErrTooLarge) && len(page) > 1 {
			page = page[:len(page)/2]
			body, err = unixmsg.Encode(wire.Response{Entries: page, More: true}, limit)
		}
		if err != nil {
			return sendReply(conn, wire.Response{Error: encodeError(err)}, nil, limit)
		}
		if err := wire.WriteEncoded(conn, body, nil); err != nil {
			return err
		}
		entries = entries[len(page):]
		if len(entries) == 0 {
			return nil
		}
	}
}

// sendReply sends one message, or an error in its place when the message
// cannot be encoded within the limit.
func sendReply(conn *net.UnixConn, result wire.Response, file *os.File, limit int) error {
	body, err := unixmsg.Encode(result, limit)
	if err != nil {
		file = nil
		if body, err = unixmsg.Encode(wire.Response{Error: encodeError(err)}, limit); err != nil {
			return err
		}
	}
	return wire.WriteEncoded(conn, body, file)
}
