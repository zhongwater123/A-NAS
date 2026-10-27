package filebroker

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"syscall"

	"github.com/zhongwater123/A-NAS/internal/filesystem"
)

// WorkerArgument is the Host Agent subcommand that runs a file worker.
const WorkerArgument = "file-worker"

// workerDescriptor is the socket the broker passes as the first extra file.
const workerDescriptor = 3

// RunWorker serves file operations for the broker until it disconnects. It
// runs with the signed-in user's credentials, set by the broker before exec,
// and refuses to run as root.
func RunWorker(volumeRoot string) error {
	return runWorker(volumeRoot, filesystem.NewRootFileSystem(volumeRoot, false))
}

// runWorker is RunWorker over fsys; test workers wrap the file system.
func runWorker(volumeRoot string, fsys filesystem.FileSystem) error {
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
	return serveWorker(context.Background(), conn, fsys)
}

// serveWorker answers the broker's requests one at a time. The broker sends
// the next request only after the last reply's final message, so the reader
// alongside sees nothing but cancel messages while a request runs; a cancel
// stops the request read before it, which may already be done.
func serveWorker(ctx context.Context, conn *net.UnixConn, fsys filesystem.FileSystem) error {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	type job struct {
		ctx    context.Context
		cancel context.CancelFunc
		req    request
	}
	jobs := make(chan job, 1)
	var readErr error
	go func() {
		defer close(jobs)
		cancel := context.CancelFunc(func() {})
		for {
			var req request
			extra, err := readMessage(conn, &req)
			closeFile(extra)
			if err != nil {
				if !errors.Is(err, io.EOF) {
					readErr = err
				}
				// The broker is gone: nobody waits for the request running.
				stop()
				return
			}
			if req.Op == opCancel {
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
		err := respond(j.ctx, conn, fsys, j.req)
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
func respond(ctx context.Context, conn *net.UnixConn, fsys filesystem.FileSystem, req request) error {
	result, file := execute(ctx, fsys, req)
	defer closeFile(file)
	if req.Op == opScan && req.Paged && result.Error == nil {
		return sendEntries(ctx, conn, result.Entries)
	}
	return sendReply(conn, result, file)
}

// sendEntries sends a scan in pages of at most scanPage entries; a page over
// the message limit, as very long paths can make one, is halved until it
// fits. The broker relays each page as it comes instead of holding the whole
// tree. A cancel between pages ends the reply with an error.
func sendEntries(ctx context.Context, conn *net.UnixConn, entries []filesystem.ScanEntry) error {
	for {
		if err := ctx.Err(); err != nil {
			return sendReply(conn, response{Error: encodeError(err)}, nil)
		}
		page := entries[:min(len(entries), scanPage)]
		body, err := encodeMessage(response{Entries: page, More: len(page) < len(entries)})
		for errors.Is(err, errTooLarge) && len(page) > 1 {
			page = page[:len(page)/2]
			body, err = encodeMessage(response{Entries: page, More: true})
		}
		if err != nil {
			return sendReply(conn, response{Error: encodeError(err)}, nil)
		}
		if err := writeEncoded(conn, body, nil); err != nil {
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
func sendReply(conn *net.UnixConn, result response, file *os.File) error {
	body, err := encodeMessage(result)
	if err != nil {
		file = nil
		if body, err = encodeMessage(response{Error: encodeError(err)}); err != nil {
			return err
		}
	}
	return writeEncoded(conn, body, file)
}
