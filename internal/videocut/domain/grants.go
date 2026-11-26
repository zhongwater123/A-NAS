package domain

import (
	"fmt"
	"regexp"

	"github.com/zhongwater123/A-NAS/internal/photosapi"
)

// GrantError turns what the photo library answered into the montage
// center's errors. No answer, an owner whose account is not in use now
// (owner_inactive) and the photo library's own failure are for later.
func GrantError(err error) error {
	switch photosapi.GrantErrorCode(err) {
	case photosapi.GrantAssetsRefused, photosapi.GrantAssetNotFound:
		return ErrAssetsRefused
	case photosapi.GrantTooMany:
		return ErrTooManyAssets
	case photosapi.GrantUnauthorized:
		return ErrSessionEnded
	case photosapi.GrantNotFound:
		return ErrGrantRevoked
	}
	return fmt.Errorf("%w: %v", ErrPhotosUnavailable, err)
}

// AccountName is what the planner accepts as an account's name: the marker
// is a file named by it.
var AccountName = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
