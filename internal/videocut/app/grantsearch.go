package app

import (
	"context"
	"fmt"

	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
)

// WindowSearch finds, among a grant's videos, the windows most like each of
// texts: the photo library's searchWindows (#599), which its grants answer
// (photosapi.Grants).
type WindowSearch interface {
	SearchWindows(ctx context.Context, grantID string, texts []string, k int) ([][]photos.WindowMatch, error)
}

// GrantSearch is the search of Grants, which refuse as the photo library
// does, with those refusals as the montage center's: windows made with
// another model are ErrSearchModel, a library or local AI that cannot answer
// now is ErrSearchUnavailable, a text it does not search for is ErrInvalid,
// and the rest are as for any grant call (grantError). Open uses it when
// Options.Grants search windows.
type GrantSearch struct {
	Grants WindowSearch
}

func (g GrantSearch) SearchWindows(ctx context.Context, grantID string, texts []string, k int) ([][]photos.WindowMatch, error) {
	matches, err := g.Grants.SearchWindows(ctx, grantID, texts, k)
	if err == nil {
		return matches, nil
	}
	switch photosapi.GrantErrorCode(err) {
	case photosapi.GrantModelMismatch:
		return nil, ErrSearchModel
	case "", photosapi.GrantAIUnavailable, photosapi.GrantUnavailable, photosapi.GrantOwnerInactive:
		// The library did not answer, or cannot now: searching waits.
		return nil, fmt.Errorf("%w: %w", ErrSearchUnavailable, err)
	case photosapi.GrantInvalid:
		// The library does not search for such a text (blank, or longer
		// than its queries are).
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	// The rest are what any grant call answers: a grant revoked, a session
	// ended.
	return nil, grantError(err)
}
