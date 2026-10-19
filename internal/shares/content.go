package shares

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type contentReader struct {
	file        *os.File
	store       *Store
	ctx         context.Context
	user        User
	id, shareID string
	once        sync.Once
	timer       *time.Timer
	done        chan struct{}
}

func (r *contentReader) stop() {
	r.once.Do(func() {
		close(r.done)
		_ = r.file.Close()
		if r.timer != nil {
			r.timer.Stop()
		}
	})
}
func (r *contentReader) Done() <-chan struct{} { return r.done }
func (r *contentReader) Close() error {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	r.stop()
	delete(r.store.streams, r.id)
	return nil
}
func (r *contentReader) Read(p []byte) (int, error) {
	r.store.mu.Lock()
	share, err := r.store.access(r.ctx, r.user, r.shareID)
	if err == nil && !r.store.isActive(share) {
		err = ErrInactive
	}
	r.store.mu.Unlock()
	if err != nil {
		r.stop()
		return 0, err
	}
	return r.file.Read(p)
}

// Seek rechecks authorization too: conditional and range responses must never
// turn a previously opened descriptor into a capability beyond expiry.
func (r *contentReader) Seek(offset int64, whence int) (int64, error) {
	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	share, err := r.store.access(r.ctx, r.user, r.shareID)
	if err != nil {
		return 0, err
	}
	if !r.store.isActive(share) {
		return 0, ErrInactive
	}
	return r.file.Seek(offset, whence)
}
func (s *Store) OpenContent(ctx context.Context, u User, shareID, entryID string) (io.ReadCloser, Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.access(ctx, u, shareID)
	if err != nil {
		return nil, Entry{}, err
	}
	if !s.isActive(r) {
		return nil, Entry{}, ErrInactive
	}
	if len(s.streams) >= 32 {
		return nil, Entry{}, ErrBusy
	}
	if err = s.volume(ctx); err != nil {
		return nil, Entry{}, err
	}
	for _, entry := range r.Share.Entries {
		if entry.ID != entryID || entry.IsDir {
			continue
		}
		file, err := os.Open(filepath.Join(s.dir, shareID, entryID))
		if err != nil {
			return nil, Entry{}, err
		}
		reader := &contentReader{file: file, store: s, ctx: ctx, user: u, id: newID(), shareID: shareID, done: make(chan struct{})}
		reader.timer = time.NewTimer(r.Share.ExpiresAt.Sub(s.now()))
		s.streams[reader.id] = reader
		go func() {
			select {
			case <-reader.timer.C:
				_ = reader.Close()
			case <-ctx.Done():
				_ = reader.Close()
			case <-reader.done:
			}
		}()
		return reader, entry, nil
	}
	return nil, Entry{}, ErrNotFound
}
