// Package transfers runs 传输中心: every upload into and download out of a
// user's file spaces as a Transfer the user can follow, pause, resume and
// cancel from any browser.
//
// Uploads arrive in parts over the tus protocol (ADR 0019): each holds a
// files.PendingUpload whose temporary the File Broker created as the user,
// so a dropped connection resumes where it stopped instead of starting over.
// Downloads are counted as the Product Service sends them, so progress and
// completion are known without access to the browser's own download list.
// Transfers and their history live in transfers.db on the system disk; the
// bytes never do.
package transfers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
)

var (
	ErrNotFound = errors.New("transfer not found")
	// ErrState reports an action the transfer's state does not allow, such
	// as pausing a download or removing a running transfer.
	ErrState = errors.New("transfer state does not allow this")
	// ErrTooManyUploads keeps one user from holding more open uploads than
	// maxLiveUploads.
	ErrTooManyUploads = errors.New("too many unfinished uploads")
	ErrInvalid        = errors.New("transfer request is invalid")
	ErrCanceled       = errors.New("transfer was canceled")
	// ErrInterrupted reports a part whose connection dropped; what arrived is kept.
	ErrInterrupted = errors.New("the connection dropped during the transfer")
)

type Direction string

const (
	Upload   Direction = "upload"
	Download Direction = "download"
)

type Kind string

const (
	// KindFile moves one file.
	KindFile Kind = "file"
	// KindArchive downloads files and folders as one ZIP archive.
	KindArchive Kind = "archive"
)

type State string

const (
	// StatePending is a download that its browser has not requested yet.
	StatePending State = "pending"
	StateRunning State = "running"
	// StatePaused is an upload its browser paused; it resumes where it stopped.
	StatePaused State = "paused"
	// StateInterrupted lost its connection: an upload that received nothing
	// for interruptedAfter, or a download whose browser went away. Both can
	// resume where they stopped.
	StateInterrupted State = "interrupted"
	StateCompleted   State = "completed"
	StateFailed      State = "failed"
	StateCanceled    State = "canceled"
)

func (s State) finished() bool {
	return s == StateCompleted || s == StateFailed || s == StateCanceled
}

const (
	interruptedAfter = 30 * time.Second
	// abandonedAfter ends an upload that received nothing for a day and
	// removes its temporary.
	abandonedAfter = 24 * time.Hour
	unstartedAfter = 15 * time.Minute
	// partialAfter ends a download that a range request stopping before the
	// end left part-way.
	partialAfter   = time.Hour
	keepFinished   = 30 * 24 * time.Hour
	maxFinished    = 500
	maxLiveUploads = 64
	flushEvery     = 5 * time.Second
)

// Location is the folder a transfer reads from or writes to.
type Location struct {
	SpaceID   string             `json:"spaceId"`
	SpaceKind accounts.SpaceKind `json:"spaceKind"`
	// Folders lead from the space root to the folder; the root has none.
	Folders []files.Folder `json:"folders"`
}

// Failure says why a transfer failed; Code is stable for clients.
type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Transfer struct {
	ID               string    `json:"id"`
	Direction        Direction `json:"direction"`
	Kind             Kind      `json:"kind"`
	Name             string    `json:"name"`
	Location         Location  `json:"location"`
	TotalBytes       int64     `json:"totalBytes"`
	TransferredBytes int64     `json:"transferredBytes"`
	BytesPerSecond   int64     `json:"bytesPerSecond"`
	State            State     `json:"state"`
	Error            *Failure  `json:"error,omitempty"`
	// Batch groups the uploads one action started; the client chooses it.
	Batch string `json:"batch,omitempty"`
	// RelativePath and LastModified describe an upload's file as its browser
	// saw it (folders inside a folder upload, modification time in
	// milliseconds), so a reloaded page can find the same file again.
	RelativePath string `json:"relativePath,omitempty"`
	LastModified int64  `json:"lastModified,omitempty"`
	// EntryID is the saved file of a completed upload.
	EntryID string `json:"entryId,omitempty"`
	// SavedName is set when an upload was saved under another name because
	// the requested one was taken.
	SavedName string `json:"savedName,omitempty"`
	// Replaced is set when an upload sent the file it replaced to the trash.
	Replaced bool `json:"replaced,omitempty"`
	// Skipped counts files an archive could not read and left out.
	Skipped    int        `json:"skipped,omitempty"`
	ContentURL string     `json:"contentUrl,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`

	// entries are the files and folders a download reads.
	entries []string
}

// Spaces tells which spaces a user sees, to describe locations.
type Spaces interface {
	ListSpaces(context.Context, accounts.User) ([]accounts.Space, error)
}

type Options struct {
	Now    func() time.Time
	Random io.Reader
	// Sessions lists the signed-in sessions. Ending an abandoned upload
	// removes its temporary as its owner through one of them.
	Sessions func(context.Context) []accounts.Session
	// FilesChanged, when set, is told after an upload was saved in a space,
	// so indexes over ordinary files (the media center) catch up.
	FilesChanged func(ctx context.Context, user accounts.User, spaceID string)
	Logger       *slog.Logger
}

type Service struct {
	store    *store
	files    *files.Service
	spaces   Spaces
	sessions func(context.Context) []accounts.Session
	changed  func(context.Context, accounts.User, string)
	now      func() time.Time
	random   io.Reader
	logger   *slog.Logger

	mu   sync.Mutex
	live map[string]*liveTransfer
}

// liveTransfer is a transfer this process is moving bytes for. Service.mu
// guards its fields; op serialises the operations of an upload, so upload
// changes only while both are held.
type liveTransfer struct {
	op       sync.Mutex
	owner    accounts.User
	transfer Transfer
	upload   *files.PendingUpload
	meter    meter
	// active counts requests moving bytes right now.
	active       int
	lastActivity time.Time
	dirty        bool
	canceled     bool
	// cancel stops the request streaming a download.
	cancel context.CancelFunc
}

// Open opens transfers.db and settles transfers a restart interrupted.
func Open(path string, fileService *files.Service, spaces Spaces, options Options) (*Service, error) {
	if fileService == nil || spaces == nil {
		return nil, errors.New("transfers need the file service and spaces")
	}
	db, err := openStore(path)
	if err != nil {
		return nil, err
	}
	service := &Service{
		store: db, files: fileService, spaces: spaces, sessions: options.Sessions, changed: options.FilesChanged,
		now: options.Now, random: options.Random, logger: options.Logger, live: make(map[string]*liveTransfer),
	}
	if service.now == nil {
		service.now = time.Now
	}
	if service.random == nil {
		service.random = rand.Reader
	}
	if service.logger == nil {
		service.logger = slog.Default()
	}
	if service.sessions == nil {
		service.sessions = func(context.Context) []accounts.Session { return nil }
	}
	ctx := context.Background()
	if err := db.recover(ctx, service.now()); err != nil {
		_ = db.close()
		return nil, fmt.Errorf("recover transfers: %w", err)
	}
	if err := db.prune(ctx, service.now().Add(-keepFinished), maxFinished); err != nil {
		_ = db.close()
		return nil, err
	}
	return service, nil
}

// Close stops every transfer still moving bytes and closes the database.
// Upload temporaries stay; reconciliation removes them once nothing holds
// them.
func (s *Service) Close() error {
	s.mu.Lock()
	for id, live := range s.live {
		if live.cancel != nil {
			live.cancel()
		}
		delete(s.live, id)
	}
	s.mu.Unlock()
	return s.store.close()
}

// Run saves progress, ends abandoned uploads and unstarted downloads, and
// prunes history until ctx ends.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(flushEvery)
	defer ticker.Stop()
	lastPrune := s.now()
	for {
		select {
		case <-ctx.Done():
			s.flush(context.WithoutCancel(ctx))
			return
		case <-ticker.C:
		}
		s.flush(ctx)
		s.endAbandoned(ctx)
		now := s.now()
		if err := s.store.failStaleDownloads(ctx, now.Add(-unstartedAfter), now.Add(-partialAfter), now); err != nil {
			s.logger.ErrorContext(ctx, "fail stale downloads", "error", err)
		}
		if now.Sub(lastPrune) > time.Hour {
			lastPrune = now
			if err := s.store.prune(ctx, now.Add(-keepFinished), maxFinished); err != nil {
				s.logger.ErrorContext(ctx, "prune transfers", "error", err)
			}
		}
	}
}

// List returns the owner's transfers newest first: everything unfinished
// and the latest finished ones.
func (s *Service) List(ctx context.Context, owner accounts.User) ([]Transfer, error) {
	saved, err := s.store.list(ctx, owner.ID, maxFinished)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	now := s.now()
	for index, t := range saved {
		if live, ok := s.live[t.ID]; ok && live.owner.ID == owner.ID {
			saved[index] = live.snapshot(now)
		}
	}
	s.mu.Unlock()
	sort.SliceStable(saved, func(i, j int) bool { return saved[i].CreatedAt.After(saved[j].CreatedAt) })
	for index := range saved {
		saved[index].decorate()
	}
	return saved, nil
}

// Get returns one of the owner's transfers.
func (s *Service) Get(ctx context.Context, owner accounts.User, id string) (Transfer, error) {
	s.mu.Lock()
	if live, ok := s.live[id]; ok && live.owner.ID == owner.ID {
		t := live.snapshot(s.now())
		s.mu.Unlock()
		t.decorate()
		return t, nil
	}
	s.mu.Unlock()
	t, err := s.store.get(ctx, owner.ID, id)
	if err != nil {
		return Transfer{}, err
	}
	t.decorate()
	return t, nil
}

// Pause records that the owner's browser paused an upload, so other
// browsers show it paused rather than interrupted.
func (s *Service) Pause(ctx context.Context, owner accounts.User, id string) (Transfer, error) {
	s.mu.Lock()
	live, ok := s.live[id]
	if !ok || live.owner.ID != owner.ID {
		s.mu.Unlock()
		if _, err := s.store.get(ctx, owner.ID, id); err != nil {
			return Transfer{}, err
		}
		return Transfer{}, ErrState
	}
	if live.transfer.Direction != Upload {
		s.mu.Unlock()
		return Transfer{}, ErrState
	}
	live.transfer.State = StatePaused
	live.transfer.UpdatedAt = s.now()
	t := live.snapshot(s.now())
	s.mu.Unlock()
	if err := s.store.save(ctx, owner.ID, t); err != nil {
		return Transfer{}, err
	}
	t.decorate()
	return t, nil
}

// Cancel stops a transfer. A canceled upload's bytes are discarded; a
// canceled download's connection is cut, so the browser reports it failed.
func (s *Service) Cancel(ctx context.Context, owner accounts.User, id string) (Transfer, error) {
	s.mu.Lock()
	live, ok := s.live[id]
	if ok && live.owner.ID == owner.ID {
		live.canceled = true
		delete(s.live, id)
		if live.cancel != nil {
			live.cancel()
		}
		t := live.snapshot(s.now())
		s.mu.Unlock()
		t = s.finish(t, StateCanceled, nil)
		if err := s.store.save(ctx, owner.ID, t); err != nil {
			return Transfer{}, err
		}
		// A write in progress discards the upload when it returns.
		if live.op.TryLock() {
			s.abort(ctx, live)
			live.op.Unlock()
		}
		t.decorate()
		return t, nil
	}
	s.mu.Unlock()
	t, err := s.store.get(ctx, owner.ID, id)
	if err != nil {
		return Transfer{}, err
	}
	if t.State.finished() {
		t.decorate()
		return t, nil
	}
	// A download not being sent, or an upload whose process restarted.
	t = s.finish(t, StateCanceled, nil)
	if err := s.store.save(ctx, owner.ID, t); err != nil {
		return Transfer{}, err
	}
	t.decorate()
	return t, nil
}

// Remove deletes a finished transfer from the history.
func (s *Service) Remove(ctx context.Context, owner accounts.User, id string) error {
	t, err := s.store.get(ctx, owner.ID, id)
	if err != nil {
		return err
	}
	if !t.State.finished() {
		return ErrState
	}
	return s.store.delete(ctx, owner.ID, id)
}

// ClearFinished deletes the owner's finished transfers from the history.
func (s *Service) ClearFinished(ctx context.Context, owner accounts.User) error {
	return s.store.clearFinished(ctx, owner.ID)
}

// track starts following an upload's bytes in this process.
func (s *Service) track(owner accounts.User, t Transfer, upload *files.PendingUpload) *liveTransfer {
	live := &liveTransfer{owner: owner, transfer: t, upload: upload, lastActivity: s.now()}
	live.meter.reset(s.now(), t.TransferredBytes)
	s.mu.Lock()
	s.live[t.ID] = live
	s.mu.Unlock()
	return live
}

// trackIdle follows a download unless another request already sends it; one
// download goes over one connection at a time.
func (s *Service) trackIdle(owner accounts.User, t Transfer, cancel context.CancelFunc) (*liveTransfer, bool) {
	live := &liveTransfer{owner: owner, transfer: t, lastActivity: s.now(), cancel: cancel}
	live.meter.reset(s.now(), t.TransferredBytes)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, busy := s.live[t.ID]; busy {
		return nil, false
	}
	s.live[t.ID] = live
	return live, true
}

// progress records bytes moved by a live transfer.
func (s *Service) progress(live *liveTransfer, transferred int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	live.transfer.TransferredBytes = transferred
	live.lastActivity = now
	live.dirty = true
	live.meter.observe(now, transferred)
}

// begin and end bracket a request that moves bytes.
func (s *Service) begin(live *liveTransfer) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if live.canceled {
		return false
	}
	live.active++
	live.lastActivity = s.now()
	if live.transfer.State != StateRunning {
		live.transfer.State = StateRunning
		live.dirty = true
	}
	return true
}

func (s *Service) end(live *liveTransfer) (canceled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	live.active--
	live.lastActivity = s.now()
	return live.canceled
}

// settle stops following a live transfer and saves its final state.
func (s *Service) settle(ctx context.Context, live *liveTransfer, state State, failure *Failure) Transfer {
	s.mu.Lock()
	if current, ok := s.live[live.transfer.ID]; ok && current == live {
		delete(s.live, live.transfer.ID)
	}
	t := live.snapshot(s.now())
	s.mu.Unlock()
	t = s.finish(t, state, failure)
	if err := s.store.save(context.WithoutCancel(ctx), live.owner.ID, t); err != nil {
		s.logger.ErrorContext(ctx, "save transfer", "transfer_id", t.ID, "error", err)
	}
	return t
}

func (s *Service) finish(t Transfer, state State, failure *Failure) Transfer {
	now := s.now()
	t.State, t.Error, t.UpdatedAt, t.BytesPerSecond = state, failure, now, 0
	if state.finished() {
		t.FinishedAt = &now
	}
	return t
}

// flush saves the progress of live transfers.
func (s *Service) flush(ctx context.Context) {
	s.mu.Lock()
	now := s.now()
	type pending struct {
		owner string
		t     Transfer
	}
	var dirty []pending
	for _, live := range s.live {
		if live.dirty {
			live.dirty = false
			live.transfer.UpdatedAt = now
			dirty = append(dirty, pending{live.owner.ID, live.snapshot(now)})
		}
	}
	s.mu.Unlock()
	for _, item := range dirty {
		if err := s.store.save(ctx, item.owner, item.t); err != nil {
			s.logger.ErrorContext(ctx, "save transfer progress", "transfer_id", item.t.ID, "error", err)
		}
	}
}

// endAbandoned ends uploads nothing was written to for abandonedAfter.
func (s *Service) endAbandoned(ctx context.Context) {
	s.mu.Lock()
	cutoff := s.now().Add(-abandonedAfter)
	var abandoned []*liveTransfer
	for _, live := range s.live {
		if live.upload != nil && live.active == 0 && live.lastActivity.Before(cutoff) {
			abandoned = append(abandoned, live)
		}
	}
	s.mu.Unlock()
	for _, live := range abandoned {
		if !live.op.TryLock() {
			continue
		}
		s.mu.Lock()
		live.canceled = true
		s.mu.Unlock()
		userCtx := ctx
		for _, session := range s.sessions(ctx) {
			if session.User.ID == live.owner.ID {
				userCtx = accounts.WithSessionToken(ctx, session.Token)
				break
			}
		}
		s.abort(userCtx, live)
		live.op.Unlock()
		s.settle(ctx, live, StateFailed, &Failure{Code: "expired", Message: "the upload received nothing for a day"})
	}
}

// abort discards a live upload's bytes; the caller holds live.op.
func (s *Service) abort(ctx context.Context, live *liveTransfer) {
	s.mu.Lock()
	upload := live.upload
	live.upload = nil
	s.mu.Unlock()
	if upload == nil {
		return
	}
	if err := s.files.AbortUpload(ctx, live.owner, upload); err != nil {
		s.logger.WarnContext(ctx, "upload temporary left for reconciliation", "transfer_id", live.transfer.ID, "error", err)
	}
}

// location describes a folder of a space for the owner.
func (s *Service) location(ctx context.Context, owner accounts.User, spaceID, folderID string) (Location, error) {
	spaces, err := s.spaces.ListSpaces(ctx, owner)
	if err != nil {
		return Location{}, err
	}
	location := Location{SpaceID: spaceID, Folders: []files.Folder{}}
	for _, space := range spaces {
		if space.ID == spaceID {
			location.SpaceKind = space.Kind
		}
	}
	if location.SpaceKind == "" {
		return Location{}, files.ErrForbidden
	}
	if folderID != "" {
		folders, err := s.files.Folders(ctx, owner, spaceID, folderID)
		if err != nil {
			return Location{}, err
		}
		location.Folders = folders
	}
	return location, nil
}

func (s *Service) newID(direction Direction) string {
	value := make([]byte, 16)
	if _, err := io.ReadFull(s.random, value); err != nil {
		panic(err)
	}
	return string(direction) + "-" + hex.EncodeToString(value)
}

// snapshot copies the transfer with its live state; the caller holds Service.mu.
func (l *liveTransfer) snapshot(now time.Time) Transfer {
	t := l.transfer
	t.BytesPerSecond = l.meter.rate(now)
	if t.Direction == Upload && t.State == StateRunning && l.active == 0 && now.Sub(l.lastActivity) > interruptedAfter {
		t.State = StateInterrupted
		t.BytesPerSecond = 0
	}
	return t
}

// decorate adds what clients derive from the stored fields.
func (t *Transfer) decorate() {
	if t.Direction == Download {
		t.ContentURL = "/api/v1/transfers/" + t.ID + "/content"
	}
	if t.Location.Folders == nil {
		t.Location.Folders = []files.Folder{}
	}
}

// FailureOf describes an error for a client without revealing paths or
// other internals.
func FailureOf(err error) *Failure {
	for _, known := range failures {
		for _, target := range known.errors {
			if errors.Is(err, target) {
				return &Failure{Code: known.code, Message: known.message}
			}
		}
	}
	return &Failure{Code: "internal_error", Message: "the transfer could not be completed"}
}

var failures = []struct {
	code, message string
	errors        []error
}{
	{"forbidden", "file space access is forbidden", []error{files.ErrForbidden}},
	{"not_found", "the file or folder was not found", []error{files.ErrNotFound, ErrNotFound}},
	{"name_conflict", "a file with this name already exists", []error{files.ErrConflict}},
	{"folder_blocked", "a file has the name of a folder to create", []error{files.ErrFolderBlocked}},
	{"name_too_long", "names may hold at most 255 bytes", []error{files.ErrNameTooLong, syscall.ENAMETOOLONG}},
	{"name_not_portable", "the name cannot be used on Windows or macOS", []error{files.ErrNameNotPortable}},
	{"validation_failed", "the request is invalid", []error{files.ErrInvalidName, files.ErrInvalidUpload, files.ErrInvalidSelection, files.ErrUnsupportedType, ErrInvalid}},
	{"volume_unavailable", "the data volume is unavailable", []error{files.ErrVolumeUnavailable}},
	{"insufficient_storage", "the data volume reserve would be exhausted", []error{files.ErrInsufficientSpace}},
	{"too_many_uploads", "too many unfinished uploads", []error{ErrTooManyUploads}},
	{"transfer_state", "the transfer state does not allow this", []error{ErrState, files.ErrUploadOffset, files.ErrUploadIncomplete}},
	{"canceled", "the transfer was canceled", []error{ErrCanceled}},
	{"interrupted", "the connection dropped during the transfer", []error{ErrInterrupted}},
}

// meter estimates a transfer's speed from its progress.
type meter struct {
	at    time.Time
	bytes int64
	value float64
}

func (m *meter) reset(now time.Time, bytes int64) { *m = meter{at: now, bytes: bytes} }

func (m *meter) observe(now time.Time, bytes int64) {
	elapsed := now.Sub(m.at).Seconds()
	if elapsed < 0.5 {
		return
	}
	instant := float64(bytes-m.bytes) / elapsed
	if m.value == 0 {
		m.value = instant
	} else {
		m.value = 0.6*m.value + 0.4*instant
	}
	m.at, m.bytes = now, bytes
}

// rate is the speed, falling to zero when bytes stop moving.
func (m *meter) rate(now time.Time) int64 {
	if now.Sub(m.at) > 3*time.Second || m.value < 0 {
		return 0
	}
	return int64(m.value)
}
