// Package sharefiles bridges shares to ordinary files without granting the
// sharing service access to members' spaces. Every operation retains its session.
package sharefiles

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/zhongwater123/A-NAS/internal/filebroker"
	"io"
	"os"
	"path"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/shares"
)

type Operations interface {
	Select(context.Context, shares.User, []string) ([]files.ShareSource, error)
	Open(context.Context, shares.User, string) (*os.File, error)
	Directory(context.Context, shares.User, shares.Target, string) (string, error)
	Stage(context.Context, shares.User, shares.Target, shares.Entry, io.Reader) (string, error)
	Commit(context.Context, shares.User, string) error
	Abort(context.Context, shares.User, string) error
	Fence(context.Context, string) error
}

type Backend struct{ ops Operations }

func NewBackend(ops Operations) *Backend                      { return &Backend{ops: ops} }
func (b *Backend) Fence(ctx context.Context, id string) error { return b.ops.Fence(ctx, id) }
func (b *Backend) Capture(ctx context.Context, user shares.User, ids []string, visit func(shares.Entry, io.Reader) error) error {
	before, err := b.ops.Select(ctx, user, ids)
	if err != nil {
		return err
	}
	for _, source := range before {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry := shares.Entry{ID: source.ID, Path: source.Path, IsDir: source.Kind == files.EntryKindDirectory, Size: source.SizeBytes}
		if entry.IsDir {
			if err := visit(entry, nil); err != nil {
				return err
			}
			continue
		}
		f, err := b.ops.Open(ctx, user, source.ID)
		if err != nil {
			return err
		}
		err = visit(entry, f)
		_ = f.Close()
		if err != nil {
			return err
		}
	}
	after, err := b.ops.Select(ctx, user, ids)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(before, after) {
		return errors.New("source_changed: 文件在准备过程中发生变化，请停止修改后重试")
	}
	return nil
}
func (b *Backend) Save(ctx context.Context, user shares.User, target shares.Target, entries []shares.Entry, open func(string) (io.ReadCloser, error), commit func(string, func() error) error, progress func(int64)) error {
	ordered := append([]shares.Entry(nil), entries...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	parents := map[string]string{"": target.ParentID}
	for _, entry := range ordered {
		if err := ctx.Err(); err != nil {
			return err
		}
		dir := path.Dir(entry.Path)
		if dir == "." {
			dir = ""
		}
		parent, ok := parents[dir]
		if !ok {
			return shares.ErrInvalid
		}
		destination := target
		destination.ParentID = parent
		if entry.IsDir {
			var id string
			err := commit(entry.ID, func() error {
				var err error
				id, err = b.ops.Directory(ctx, user, destination, path.Base(entry.Path))
				return err
			})
			if err != nil {
				return err
			}
			parents[entry.Path] = id
			continue
		}
		reader, err := open(entry.ID)
		if err != nil {
			return err
		}
		counted := &progressReader{Reader: reader, progress: progress}
		pending, err := b.ops.Stage(ctx, user, destination, entry, counted)
		_ = reader.Close()
		if err != nil {
			return err
		}
		err = commit(entry.ID, func() error { return b.ops.Commit(ctx, user, pending) })
		if err != nil {
			if errors.Is(err, shares.ErrPublicationUnknown) {
				// Do not wait for an unsettled publication while its domain task reports
				// uncertainty. Cleanup can only remove a still-pending temporary.
				go func() {
					cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
					defer cancel()
					_ = b.ops.Abort(cleanup, user, pending)
				}()
			} else {
				cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				_ = b.ops.Abort(cleanup, user, pending)
				cancel()
			}
			return err
		}
	}
	return nil
}

type progressReader struct {
	io.Reader
	progress func(int64)
}

func (r *progressReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if n > 0 && r.progress != nil {
		r.progress(int64(n))
	}
	return n, err
}

type generationKey struct{}
type operationKey struct{}
type pending struct {
	upload  *files.PendingUpload
	actor   accounts.User
	ctx     context.Context
	cancel  context.CancelFunc
	shareID string
	done    chan struct{}
}
type outcome struct {
	id  string
	err error
	at  time.Time
}
type publicationBroker interface {
	ResetShares(context.Context) (string, error)
	CheckShareEpoch(context.Context, string) error
	FenceShare(context.Context, string, string) error
}
type Local struct {
	Files      *files.Service
	Accounts   *accounts.Service
	mu         sync.Mutex
	fenced     map[string]bool
	pending    map[string]*pending
	outcomes   map[string]outcome
	generation string
	broker     publicationBroker
	cleanup    sync.WaitGroup
}

func NewLocal(fs *files.Service, users *accounts.Service) *Local {
	return &Local{Files: fs, Accounts: users, fenced: map[string]bool{}, pending: map[string]*pending{}, outcomes: map[string]outcome{}, generation: randomID()}
}

// SetBroker enables Host Agent publication fencing; call before serving production requests.
func (l *Local) SetBroker(c *filebroker.Client) { l.broker = c }
func randomID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func (l *Local) actor(ctx context.Context, u shares.User) (accounts.User, error) {
	if l.Accounts == nil {
		return accounts.User{}, shares.ErrUnauthorized
	}
	s, e := l.Accounts.ResolveSession(ctx, accounts.SessionToken(ctx))
	if e != nil || s.User.MustChangePassword || s.User.ID != u.ID {
		return accounts.User{}, shares.ErrUnauthorized
	}
	return s.User, nil
}
func (l *Local) Select(ctx context.Context, u shares.User, ids []string) ([]files.ShareSource, error) {
	a, e := l.actor(ctx, u)
	if e != nil {
		return nil, e
	}
	return l.Files.ShareSelection(ctx, a, ids)
}
func (l *Local) Open(ctx context.Context, u shares.User, id string) (*os.File, error) {
	a, e := l.actor(ctx, u)
	if e != nil {
		return nil, e
	}
	c, e := l.Files.OpenShareContent(ctx, a, id)
	return c.Reader, e
}
func (l *Local) currentGeneration(ctx context.Context) bool {
	v, _ := ctx.Value(generationKey{}).(string)
	return v == "" || v == l.generation
}
func (l *Local) generationMatches(v string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return v != "" && v == l.generation
}
func (l *Local) publicationContext(ctx context.Context, id string) context.Context {
	ctx = context.WithoutCancel(ctx)
	if l.broker != nil {
		ctx = filebroker.WithSharePublication(ctx, l.generation, id)
	}
	return ctx
}
func (l *Local) publish(ctx context.Context, id string, fn func() error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := ctx.Err(); e != nil {
		return e
	}
	if id == "" || l.fenced[id] || !l.currentGeneration(ctx) {
		return shares.ErrInactive
	}
	return fn()
}
func operationID(ctx context.Context) string {
	id, _ := ctx.Value(operationKey{}).(string)
	if id == "" {
		id = randomID()
	}
	return id
}
func (l *Local) roomLocked() bool {
	if len(l.outcomes) < 65536 {
		return true
	}
	cutoff := time.Now().Add(-time.Hour)
	for key, v := range l.outcomes {
		if v.at.Before(cutoff) {
			delete(l.outcomes, key)
		}
	}
	return len(l.outcomes) < 65536
}
func (l *Local) Directory(ctx context.Context, u shares.User, t shares.Target, name string) (string, error) {
	id := operationID(ctx)
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.currentGeneration(ctx) {
		return "", shares.ErrInactive
	}
	if old, ok := l.outcomes["directory/"+id]; ok {
		return old.id, old.err
	}
	a, err := l.actor(ctx, u)
	if err != nil {
		return "", err
	}
	if t.ShareID == "" || l.fenced[t.ShareID] {
		return "", shares.ErrInactive
	}
	if !l.roomLocked() {
		return "", shares.ErrBusy
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	ctx = l.publicationContext(ctx, t.ShareID)
	var result string
	for n := 0; n < 1000; n++ {
		candidate := name
		if n > 0 {
			candidate = fmt.Sprintf("%s (%d)", name, n)
		}
		entry, e := l.Files.CreateDirectory(ctx, a, t.RootID, t.ParentID, candidate)
		err = e
		if errors.Is(err, files.ErrConflict) {
			continue
		}
		if err == nil {
			result = entry.ID
		}
		break
	}
	if errors.Is(err, filebroker.ErrUnavailable) {
		err = shares.ErrPublicationUnknown
	}
	l.outcomes["directory/"+id] = outcome{result, err, time.Now()}
	return result, err
}
func (l *Local) Stage(ctx context.Context, u shares.User, t shares.Target, entry shares.Entry, reader io.Reader) (string, error) {
	id := operationID(ctx)
	a, err := l.actor(ctx, u)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithCancel(ctx)
	p := &pending{actor: a, ctx: ctx, cancel: cancel, shareID: t.ShareID, done: make(chan struct{})}
	l.mu.Lock()
	if !l.currentGeneration(ctx) || t.ShareID == "" || l.fenced[t.ShareID] {
		l.mu.Unlock()
		cancel()
		return "", shares.ErrInactive
	}
	if _, ok := l.outcomes["stage/"+id]; ok {
		l.mu.Unlock()
		cancel()
		return "", shares.ErrConflict
	}
	if _, ok := l.pending[id]; ok {
		l.mu.Unlock()
		cancel()
		return "", shares.ErrConflict
	}
	if len(l.pending) >= 64 || !l.roomLocked() {
		l.mu.Unlock()
		cancel()
		return "", shares.ErrBusy
	}
	l.pending[id] = p
	l.mu.Unlock()
	stop := context.AfterFunc(ctx, func() {
		if c, ok := reader.(io.Closer); ok {
			_ = c.Close()
		}
	})
	defer stop()
	p.upload, err = l.Files.BeginUpload(ctx, a, files.UploadRequest{SpaceID: t.RootID, ParentID: t.ParentID, Name: path.Base(entry.Path), Size: entry.Size, Conflict: files.ConflictRename})
	if err == nil {
		var n int64
		n, err = l.Files.WriteUpload(ctx, p.upload, 0, reader)
		if err == nil && n != entry.Size {
			err = io.ErrUnexpectedEOF
		}
		if err == nil {
			err = ctx.Err()
		}
	}
	l.mu.Lock()
	if !l.currentGeneration(ctx) || l.pending[id] != p {
		if err == nil {
			err = shares.ErrInactive
		}
	}
	if l.currentGeneration(ctx) {
		l.outcomes["stage/"+id] = outcome{id, err, time.Now()}
	}
	close(p.done)
	l.mu.Unlock()
	if err != nil {
		_ = l.Abort(context.WithoutCancel(ctx), u, id)
		return "", err
	}
	return id, nil
}
func (l *Local) Commit(ctx context.Context, u shares.User, id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.currentGeneration(ctx) {
		return shares.ErrInactive
	}
	if old, ok := l.outcomes["commit/"+id]; ok {
		return old.err
	}
	a, err := l.actor(ctx, u)
	if err != nil {
		return err
	}
	p := l.pending[id]
	if p == nil || p.actor.ID != a.ID {
		return shares.ErrNotFound
	}
	if l.fenced[p.shareID] {
		return shares.ErrInactive
	}
	select {
	case <-p.done:
	default:
		return shares.ErrBusy
	}
	if p.upload == nil {
		return shares.ErrInvalid
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	// Once accepted, wait for the worker even if the requesting connection dies.
	publication := l.publicationContext(ctx, p.shareID)
	_, err = l.Files.CommitUpload(publication, a, p.upload)
	if errors.Is(err, filebroker.ErrUnavailable) {
		err = shares.ErrPublicationUnknown
	}
	l.outcomes["commit/"+id] = outcome{id, err, time.Now()}
	if err == nil {
		delete(l.pending, id)
		p.cancel()
	}
	return err
}
func (l *Local) discard(p *pending) {
	p.cancel()
	l.cleanup.Add(1)
	go func() {
		defer l.cleanup.Done()
		<-p.done
		if p.upload != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(p.ctx), 10*time.Second)
			defer cancel()
			_ = l.Files.AbortUpload(cleanup, p.actor, p.upload)
		}
	}()
}
func (l *Local) Abort(ctx context.Context, u shares.User, id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.currentGeneration(ctx) {
		return shares.ErrInactive
	}
	p := l.pending[id]
	if p != nil && (u.ID == "" || p.actor.ID == u.ID) {
		delete(l.pending, id)
		l.discard(p)
	}
	// Cleanup may arrive before a delayed stage request. Tombstone its handle.
	if _, ok := l.outcomes["stage/"+id]; !ok {
		l.outcomes["stage/"+id] = outcome{id, shares.ErrInactive, time.Now()}
	}
	return nil
}
func (l *Local) Fence(ctx context.Context, id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.currentGeneration(ctx) {
		return shares.ErrInactive
	}
	if id == "" {
		return shares.ErrInvalid
	}
	if l.broker != nil {
		control, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if e := l.broker.FenceShare(control, l.generation, id); e != nil {
			return e
		}
	}
	l.fenced[id] = true
	for key, p := range l.pending {
		if p.shareID == id {
			delete(l.pending, key)
			l.discard(p)
		}
	}
	return nil
}
func (l *Local) Reset(ctx context.Context) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	generation := randomID()
	if l.broker != nil {
		var err error
		control, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		generation, err = l.broker.ResetShares(control)
		if err != nil {
			return "", err
		}
	}
	for _, p := range l.pending {
		l.discard(p)
	}
	l.pending = map[string]*pending{}
	l.outcomes = map[string]outcome{}
	l.fenced = map[string]bool{}
	l.generation = generation
	return generation, nil
}
func (l *Local) status(ctx context.Context, kind, id string) (string, error) {
	for {
		l.mu.Lock()
		if !l.currentGeneration(ctx) {
			l.mu.Unlock()
			return "", shares.ErrInactive
		}
		if old, ok := l.outcomes[kind+"/"+id]; ok {
			l.mu.Unlock()
			return old.id, old.err
		}
		p := l.pending[id]
		if kind != "stage" || p == nil {
			l.mu.Unlock()
			return "", shares.ErrNotFound
		}
		done := p.done
		l.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}
func validPath(v string) bool {
	return v != "" && v != "." && !strings.HasPrefix(v, "/") && !strings.ContainsAny(v, "\\\x00") && path.Clean(v) == v && !strings.HasPrefix(v, "../")
}

// Check validates both the bridge generation and the surviving Host Agent.
func (l *Local) Check(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.currentGeneration(ctx) {
		return shares.ErrInactive
	}
	if l.broker != nil {
		probe, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return l.broker.CheckShareEpoch(probe, l.generation)
	}
	return nil
}
