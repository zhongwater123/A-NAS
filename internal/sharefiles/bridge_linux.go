//go:build linux

package sharefiles

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/shares"
	"github.com/zhongwater123/A-NAS/internal/unixmsg"
)

type wireRequest struct {
	Op         string        `json:"op"`
	Token      string        `json:"token,omitempty"`
	Generation string        `json:"generation,omitempty"`
	IDs        []string      `json:"ids,omitempty"`
	ID         string        `json:"id,omitempty"`
	ShareID    string        `json:"shareId,omitempty"`
	Target     shares.Target `json:"target"`
	Entry      shares.Entry  `json:"entry"`
	Name       string        `json:"name,omitempty"`
}
type wireResponse struct {
	ID    string              `json:"id,omitempty"`
	Items []files.ShareSource `json:"items,omitempty"`
	Code  string              `json:"code,omitempty"`
}

const wireLimit = 8 << 20

// writeWire and readWire speak internal/unixmsg; an oversized message or
// one whose descriptors were cut is invalid.
func writeWire(conn *net.UnixConn, value any, file *os.File) error {
	return asInvalid(unixmsg.Write(conn, value, file, wireLimit))
}

func readWire(conn *net.UnixConn, value any) (*os.File, error) {
	file, err := unixmsg.Read(conn, value, wireLimit)
	return file, asInvalid(err)
}

func asInvalid(err error) error {
	if errors.Is(err, unixmsg.ErrTooLarge) || errors.Is(err, unixmsg.ErrTruncated) {
		return shares.ErrInvalid
	}
	return err
}

func wireError(err error) string {
	switch {
	case errors.Is(err, shares.ErrBusy):
		return "busy"
	case errors.Is(err, shares.ErrInvalid):
		return "invalid"
	case errors.Is(err, shares.ErrPublicationUnknown):
		return "publication_unknown"
	case err == nil:
		return ""
	case errors.Is(err, shares.ErrInactive):
		return "inactive"
	case errors.Is(err, shares.ErrUnauthorized), errors.Is(err, files.ErrForbidden):
		return "unauthorized"
	case errors.Is(err, files.ErrNotFound), errors.Is(err, shares.ErrNotFound):
		return "not_found"
	case errors.Is(err, files.ErrInsufficientSpace):
		return "no_space"
	case errors.Is(err, files.ErrVolumeUnavailable):
		return "volume_unavailable"
	case errors.Is(err, files.ErrConflict):
		return "conflict"
	default:
		return "files_failed"
	}
}
func responseError(code string) error {
	switch code {
	case "publication_unknown":
		return shares.ErrPublicationUnknown
	case "busy":
		return shares.ErrBusy
	case "invalid":
		return shares.ErrInvalid
	case "":
		return nil
	case "inactive":
		return shares.ErrInactive
	case "unauthorized":
		return shares.ErrUnauthorized
	case "not_found":
		return shares.ErrNotFound
	case "no_space":
		return files.ErrInsufficientSpace
	case "volume_unavailable":
		return files.ErrVolumeUnavailable
	case "conflict":
		return files.ErrConflict
	default:
		return errors.New("share file operation failed")
	}
}

// Serve exposes the bridge on a product-owned directory. The installer must
// create that directory as a-nas:a-nas-shares 0750. We explicitly inherit its
// group for the socket; ordinary directory permissions do not imply setgid.
func Serve(parent context.Context, socket string, local *Local) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(socket), 0750); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("bridge path is not a socket")
		}
		if err = os.Remove(socket); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(filepath.Dir(socket))
	if err == nil {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			err = errors.New("bridge parent has no owner")
		} else {
			err = os.Chown(socket, -1, int(stat.Gid))
		}
	}
	if err == nil {
		err = os.Chmod(socket, 0660)
	}
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	accepted := make(chan struct{})
	slots := make(chan struct{}, 32)
	stop := context.AfterFunc(ctx, func() { _ = listener.Close() })
	go func() {
		defer close(accepted)
		for {
			conn, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			select {
			case slots <- struct{}{}:
			default:
				conn.Close()
				continue
			}
			wg.Go(func() {
				defer func() { <-slots }()
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				stopConn := context.AfterFunc(ctx, func() { conn.Close() })
				defer stopConn()
				serveConnection(ctx, conn, local)
			})
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			stop()
			_ = listener.Close()
			<-accepted
			_, _ = local.Reset(context.Background())
			wg.Wait()
			local.cleanup.Wait()
		})
	}, nil
}
func serveConnection(ctx context.Context, conn *net.UnixConn, local *Local) {
	var req wireRequest
	inbound, err := readWire(conn, &req)
	if inbound != nil {
		_ = inbound.Close()
		err = shares.ErrInvalid
	}
	if err != nil {
		return
	}
	_ = conn.SetDeadline(time.Now().Add(2 * time.Hour))
	if req.Op == "reset" {
		id, e := local.Reset(ctx)
		_ = writeWire(conn, wireResponse{ID: id, Code: wireError(e)}, nil)
		return
	}
	if !local.generationMatches(req.Generation) {
		_ = writeWire(conn, wireResponse{Code: "inactive"}, nil)
		return
	}
	ctx = accounts.WithSessionToken(context.WithValue(ctx, generationKey{}, req.Generation), req.Token)
	if req.ID != "" {
		if len(req.ID) > 256 {
			return
		}
		ctx = context.WithValue(ctx, operationKey{}, req.ID)
	}
	var resp wireResponse
	var descriptor *os.File
	switch req.Op {
	case "check":
		err = local.Check(ctx)
	case "fence":
		err = local.Fence(ctx, req.ShareID)
	case "abort":
		err = local.Abort(ctx, shares.User{}, req.ID)
	case "status":
		resp.ID, err = local.status(ctx, req.Name, req.ID)
	default:
		session, e := local.Accounts.ResolveSession(ctx, req.Token)
		if e != nil || session.User.MustChangePassword {
			_ = writeWire(conn, wireResponse{Code: "unauthorized"}, nil)
			return
		}
		actor := shares.User{ID: session.User.ID, Username: session.User.Username}
		req.Target.ShareID = req.ShareID
		switch req.Op {
		case "select":
			resp.Items, err = local.Select(ctx, actor, req.IDs)
		case "open":
			descriptor, err = local.Open(ctx, actor, req.ID)
		case "directory":
			if req.ID == "" {
				err = shares.ErrInvalid
			} else {
				resp.ID, err = local.Directory(ctx, actor, req.Target, req.Name)
			}
		case "stage":
			if req.ID == "" || !validPath(req.Entry.Path) || req.Entry.Size < 0 {
				err = shares.ErrInvalid
			} else {
				resp.ID, err = local.Stage(ctx, actor, req.Target, req.Entry, conn)
			}
		case "commit":
			err = local.Commit(ctx, actor, req.ID)
		default:
			err = shares.ErrInvalid
		}
	}
	if descriptor != nil {
		defer func() { _ = descriptor.Close() }()
	}
	resp.Code = wireError(err)
	_ = writeWire(conn, resp, descriptor)
}

type Remote struct{ socket, generation string }

func NewRemote(ctx context.Context, socket string) (*Remote, error) {
	r := &Remote{socket: socket}
	resp, f, err := r.call(ctx, wireRequest{Op: "reset"}, nil)
	if f != nil {
		_ = f.Close()
	}
	if err != nil {
		return nil, err
	}
	if resp.ID == "" {
		return nil, io.ErrUnexpectedEOF
	}
	r.generation = resp.ID
	return r, nil
}
func (r *Remote) call(ctx context.Context, req wireRequest, body io.Reader) (wireResponse, *os.File, error) {
	req.Token = accounts.SessionToken(ctx)
	req.Generation = r.generation
	var dialer net.Dialer
	c, err := dialer.DialContext(ctx, "unix", r.socket)
	if err != nil {
		return wireResponse{}, nil, err
	}
	conn := c.(*net.UnixConn)
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err = writeWire(conn, req, nil); err != nil {
		return wireResponse{}, nil, err
	}
	if body != nil {
		if _, err = io.CopyN(conn, body, req.Entry.Size); err != nil {
			return wireResponse{}, nil, err
		}
	}
	var resp wireResponse
	f, err := readWire(conn, &resp)
	if err == nil {
		err = responseError(resp.Code)
	}
	if err != nil && f != nil {
		_ = f.Close()
		f = nil
	}
	return resp, f, err
}
func (r *Remote) Select(ctx context.Context, _ shares.User, ids []string) ([]files.ShareSource, error) {
	resp, f, e := r.call(ctx, wireRequest{Op: "select", IDs: ids}, nil)
	if f != nil {
		_ = f.Close()
	}
	return resp.Items, e
}
func (r *Remote) Open(ctx context.Context, _ shares.User, id string) (*os.File, error) {
	_, f, e := r.call(ctx, wireRequest{Op: "open", ID: id}, nil)
	if e == nil && f == nil {
		e = io.ErrUnexpectedEOF
	}
	return f, e
}
func (r *Remote) simple(ctx context.Context, req wireRequest, body io.Reader) (string, error) {
	resp, f, e := r.call(ctx, req, body)
	if f != nil {
		_ = f.Close()
	}
	return resp.ID, e
}

// Publication retries reuse an operation ID. Status reconciles a reply lost
// after publication, including when the user's session has since expired.
func (r *Remote) reliable(ctx context.Context, req wireRequest) (string, error) {
	return r.reliableWithin(ctx, req, 15*time.Second, 15*time.Second)
}
func (r *Remote) reliableWithin(ctx context.Context, req wireRequest, attemptLimit, reconcileLimit time.Duration) (string, error) {
	attempt, stopAttempt := context.WithTimeout(ctx, attemptLimit)
	resp, f, err := r.call(attempt, req, nil)
	stopAttempt()
	if f != nil {
		_ = f.Close()
	}
	if err == nil || resp.Code != "" {
		return resp.ID, err
	}
	retry, cancel := context.WithTimeout(context.WithoutCancel(ctx), reconcileLimit)
	defer cancel()
	for range 3 {
		result, f, e := r.call(retry, wireRequest{Op: "status", ID: req.ID, Name: req.Op}, nil)
		if f != nil {
			_ = f.Close()
		}
		if e == nil {
			return result.ID, nil
		}
		if result.Code == "inactive" {
			return "", shares.ErrPublicationUnknown
		}
		if result.Code != "" && result.Code != "not_found" {
			return "", e
		}
		if result.Code == "not_found" {
			result, f, e = r.call(retry, req, nil)
			if f != nil {
				_ = f.Close()
			}
			if e == nil || result.Code != "" {
				return result.ID, e
			}
		}
		if retry.Err() != nil {
			return "", shares.ErrPublicationUnknown
		}
	}
	return "", shares.ErrPublicationUnknown
}
func (r *Remote) Directory(ctx context.Context, _ shares.User, t shares.Target, name string) (string, error) {
	return r.reliable(ctx, wireRequest{Op: "directory", ID: randomID(), Target: t, ShareID: t.ShareID, Name: name})
}
func (r *Remote) Stage(ctx context.Context, _ shares.User, t shares.Target, e shares.Entry, reader io.Reader) (string, error) {
	id := randomID()
	req := wireRequest{Op: "stage", ID: id, Target: t, ShareID: t.ShareID, Entry: e}
	resp, f, err := r.call(ctx, req, reader)
	if f != nil {
		_ = f.Close()
	}
	if err == nil {
		return resp.ID, nil
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if resp.Code == "" {
		result, f, e := r.call(cleanup, wireRequest{Op: "status", ID: id, Name: "stage"}, nil)
		if f != nil {
			_ = f.Close()
		}
		if e == nil {
			return result.ID, nil
		}
	}
	_, _ = r.simple(cleanup, wireRequest{Op: "abort", ID: id}, nil)
	return "", err
}
func (r *Remote) Commit(ctx context.Context, _ shares.User, id string) error {
	_, e := r.reliable(ctx, wireRequest{Op: "commit", ID: id})
	return e
}
func (r *Remote) Abort(ctx context.Context, _ shares.User, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, e := r.simple(ctx, wireRequest{Op: "abort", ID: id}, nil)
	return e
}
func (r *Remote) Fence(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, e := r.simple(ctx, wireRequest{Op: "fence", ShareID: id}, nil)
	return e
}

// Check is a nonmutating health probe for process supervision.
func (r *Remote) Check(ctx context.Context) error {
	_, err := r.simple(ctx, wireRequest{Op: "check"}, nil)
	return err
}
