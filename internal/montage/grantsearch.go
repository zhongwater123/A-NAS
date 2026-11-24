package montage

import (
	"context"
	"fmt"

	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
)

// WindowGrants are grants that search their videos' windows by text: the
// photo library's searchWindows operation (photosapi.Grants).
type WindowGrants interface {
	SearchWindows(ctx context.Context, grantID string, texts []string, k int) ([][]photos.WindowMatch, error)
}

// GrantSearch is the WindowSearch of grants that search windows, with the
// photo library's refusals as the montage center's: windows made with
// another model are ErrSearchModel, a library or local AI that cannot answer
// now is ErrSearchUnavailable, a text it does not search for is ErrInvalid,
// and the rest are as for any grant call (grantError). Open uses it when Options.Grants search
// windows and Options.Search is not set.
type GrantSearch struct {
	Grants WindowGrants
}

func (g GrantSearch) SearchWindows(ctx context.Context, grantID string, texts []string, k int) ([][]WindowHit, error) {
	matches, err := g.Grants.SearchWindows(ctx, grantID, texts, k)
	if err != nil {
		switch photosapi.GrantErrorCode(err) {
		case photosapi.GrantModelMismatch:
			return nil, ErrSearchModel
		case "", photosapi.GrantAIUnavailable, photosapi.GrantUnavailable, photosapi.GrantOwnerInactive:
			// The library did not answer, or cannot now: searching waits.
			return nil, fmt.Errorf("%w: %w", ErrSearchUnavailable, err)
		case photosapi.GrantInvalid:
			// The library does not search for such a text (blank, or
			// longer than its queries are).
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		// The rest are what any grant call answers: a grant revoked, a
		// session ended.
		return nil, grantError(err)
	}
	hits := make([][]WindowHit, len(matches))
	for i, list := range matches {
		hits[i] = make([]WindowHit, len(list))
		for n, match := range list {
			hits[i][n] = WindowHit{AssetID: match.AssetID, Window: match.Window, Score: match.Score}
		}
	}
	return hits, nil
}
