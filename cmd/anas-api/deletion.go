package main

import (
	"context"

	"github.com/zhongwater123/A-NAS/internal/accountremoval"
	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/media"
	"github.com/zhongwater123/A-NAS/internal/music"
	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photoservice"
	"github.com/zhongwater123/A-NAS/internal/transfers"
)

// accountDeletionSteps removes a deleted account from every part of A-NAS, in
// order (docs/specs/account-enable-and-delete.md). The photo library goes
// first, handed over or deleted; the files service then deletes the personal
// space's snapshots, which the Host Agent can only do while it still knows
// the space; the Host Agent removes the space and the account from the host;
// the other services forget the account last. The step names are stored with
// a deletion in progress, so they must not change.
func accountDeletionSteps(config Config, users *accounts.Service, photoService *photos.Service, fileService *files.Service,
	removeShares, removeFamily func(context.Context, string) error,
	musicService *music.Service, mediaService *media.Service, transferService *transfers.Service) []accounts.DeletionStep {
	photosStep := func(ctx context.Context, removal accounts.Removal) error {
		if config.Live() {
			return accountremoval.Request(ctx, config.PhotosSocket, removal.UserID)
		}
		if photoService == nil {
			return nil
		}
		// Development runs the photo library in this process and knows the
		// deletion itself.
		return photoservice.RemoveAccount(photoService)(ctx, accounts.AccountDeletion{
			UserID: removal.UserID, Username: removal.Username,
			PersonalSpace: string(removal.PersonalSpace), Library: string(removal.Library),
			AdminID: removal.Administrator.ID, AdminUsername: removal.Administrator.Username,
		})
	}
	return []accounts.DeletionStep{
		{Name: "photos", Remove: photosStep},
		{Name: "files", Remove: func(ctx context.Context, removal accounts.Removal) error {
			return fileService.RemoveAccount(ctx, removal.UserID, removal.PrivateSpaceID)
		}},
		users.HostRemoval(),
		{Name: "shares", Remove: func(ctx context.Context, removal accounts.Removal) error {
			return removeShares(ctx, removal.UserID)
		}},
		{Name: "family", Remove: func(ctx context.Context, removal accounts.Removal) error {
			return removeFamily(ctx, removal.UserID)
		}},
		{Name: "music", Remove: func(ctx context.Context, removal accounts.Removal) error {
			return musicService.RemoveAccount(ctx, removal.UserID, removal.PrivateSpaceID)
		}},
		{Name: "media", Remove: func(ctx context.Context, removal accounts.Removal) error {
			return mediaService.RemoveAccount(ctx, removal.UserID, removal.PrivateSpaceID)
		}},
		{Name: "transfers", Remove: func(ctx context.Context, removal accounts.Removal) error {
			return transferService.RemoveAccount(ctx, removal.UserID)
		}},
	}
}
