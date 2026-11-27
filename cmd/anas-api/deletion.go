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
// a deletion in progress: a deletion under way across an update skips the
// steps stored as done, so it runs a renamed step a second time. Only a step
// that can run twice may be renamed; the videocut step, once named montage,
// can: run again, it only forgets the member's projects once more.
func accountDeletionSteps(config Config, users *accounts.Service, photoService *photos.Service, fileService *files.Service,
	removeShares, removeFamily, removeVideoCut, removeWorkspace func(context.Context, string) error,
	musicService *music.Service, mediaService *media.Service, transferService *transfers.Service) []accounts.DeletionStep {
	// The photo service names the album it handed the photos over in.
	photosStep := func(ctx context.Context, removal accounts.Removal) (accounts.Handover, error) {
		var result accountremoval.Result
		var err error
		switch {
		case config.Live():
			result, err = accountremoval.Request(ctx, config.PhotosSocket, removal.UserID)
		case photoService != nil:
			// Development runs the photo library in this process and knows
			// the deletion itself.
			result, err = photoservice.RemoveAccount(photoService)(ctx, accounts.AccountDeletion{
				UserID: removal.UserID, Username: removal.Username,
				PersonalSpace: string(removal.PersonalSpace), Library: string(removal.Library),
				AdminID: removal.Administrator.ID, AdminUsername: removal.Administrator.Username,
			})
		}
		return accounts.Handover{Album: result.Album}, err
	}
	return []accounts.DeletionStep{
		{Name: "photos", Remove: photosStep},
		accounts.ForgetStep("files", func(ctx context.Context, removal accounts.Removal) error {
			return fileService.RemoveAccount(ctx, removal.UserID, removal.PrivateSpaceID)
		}),
		users.HostRemoval(),
		accounts.ForgetStep("shares", func(ctx context.Context, removal accounts.Removal) error {
			return removeShares(ctx, removal.UserID)
		}),
		accounts.ForgetStep("family", func(ctx context.Context, removal accounts.Removal) error {
			return removeFamily(ctx, removal.UserID)
		}),
		// VideoCut forgets the account's projects; its videos and
		// films went with the photos step (ADR 0038).
		accounts.ForgetStep("videocut", func(ctx context.Context, removal accounts.Removal) error {
			return removeVideoCut(ctx, removal.UserID)
		}),
		accounts.ForgetStep("music", func(ctx context.Context, removal accounts.Removal) error {
			return musicService.RemoveAccount(ctx, removal.UserID, removal.PrivateSpaceID)
		}),
		accounts.ForgetStep("media", func(ctx context.Context, removal accounts.Removal) error {
			return mediaService.RemoveAccount(ctx, removal.UserID, removal.PrivateSpaceID)
		}),
		accounts.ForgetStep("transfers", func(ctx context.Context, removal accounts.Removal) error {
			return transferService.RemoveAccount(ctx, removal.UserID)
		}),
		// The shared workspace forgets the account as a member; the pages and
		// versions it saved stay under its name (ADR 0040). It comes
		// last, after the steps of deletions already under way.
		accounts.ForgetStep("workspace", func(ctx context.Context, removal accounts.Removal) error {
			return removeWorkspace(ctx, removal.UserID)
		}),
	}
}
