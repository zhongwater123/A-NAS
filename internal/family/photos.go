package family

import (
	"context"
	"time"
)

// Photos is the photo service as seen by one caller. Every answer is
// authorized by the photo service's own Policy for that caller (ADR 0029):
// the family center never decides who may see a photo.
type Photos interface {
	// Lookup returns those of ids that actor can see outside the trash,
	// keyed by asset ID. Unknown, trashed and hidden assets are absent.
	Lookup(ctx context.Context, actor Actor, ids []string) (map[string]Photo, error)
}

// Photo is what a channel needs to know about a photo or video.
type Photo struct {
	ID      string
	TakenAt *time.Time
	Video   bool
	// Shared reports whether the asset is in the shared library; channels
	// hold only shared photos.
	Shared bool
}

// lookupBatch bounds the IDs sent in one request to the photo service.
const lookupBatch = 500

// lookup asks Photos about ids in batches. Without a photo service, no
// photo is visible.
func (s *Store) lookup(ctx context.Context, actor Actor, ids []string) (map[string]Photo, error) {
	found := map[string]Photo{}
	if len(ids) == 0 {
		return found, nil
	}
	if s.photos == nil {
		return nil, ErrPhotosUnavailable
	}
	for start := 0; start < len(ids); start += lookupBatch {
		batch, err := s.photos.Lookup(ctx, actor, ids[start:min(start+lookupBatch, len(ids))])
		if err != nil {
			return nil, ErrPhotosUnavailable
		}
		for id, photo := range batch {
			found[id] = photo
		}
	}
	return found, nil
}
