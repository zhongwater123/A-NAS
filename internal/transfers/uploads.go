package transfers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
)

// maxBatchBytes and maxRelativePathBytes bound what a browser may attach to
// an upload.
const (
	maxBatchBytes        = 64
	maxRelativePathBytes = 4096
)

// UploadRequest starts an upload of one file.
type UploadRequest struct {
	files.UploadRequest
	Batch        string
	RelativePath string
	LastModified int64
}

// BeginUpload checks and opens an upload. Nothing is recorded when it
// fails: the browser that asked shows why.
func (s *Service) BeginUpload(ctx context.Context, owner accounts.User, request UploadRequest) (Transfer, error) {
	if len(request.Batch) > maxBatchBytes || len(request.RelativePath) > maxRelativePathBytes || request.LastModified < 0 {
		return Transfer{}, ErrInvalid
	}
	s.mu.Lock()
	open := 0
	for _, live := range s.live {
		if live.owner.ID == owner.ID && live.transfer.Direction == Upload {
			open++
		}
	}
	s.mu.Unlock()
	if open >= maxLiveUploads {
		return Transfer{}, ErrTooManyUploads
	}
	pending, err := s.files.BeginUpload(ctx, owner, request.UploadRequest)
	if err != nil {
		return Transfer{}, err
	}
	location, err := s.location(ctx, owner, pending.SpaceID, pending.ParentID)
	if err != nil {
		_ = s.files.AbortUpload(ctx, owner, pending)
		return Transfer{}, err
	}
	now := s.now()
	t := Transfer{
		ID: s.newID(Upload), Direction: Upload, Kind: KindFile, Name: request.Name, Location: location,
		TotalBytes: request.Size, State: StateRunning, Batch: request.Batch, RelativePath: request.RelativePath,
		LastModified: request.LastModified, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.save(ctx, owner.ID, t); err != nil {
		_ = s.files.AbortUpload(ctx, owner, pending)
		return Transfer{}, err
	}
	s.track(owner, t, pending)
	t.decorate()
	return t, nil
}

// UploadStatus reports an upload for resuming it: how far a live one is,
// or that a finished one completed, so a browser that missed the final
// response learns the file was saved instead of sending it again. Any other
// upload is ErrNotFound and must start over.
func (s *Service) UploadStatus(ctx context.Context, owner accounts.User, id string) (Transfer, error) {
	if live := s.liveUpload(owner, id); live != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if live.upload == nil {
			return Transfer{}, ErrNotFound
		}
		t := live.snapshot(s.now())
		t.TransferredBytes = live.upload.Offset()
		return t, nil
	}
	t, err := s.store.get(ctx, owner.ID, id)
	if err != nil || t.Direction != Upload || t.State != StateCompleted {
		return Transfer{}, ErrNotFound
	}
	return t, nil
}

// WriteUpload writes a part of an upload starting at offset and returns the
// bytes written. A failure keeps what was written, so the browser can resume
// once, for example, space was freed.
func (s *Service) WriteUpload(ctx context.Context, owner accounts.User, id string, offset int64, part io.Reader) (int64, error) {
	live := s.liveUpload(owner, id)
	if live == nil {
		return 0, ErrNotFound
	}
	live.op.Lock()
	defer live.op.Unlock()
	s.mu.Lock()
	upload := live.upload
	s.mu.Unlock()
	if upload == nil || !s.begin(live) {
		return 0, ErrCanceled
	}
	counted := &countingReader{reader: part, counted: offset, report: func(bytes int64) { s.progress(live, bytes) }}
	written, err := s.files.WriteUpload(ctx, upload, offset, counted)
	s.progress(live, upload.Offset())
	if s.end(live) {
		s.abort(context.WithoutCancel(ctx), live)
		return written, ErrCanceled
	}
	if s.volumeRefused(live, err) {
		s.abort(context.WithoutCancel(ctx), live)
		s.settle(ctx, live, StateFailed, FailureOf(err))
		return written, ErrLost
	}
	if err != nil && (counted.failed != nil && errors.Is(err, counted.failed) || ctx.Err() != nil) {
		// The browser went away, perhaps with its page: show the upload
		// interrupted now, so picking the file again soon resumes it.
		s.mu.Lock()
		live.cut = true
		s.mu.Unlock()
		return written, fmt.Errorf("%w: %v", ErrInterrupted, err)
	}
	return written, err
}

// FinishUpload saves a complete upload in its folder. When that fails the
// bytes are discarded and the transfer fails with the reason.
func (s *Service) FinishUpload(ctx context.Context, owner accounts.User, id string) (Transfer, error) {
	live := s.liveUpload(owner, id)
	if live == nil {
		t, err := s.store.get(ctx, owner.ID, id)
		if err == nil && t.Direction == Upload && t.State == StateCompleted {
			t.decorate()
			return t, nil
		}
		return Transfer{}, ErrNotFound
	}
	live.op.Lock()
	defer live.op.Unlock()
	s.mu.Lock()
	canceled, upload := live.canceled, live.upload
	s.mu.Unlock()
	if canceled || upload == nil {
		return Transfer{}, ErrCanceled
	}
	committed, err := s.files.CommitUpload(ctx, owner, upload)
	if errors.Is(err, files.ErrVolumeUnavailable) && !s.volumeRefused(live, err) {
		// Every byte is here: keep them for when the volume is back. The
		// browser asks for the upload again then, and saving it is retried.
		return Transfer{}, err
	}
	if err != nil {
		s.abort(context.WithoutCancel(ctx), live)
		t := s.settle(ctx, live, StateFailed, FailureOf(err))
		t.decorate()
		return t, err
	}
	s.mu.Lock()
	live.upload = nil
	live.transfer.TransferredBytes = live.transfer.TotalBytes
	live.transfer.EntryID = committed.Entry.ID
	live.transfer.Replaced = committed.Replaced
	if committed.Entry.Name != live.transfer.Name {
		live.transfer.SavedName = committed.Entry.Name
	}
	s.mu.Unlock()
	t := s.settle(ctx, live, StateCompleted, nil)
	if s.changed != nil {
		s.changed(ctx, owner, committed.Entry.SpaceID)
	}
	t.decorate()
	return t, nil
}

// volumeRefused notes whether the data volume refused an upload's bytes and
// reports when it has done so for longer than volumeRetryWindow.
func (s *Service) volumeRefused(live *liveTransfer, err error) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !errors.Is(err, files.ErrVolumeUnavailable) {
		if err == nil {
			live.volumeSince = time.Time{}
		}
		return false
	}
	now := s.now()
	if live.volumeSince.IsZero() {
		live.volumeSince = now
	}
	return now.Sub(live.volumeSince) > volumeRetryWindow
}

func (s *Service) liveUpload(owner accounts.User, id string) *liveTransfer {
	s.mu.Lock()
	defer s.mu.Unlock()
	live, ok := s.live[id]
	if !ok || live.owner.ID != owner.ID || live.transfer.Direction != Upload || live.upload == nil {
		return nil
	}
	return live
}

// countingReader reports the position an upload has reached while a part
// is still being read, so progress moves within one large part.
type countingReader struct {
	reader  io.Reader
	counted int64
	report  func(int64)
	// failed is the read error that ended the part early, if any.
	failed error
}

func (c *countingReader) Read(buffer []byte) (int, error) {
	count, err := c.reader.Read(buffer)
	if count > 0 {
		c.counted += int64(count)
		c.report(c.counted)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		c.failed = err
	}
	return count, err
}
