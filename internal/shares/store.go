package shares

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type requestRecord struct {
	Hash      string
	ID        string
	MessageID int64
}
type storedJob struct {
	Job    Job
	Target Target
}
type event struct {
	Notification Notification
	To           string
}
type record struct {
	Share          Share
	Threads        map[string][]Message
	Jobs           map[string]*storedJob
	Requests       map[string]requestRecord
	Events         []event
	Saved          map[string]map[string]bool
	ContentRemoved bool
	dirty          bool
}
type running struct {
	shareID string
	cancel  context.CancelFunc
	save    bool
	ctx     context.Context
	user    User
}
type Store struct {
	mu              sync.Mutex
	db              *sql.DB
	dir             string
	options         Options
	records         map[string]*record
	running         map[string]running
	streams         map[string]*contentReader
	nextEvent       int64
	wg              sync.WaitGroup
	stop            chan struct{}
	closed          bool
	interruptReason string
}

func Open(dbPath, contentDir string, options Options) (*Store, error) {
	if dbPath == "" || contentDir == "" || options.Backend == nil || options.Active == nil {
		return nil, ErrInvalid
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.MaxTasks <= 0 {
		options.MaxTasks = 4
	}
	if options.CheckVolume != nil {
		if err := options.CheckVolume(context.Background()); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(contentDir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(contentDir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalid
	}
	db, err := sql.Open("sqlite3", dbPath+"?_busy_timeout=5000&_journal_mode=WAL&_synchronous=FULL")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, dir: contentDir, options: options, records: map[string]*record{}, running: map[string]running{}, streams: map[string]*contentReader{}, stop: make(chan struct{})}
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS share_documents(id TEXT PRIMARY KEY, document BLOB NOT NULL)`); err != nil {
		db.Close()
		return nil, err
	}
	rows, err := db.Query(`SELECT document FROM share_documents`)
	if err != nil {
		db.Close()
		return nil, err
	}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			break
		}
		var r record
		if err = json.Unmarshal(raw, &r); err != nil {
			break
		}
		s.records[r.Share.ID] = &r
		for _, e := range r.Events {
			if e.Notification.ID > s.nextEvent {
				s.nextEvent = e.Notification.ID
			}
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		db.Close()
		return nil, err
	}
	for _, r := range s.records {
		changed := false
		if r.Share.Status == "preparing" {
			r.Share.Status = "failed"
			r.Share.Error = "Preparation interrupted by service restart"
			changed = true
		}
		for _, j := range r.Jobs {
			if j.Job.Status == "running" {
				j.Job.Status = "interrupted"
				j.Job.Error = "Save interrupted by service restart; complete target files may exist and the saved count may be incomplete"
				if p := recipient(r, j.Job.UserID); p != nil {
					p.SaveUncertain = true
				}
				now := s.now()
				j.Job.FinishedAt = &now
				changed = true
			}
		}
		if changed {
			if err = s.persist(r); err != nil {
				db.Close()
				return nil, err
			}
		}
	}
	// Only generated object directories are managed; unknown files are untouched.
	children, err := os.ReadDir(s.dir)
	if err != nil {
		db.Close()
		return nil, err
	}
	for _, child := range children {
		if validID(child.Name()) && s.records[child.Name()] == nil {
			if err = os.RemoveAll(filepath.Join(s.dir, child.Name())); err != nil {
				db.Close()
				return nil, err
			}
		}
	}
	s.wg.Go(func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-ticker.C:
				_ = s.Maintain(context.Background())
			}
		}
	})
	return s, nil
}
func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.stop)
	for _, r := range s.running {
		r.cancel()
	}
	for _, r := range s.streams {
		r.stop()
	}
	s.mu.Unlock()
	s.wg.Wait()
	return s.db.Close()
}

// Interrupt stops this store generation without restarting or expanding any
// accepted task. A fresh store/bridge handshake is required before new work.
func (s *Store) Interrupt(reason string) error {
	s.mu.Lock()
	if !s.closed {
		s.interruptReason = reason
	}
	s.mu.Unlock()
	return s.Close()
}
func (s *Store) now() time.Time { return s.options.Now().UTC() }
func (s *Store) persist(r *record) error {
	r.dirty = true
	if err := s.volume(context.Background()); err != nil {
		return err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO share_documents(id,document) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET document=excluded.document`, r.Share.ID, raw)
	if err == nil {
		r.dirty = false
	}
	return err
}
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}
func validText(text string, empty bool) bool {
	return utf8.ValidString(text) && len(text) <= MaxMessageBytes && (empty || strings.TrimSpace(text) != "")
}
func validKey(key string) bool {
	return len(key) > 0 && len(key) <= 128 && utf8.ValidString(key) && !strings.ContainsRune(key, 0)
}
func hash(value any) string {
	raw, _ := json.Marshal(value)
	v := sha256.Sum256(raw)
	return hex.EncodeToString(v[:])
}
func requestKey(user, op, key string) string { return hash([]string{user, op, key}) }
func (s *Store) authorize(ctx context.Context, u User) error {
	if s.closed {
		return ErrInactive
	}
	if u.ID == "" {
		return ErrUnauthorized
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.options.Authorize != nil {
		if err := s.options.Authorize(ctx, u); err != nil {
			return err
		}
	}
	active, err := s.options.Active(ctx, u.ID)
	if err != nil {
		return err
	}
	if !active {
		return ErrUnauthorized
	}
	return nil
}
func (s *Store) volume(ctx context.Context) error {
	if s.options.CheckVolume != nil {
		return s.options.CheckVolume(ctx)
	}
	return nil
}

// CheckStorage lets the process supervisor withdraw its API when the opened
// volume identity changes; individual content operations also check it.
func (s *Store) CheckStorage(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrInactive
	}
	return s.volume(ctx)
}
func (s *Store) isActive(r *record) bool {
	return r.Share.Status == "published" && r.Share.ExpiresAt != nil && s.now().Before(*r.Share.ExpiresAt)
}
func recipient(r *record, id string) *Recipient {
	for i := range r.Share.Recipients {
		if r.Share.Recipients[i].UserID == id {
			return &r.Share.Recipients[i]
		}
	}
	return nil
}
func (s *Store) access(ctx context.Context, u User, id string) (*record, error) {
	if err := s.authorize(ctx, u); err != nil {
		return nil, err
	}
	r := s.records[id]
	if r == nil || (r.Share.Sender.ID != u.ID && recipient(r, u.ID) == nil) {
		return nil, ErrNotFound
	}
	if r.Share.Sender.ID != u.ID && (r.Share.Status == "preparing" || r.Share.Status == "failed") {
		return nil, ErrNotFound
	}
	active, err := s.options.Active(ctx, r.Share.Sender.ID)
	if err != nil {
		return nil, err
	}
	if !active && (r.Share.Status == "published" || r.Share.Status == "preparing") {
		s.revokeLocked(r, "Sender account is unavailable")
		if err = errors.Join(s.persist(r), s.fence(ctx, r.Share.ID)); err != nil {
			return nil, err
		}
	}
	return r, nil
}
func (s *Store) view(r *record, u User) Share {
	v := r.Share
	v.Entries = append([]Entry{}, r.Share.Entries...)
	v.Recipients = []Recipient{}
	for _, p := range r.Share.Recipients {
		if u.ID == r.Share.Sender.ID || p.UserID == u.ID {
			v.Recipients = append(v.Recipients, p)
		}
	}
	if v.Status == "published" && !s.isActive(r) {
		v.Status = "expired"
	}
	return v
}

func (s *Store) Create(ctx context.Context, u User, req CreateRequest) (Share, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.authorize(ctx, u); err != nil {
		return Share{}, err
	}
	if !validKey(req.RequestID) || !validText(req.Message, true) || len(req.SourceIDs) == 0 || len(req.SourceIDs) > 100 || len(req.RecipientIDs) == 0 || len(req.RecipientIDs) > 100 {
		return Share{}, ErrInvalid
	}
	key := requestKey(u.ID, "create", req.RequestID)
	digest := hash(req)
	for _, r := range s.records {
		if old, ok := r.Requests[key]; ok {
			if old.Hash != digest {
				return Share{}, ErrConflict
			}
			return s.view(r, u), nil
		}
	}
	if len(s.running) >= s.options.MaxTasks {
		return Share{}, ErrBusy
	}
	if err := s.volume(ctx); err != nil {
		return Share{}, err
	}
	seen := map[string]bool{}
	recipients := []Recipient{}
	for _, id := range req.RecipientIDs {
		if id == u.ID || id == "" || seen[id] {
			return Share{}, ErrInvalid
		}
		active, err := s.options.Active(ctx, id)
		if err != nil {
			return Share{}, err
		}
		if !active {
			return Share{}, ErrInvalid
		}
		seen[id] = true
		recipients = append(recipients, Recipient{UserID: id})
	}
	for _, id := range req.SourceIDs {
		if id == "" || len(id) > 4096 {
			return Share{}, ErrInvalid
		}
	}
	r := &record{Share: Share{ID: newID(), Sender: u, Status: "preparing", CreatedAt: s.now(), Entries: []Entry{}, Recipients: recipients}, Threads: map[string][]Message{}, Jobs: map[string]*storedJob{}, Requests: map[string]requestRecord{}, Saved: map[string]map[string]bool{}}
	r.Requests[key] = requestRecord{Hash: digest, ID: r.Share.ID}
	if err := s.persist(r); err != nil {
		return Share{}, err
	}
	s.records[r.Share.ID] = r
	taskCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.running[r.Share.ID] = running{shareID: r.Share.ID, cancel: cancel, ctx: taskCtx, user: u}
	s.wg.Add(1)
	go s.prepare(taskCtx, u, r.Share.ID, req)
	return s.view(r, u), nil
}
func validPath(p string) bool {
	if p == "" || len(p) > 4096 || !utf8.ValidString(p) || strings.ContainsAny(p, "\\\x00:") || strings.HasPrefix(p, "/") || path.Clean(p) != p {
		return false
	}
	for part := range strings.SplitSeq(p, "/") {
		if part == "." || part == ".." || part == "" || len(part) > 255 {
			return false
		}
	}
	return true
}
func (s *Store) prepare(ctx context.Context, u User, id string, req CreateRequest) {
	defer s.wg.Done()
	entries := []Entry{}
	names := map[string]bool{}
	var total int64
	err := s.volume(ctx)
	if err == nil {
		err = os.Mkdir(filepath.Join(s.dir, id), 0700)
	}
	if err == nil {
		err = s.options.Backend.Capture(ctx, u, req.SourceIDs, func(e Entry, reader io.Reader) error {
			s.mu.Lock()
			authErr := s.authorize(ctx, u)
			s.mu.Unlock()
			if authErr != nil {
				return authErr
			}
			if err := s.volume(ctx); err != nil {
				return err
			}
			if !validPath(e.Path) || e.Size < 0 || names[e.Path] || len(entries) >= MaxEntries {
				return ErrInvalid
			}
			names[e.Path] = true
			e.ID = newID()
			if !e.IsDir {
				if reader == nil {
					return ErrInvalid
				}
				file, err := os.OpenFile(filepath.Join(s.dir, id, e.ID), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if err != nil {
					return err
				}
				n, err := captureFile(ctx, file, reader, e.Size, s.options.RequireClone)
				if err == nil {
					err = file.Sync()
				}
				if err == nil {
					err = file.Chmod(0400)
				}
				closeErr := file.Close()
				if err == nil {
					err = closeErr
				}
				if err != nil {
					return err
				}
				if n != e.Size {
					return errors.New("source content changed during capture")
				}
				total += n
			} else {
				e.Size = 0
			}
			entries = append(entries, e)
			s.mu.Lock()
			r := s.records[id]
			r.Share.PreparedBytes = total
			s.mu.Unlock()
			return nil
		})
	}
	// Blob data and directory entries reach storage before their manifest.
	if err == nil {
		err = s.volume(ctx)
	}
	if err == nil {
		err = syncDirectory(filepath.Join(s.dir, id))
	}
	if err == nil {
		err = syncDirectory(s.dir)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.records[id]
	delete(s.running, id)
	if err == nil {
		err = s.authorize(ctx, u)
	}
	if r.Share.Status == "revoked" {
		err = ErrInactive
	}
	if err == nil && len(entries) == 0 {
		err = ErrInvalid
	}
	if err != nil {
		if r.Share.Status != "revoked" {
			r.Share.Status = "failed"
			r.Share.Error = "Unable to prepare share: " + safeError(err)
			if s.interruptReason != "" {
				r.Share.Error = s.interruptReason
			}
		}
	} else {
		now := s.now()
		expires := now.Add(7 * 24 * time.Hour)
		r.Share.Status = "published"
		r.Share.PublishedAt = &now
		r.Share.ExpiresAt = &expires
		r.Share.Entries = entries
		for _, p := range r.Share.Recipients {
			r.Threads[p.UserID] = []Message{}
			if req.Message != "" {
				m := Message{ID: 1, Sender: u, Text: req.Message, CreatedAt: now}
				r.Threads[p.UserID] = append(r.Threads[p.UserID], m)
				thread := recipient(r, p.UserID)
				thread.LastMessage = 1
				// The sender wrote the note, so it is never unread on their side.
				thread.SenderReadThrough = 1
			}
			s.event(r, p.UserID, p.UserID, "share", 0)
		}
	}
	if persistErr := s.persist(r); persistErr != nil {
		r.Share.Status = "failed"
		r.Share.Error = "Unable to persist prepared share"
		r.Events = nil
		_ = s.persist(r)
	}
	_ = s.cleanupLocked(ctx, r)
}
func safeError(err error) string {
	switch {
	case errors.Is(err, errCommitRecord):
		return "file was saved but its task state could not be recorded"
	case errors.Is(err, context.Canceled):
		return "operation stopped"
	case errors.Is(err, ErrUnauthorized):
		return "session or account unavailable"
	case errors.Is(err, ErrInactive):
		return "share withdrawn"
	case errors.Is(err, ErrInvalid):
		return "invalid or changed source content"
	case errors.Is(err, os.ErrPermission):
		return "permission denied"
	default:
		return "storage or file operation failed"
	}
}
func (s *Store) Get(ctx context.Context, u User, id string, markViewed bool) (Share, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.access(ctx, u, id)
	if err != nil {
		return Share{}, err
	}
	if markViewed && u.ID != r.Share.Sender.ID {
		p := recipient(r, u.ID)
		if p.ViewedAt == nil {
			now := s.now()
			p.ViewedAt = &now
			if err = s.persist(r); err != nil {
				p.ViewedAt = nil
				return Share{}, err
			}
		}
	}
	return s.view(r, u), nil
}
func (s *Store) List(ctx context.Context, u User, direction string) ([]Share, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.authorize(ctx, u); err != nil {
		return nil, err
	}
	if direction != "sent" && direction != "received" {
		return nil, ErrInvalid
	}
	out := []Share{}
	for id, r := range s.records {
		if (direction == "sent" && r.Share.Sender.ID == u.ID) || (direction == "received" && recipient(r, u.ID) != nil) {
			r, err := s.access(ctx, u, id)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			out = append(out, s.view(r, u))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}
func (s *Store) revokeLocked(r *record, reason string) {
	r.Share.Status = "revoked"
	r.Share.Error = reason
	for _, run := range s.running {
		if run.shareID == r.Share.ID {
			run.cancel()
		}
	}
	for _, reader := range s.streams {
		if reader.shareID == r.Share.ID {
			reader.stop()
		}
	}
}
func (s *Store) Revoke(ctx context.Context, u User, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.access(ctx, u, id)
	if err != nil {
		return err
	}
	if u.ID != r.Share.Sender.ID {
		return ErrNotFound
	}
	s.revokeLocked(r, "")
	if err = errors.Join(s.persist(r), s.fence(ctx, r.Share.ID)); err != nil {
		return err
	}
	return s.cleanupLocked(ctx, r)
}

// A remote publisher must acknowledge a per-share barrier before revocation
// can be reported as complete; a lost commit response must not race a later
// publication after this method returns. Local Backends share our mutex.
func (s *Store) fence(ctx context.Context, shareID string) error {
	if backend, ok := s.options.Backend.(interface {
		Fence(context.Context, string) error
	}); ok {
		bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return backend.Fence(bounded, shareID)
	}
	return nil
}
func (s *Store) cleanupLocked(ctx context.Context, r *record) error {
	if r.ContentRemoved || r.Share.Status == "preparing" || s.isActive(r) {
		return nil
	}
	for _, run := range s.running {
		if run.shareID == r.Share.ID {
			return nil
		}
	}
	if err := s.volume(ctx); err != nil {
		return err
	}
	if !validID(r.Share.ID) {
		return ErrInvalid
	}
	if err := os.RemoveAll(filepath.Join(s.dir, r.Share.ID)); err != nil {
		return err
	}
	r.ContentRemoved = true
	return s.persist(r)
}
func (s *Store) Maintain(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	var all error
	for _, r := range s.records {
		if r.dirty {
			all = errors.Join(all, s.persist(r))
		}
		active, err := s.options.Active(ctx, r.Share.Sender.ID)
		if err != nil {
			all = errors.Join(all, err)
			continue
		}
		if !active && (r.Share.Status == "published" || r.Share.Status == "preparing") {
			s.revokeLocked(r, "Sender account is unavailable")
			all = errors.Join(all, s.persist(r))
		}
		if r.Share.Status == "revoked" {
			if err = s.fence(ctx, r.Share.ID); err != nil {
				all = errors.Join(all, err)
				continue
			}
		}
		if !s.isActive(r) {
			for _, stream := range s.streams {
				if stream.shareID == r.Share.ID {
					stream.stop()
				}
			}
		}
		all = errors.Join(all, s.cleanupLocked(ctx, r))
	}
	for _, stream := range s.streams {
		if err := s.authorize(stream.ctx, stream.user); err != nil {
			stream.stop()
		}
	}
	for _, run := range s.running {
		if err := s.authorize(run.ctx, run.user); err != nil {
			run.cancel()
		}
	}
	return all
}
func (s *Store) event(r *record, to, thread, kind string, message int64) {
	s.nextEvent++
	r.Events = append(r.Events, event{To: to, Notification: Notification{ID: s.nextEvent, ShareID: r.Share.ID, RecipientID: thread, Kind: kind, MessageID: message, CreatedAt: s.now()}})
}

// String is intentionally free of content paths and session credentials.
func (s *Store) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprintf("shares.Store(%d shares)", len(s.records))
}
