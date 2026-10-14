package photos

import (
	"context"
	"sync"
	"time"
)

// An upload key lets a browser send a photo again when it never got the
// answer to the first try, after a dropped or stalled connection, without
// making a second copy. Keys live in memory for a day: a browser retries
// within minutes, and only a restart between an upload and its retry would
// need them on disk.
const keepUploadKeys = 24 * time.Hour

// MaxUploadKeyBytes bounds an upload key.
const MaxUploadKeyBytes = 64

type keyedImport struct {
	// done closes when the import that took the key ends; assetID is then
	// the photo it made, or empty when it failed.
	done    chan struct{}
	assetID string
	at      time.Time
}

type uploadKeys struct {
	mu    sync.Mutex
	byKey map[string]*keyedImport
}

// importOnce runs import for the first request with a key and answers later
// requests with the photo it made, waiting while it still runs. A key whose
// import failed, or whose photo is gone, is free to try again.
func (s *Service) importOnce(ctx context.Context, p Principal, request ImportRequest, run func() (Asset, error)) (Asset, error) {
	key := p.UserID + "\x00" + request.LibraryID + "\x00" + request.UploadKey
	for {
		s.keys.mu.Lock()
		now := s.now()
		if s.keys.byKey == nil {
			s.keys.byKey = map[string]*keyedImport{}
		}
		for old, entry := range s.keys.byKey {
			if entry.assetID != "" && now.Sub(entry.at) > keepUploadKeys {
				delete(s.keys.byKey, old)
			}
		}
		entry, taken := s.keys.byKey[key]
		if !taken {
			entry = &keyedImport{done: make(chan struct{})}
			s.keys.byKey[key] = entry
			s.keys.mu.Unlock()
			asset, err := run()
			s.keys.mu.Lock()
			if err != nil {
				delete(s.keys.byKey, key)
			} else {
				entry.assetID, entry.at = asset.ID, s.now()
			}
			close(entry.done)
			s.keys.mu.Unlock()
			return asset, err
		}
		s.keys.mu.Unlock()
		select {
		case <-entry.done:
		case <-ctx.Done():
			return Asset{}, ctx.Err()
		}
		if entry.assetID == "" {
			continue
		}
		if asset, err := s.Get(ctx, p, entry.assetID); err == nil && asset.Trash == nil {
			return asset, nil
		}
		s.keys.mu.Lock()
		if s.keys.byKey[key] == entry {
			delete(s.keys.byKey, key)
		}
		s.keys.mu.Unlock()
	}
}
