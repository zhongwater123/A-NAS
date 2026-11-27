package external

import (
	"context"
	"fmt"

	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
	"github.com/zhongwater123/A-NAS/internal/videocut/domain"
)

// GrantSearch is the search of Grants, which refuse as the photo library
// does, with those refusals as VideoCut's: windows made with
// another model are ErrSearchModel, a library or local AI that cannot answer
// now is ErrSearchUnavailable, a text it does not search for is ErrInvalid,
// and the rest are as for any grant call (domain.GrantError). videocut.go
// gives it to app when the grants search windows (videocut.WindowSearchOf).
type GrantSearch struct {
	Grants PhotoSearch
}

// PhotoSearch is the photo library's own search of a grant's windows by
// text; photosapi.Grants has it.
type PhotoSearch interface {
	SearchWindows(ctx context.Context, grantID string, texts []string, k int) ([][]photos.WindowMatch, error)
}

// SearchWindows asks the photo library and says its refusals as the
// VideoCut's errors.

func (g GrantSearch) SearchWindows(ctx context.Context, grantID string, texts []string, k int) ([][]photos.WindowMatch, error) {
	matches, err := g.Grants.SearchWindows(ctx, grantID, texts, k)
	if err == nil {
		return matches, nil
	}
	switch photosapi.GrantErrorCode(err) {
	case photosapi.GrantModelMismatch:
		return nil, domain.ErrSearchModel
	case "", photosapi.GrantAIUnavailable, photosapi.GrantUnavailable, photosapi.GrantOwnerInactive:
		// The library did not answer, or cannot now: searching waits.
		return nil, fmt.Errorf("%w: %w", domain.ErrSearchUnavailable, err)
	case photosapi.GrantInvalid:
		// The library does not search for such a text (blank, or longer
		// than its queries are).
		return nil, fmt.Errorf("%w: %v", domain.ErrInvalid, err)
	}
	// The rest are what any grant call answers: a grant revoked, a session
	// ended.
	return nil, domain.GrantError(err)
}
