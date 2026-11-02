package photos

import (
	"context"
	"errors"
	"strings"
)

// MaxLookup bounds the IDs one Lookup answers about.
const MaxLookup = 500

// ErrTooManyIDs means a Lookup asked about more than MaxLookup photos.
var ErrTooManyIDs = errors.New("too many photo IDs in one lookup")

// LookedUp is a photo that a Lookup found, with whether it is in the shared
// library.
type LookedUp struct {
	Asset  Asset `json:"asset"`
	Shared bool  `json:"shared"`
}

// Lookup returns those of ids that p sees outside the trash, with the fields
// a list shows, in no particular order. The family center asks it with the
// reader's own session to learn which photos of a channel that reader can
// see (ADR 0029): the answer comes from the same Policy as every other read,
// so unknown, trashed and hidden photos are simply missing.
func (s *Service) Lookup(ctx context.Context, p Principal, ids []string) ([]LookedUp, error) {
	if !p.valid() {
		return nil, ErrForbidden
	}
	if len(ids) > MaxLookup {
		return nil, ErrTooManyIDs
	}
	seen := map[string]bool{}
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			args = append(args, id)
		}
	}
	found := []LookedUp{}
	if len(args) == 0 {
		return found, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(args)), ", ")
	records, err := s.queryAssets(ctx, s.db, "WHERE "+listedAssets(p, "a")+" AND a.id IN ("+marks+")", args...)
	if err != nil {
		return nil, err
	}
	now := s.now()
	for _, record := range records {
		if !canView(p, record.library, now) {
			continue
		}
		found = append(found, LookedUp{Asset: record.Asset, Shared: record.library.kind == LibraryKindShared})
	}
	return found, nil
}
