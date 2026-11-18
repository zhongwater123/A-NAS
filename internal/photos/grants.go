package photos

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// Project grants let the montage center reach a member's videos while the
// member is away (ADR 0038 decision 3), as an application reaches only the
// files a user handed it. The member registers them while present; every use
// decides again through the Policy, as the owner under a grant Principal
// (Principal.Grant), so a video that left the owner's sight, a revoked grant
// or an account no longer active refuses.

// MaxGrantAssets bounds the assets one registration names.
const MaxGrantAssets = 500

var (
	// ErrGrantNotFound covers a grant that does not exist, is revoked or is
	// another member's.
	ErrGrantNotFound = errors.New("montage grant not found")
	// ErrGrantAssets reports a registration naming an asset the owner cannot
	// grant: missing, hidden, in the trash or not a video. The four answer
	// alike, so that a registration reveals nothing of other members' photos.
	ErrGrantAssets = errors.New("an asset cannot be granted")
)

// GrantAssetStatus is where a granted asset stands for the owner now.
type GrantAssetStatus string

const (
	GrantAvailable GrantAssetStatus = "available"
	GrantTrashed   GrantAssetStatus = "trashed"
	GrantMissing   GrantAssetStatus = "missing"
)

// Grant is a project grant.
type Grant struct {
	ID          string `json:"id"`
	OwnerUserID string `json:"ownerUserId"`
	ProjectID   string `json:"projectId"`
}

// GrantedAsset is one asset of a grant as the owner sees it now. Only an
// available asset carries its object and video details.
type GrantedAsset struct {
	AssetID  string           `json:"assetId"`
	Status   GrantAssetStatus `json:"status"`
	ObjectID string           `json:"objectId,omitempty"`
	Duration float64          `json:"duration,omitempty"`
	Width    int              `json:"width,omitempty"`
	Height   int              `json:"height,omitempty"`
	TakenAt  *time.Time       `json:"takenAt,omitempty"`
	// Windows, Cards, Quality and Descriptions are where the montage
	// analysis stands; Descriptions is AnalysisUnavailable while the AI
	// Worker has no text reader.
	Windows             AnalysisState `json:"windows,omitempty"`
	Cards               AnalysisState `json:"cards,omitempty"`
	Quality             AnalysisState `json:"quality,omitempty"`
	Descriptions        AnalysisState `json:"descriptions,omitempty"`
	descriptionIdentity string
}

// grantable reports whether owner may grant record: a video it sees,
// outside the trash.
func grantable(owner Principal, record assetRecord, now time.Time) bool {
	return canView(owner, record.library, now) && seesAsset(owner, record) && record.Trash == nil && record.Video != nil
}

func uniqueIDs(ids []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// RegisterGrant registers, or extends, owner's grant for project with
// assetIDs. owner must be a grant Principal of the member present now. A
// second registration of the same project returns the same grant.
func (s *Service) RegisterGrant(ctx context.Context, owner Principal, projectID string, assetIDs []string) (Grant, error) {
	if !owner.valid() || !owner.Grant || projectID == "" || len(projectID) > 128 {
		return Grant{}, ErrForbidden
	}
	if len(assetIDs) > MaxGrantAssets {
		return Grant{}, ErrTooManyIDs
	}
	ids := uniqueIDs(assetIDs)
	if len(ids) == 0 {
		return Grant{}, ErrGrantAssets
	}
	s.commitMu.Lock()
	defer s.commitMu.Unlock()
	var grant Grant
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		now := s.now()
		for _, id := range ids {
			record, err := s.assetByID(ctx, tx, id)
			if errors.Is(err, ErrNotFound) || err == nil && !grantable(owner, record, now) {
				return ErrGrantAssets
			}
			if err != nil {
				return err
			}
		}
		created := formatTime(now)
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO montage_grants(id, owner_user_id, project_id, created_at) VALUES(?, ?, ?, ?) ON CONFLICT DO NOTHING",
			s.randomID("grant"), owner.UserID, projectID, created); err != nil {
			return err
		}
		var revoked sql.NullString
		if err := tx.QueryRowContext(ctx, "SELECT id, revoked_at FROM montage_grants WHERE owner_user_id = ? AND project_id = ?",
			owner.UserID, projectID).Scan(&grant.ID, &revoked); err != nil {
			return err
		}
		if revoked.Valid {
			return ErrGrantNotFound
		}
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, "INSERT INTO montage_grant_assets(grant_id, asset_id, added_at) VALUES(?, ?, ?) ON CONFLICT DO NOTHING",
				grant.ID, id, created); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Grant{}, err
	}
	grant.OwnerUserID, grant.ProjectID = owner.UserID, projectID
	return grant, nil
}

// GrantOf returns a grant that is not revoked.
func (s *Service) GrantOf(ctx context.Context, grantID string) (Grant, error) {
	grant := Grant{ID: grantID}
	var revoked sql.NullString
	err := s.db.QueryRowContext(ctx, "SELECT owner_user_id, project_id, revoked_at FROM montage_grants WHERE id = ?", grantID).
		Scan(&grant.OwnerUserID, &grant.ProjectID, &revoked)
	if errors.Is(err, sql.ErrNoRows) || err == nil && revoked.Valid {
		return Grant{}, ErrGrantNotFound
	}
	return grant, err
}

// usableGrant checks that owner holds grantID, not revoked.
func (s *Service) usableGrant(ctx context.Context, owner Principal, grantID string) error {
	if !owner.valid() || !owner.Grant {
		return ErrForbidden
	}
	grant, err := s.GrantOf(ctx, grantID)
	if err != nil {
		return err
	}
	if grant.OwnerUserID != owner.UserID {
		return ErrGrantNotFound
	}
	return nil
}

// grantedAssets reads, in one query, those of ids that are in the grant.
func (s *Service) grantedAssets(ctx context.Context, grantID string, ids []string) (map[string]assetRecord, error) {
	records := map[string]assetRecord{}
	if len(ids) == 0 {
		return records, nil
	}
	args := []any{grantID}
	for _, id := range ids {
		args = append(args, id)
	}
	found, err := s.queryAssets(ctx, s.db, "WHERE a.id IN (SELECT asset_id FROM montage_grant_assets WHERE grant_id = ? AND asset_id IN ("+
		sqlitedb.Placeholders(len(ids))+"))", args...)
	if err != nil {
		return nil, err
	}
	for _, record := range found {
		records[record.ID] = record
	}
	return records, nil
}

// LookupGrant says where each of assetIDs stands for the grant's owner: an
// asset outside the grant, gone or no longer seen is missing; one the owner
// put in the trash is trashed.
func (s *Service) LookupGrant(ctx context.Context, owner Principal, grantID string, assetIDs []string) ([]GrantedAsset, error) {
	found, _, err := s.lookupGrant(ctx, owner, grantID, assetIDs)
	return found, err
}

// lookupGrant is LookupGrant, with where the montage analysis of each
// available video stands, read for all of them in one go.
func (s *Service) lookupGrant(ctx context.Context, owner Principal, grantID string, assetIDs []string) ([]GrantedAsset, map[string]analysisProgress, error) {
	if err := s.usableGrant(ctx, owner, grantID); err != nil {
		return nil, nil, err
	}
	if len(assetIDs) > MaxGrantAssets {
		return nil, nil, ErrTooManyIDs
	}
	ids := uniqueIDs(assetIDs)
	records, err := s.grantedAssets(ctx, grantID, ids)
	if err != nil {
		return nil, nil, err
	}
	videos := map[string]float64{}
	now := s.now()
	found := make([]GrantedAsset, 0, len(ids))
	for _, id := range ids {
		entry := GrantedAsset{AssetID: id, Status: GrantMissing}
		record, in := records[id]
		switch {
		case !in:
		case !canView(owner, record.library, now) || !seesAsset(owner, record) || record.Video == nil:
		case record.Trash != nil:
			entry.Status = GrantTrashed
		default:
			entry.Status, entry.ObjectID = GrantAvailable, record.objectID
			entry.Duration, entry.Width, entry.Height, entry.TakenAt = record.Video.Duration, record.Width, record.Height, record.TakenAt
			videos[record.objectID] = record.Video.Duration
			if entry.Cards, err = s.cardsState(ctx, record.objectID); err != nil {
				return nil, nil, err
			}
			if entry.Quality, err = s.qualityState(ctx, record.objectID); err != nil {
				return nil, nil, err
			}
		}
		found = append(found, entry)
	}
	progress, err := s.progressOf(ctx, videos)
	if err != nil {
		return nil, nil, err
	}
	for i := range found {
		if found[i].Status == GrantAvailable {
			found[i].Windows = progress[found[i].ObjectID].windows
		}
	}
	return found, progress, s.fillDescriptionProgress(ctx, found)
}

// OpenGrant opens a granted video's original read-only, with its object.
// The caller must close the file.
func (s *Service) OpenGrant(ctx context.Context, owner Principal, grantID, assetID string) (*os.File, string, error) {
	looked, err := s.LookupGrant(ctx, owner, grantID, []string{assetID})
	if err != nil {
		return nil, "", err
	}
	if len(looked) != 1 || looked[0].Status != GrantAvailable {
		return nil, "", ErrNotFound
	}
	file, err := s.openObject(looked[0].ObjectID)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", fmt.Errorf("original of %s is missing from the content store", assetID)
	}
	if err != nil {
		return nil, "", err
	}
	return file, looked[0].ObjectID, nil
}

// RemoveFromGrant takes assetIDs out of the grant; the montage center can
// no longer reach them through it.
func (s *Service) RemoveFromGrant(ctx context.Context, owner Principal, grantID string, assetIDs []string) error {
	if err := s.usableGrant(ctx, owner, grantID); err != nil {
		return err
	}
	if len(assetIDs) > MaxGrantAssets {
		return ErrTooManyIDs
	}
	ids := uniqueIDs(assetIDs)
	if len(ids) == 0 {
		return nil
	}
	args := []any{grantID}
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM montage_grant_assets WHERE grant_id = ? AND asset_id IN ("+sqlitedb.Placeholders(len(ids))+")", args...)
	return err
}

// Grants lists, in ID order, up to limit grants that are not revoked and
// whose ID comes after after. The montage center pages through them to
// reconcile its projects when it starts; a revoked grant is gone for it.
func (s *Service) Grants(ctx context.Context, after string, limit int) ([]Grant, error) {
	if limit <= 0 {
		return nil, errors.New("a page of grants needs a limit")
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, owner_user_id, project_id FROM montage_grants WHERE revoked_at IS NULL AND id > ? ORDER BY id LIMIT ?", after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	grants := []Grant{}
	for rows.Next() {
		var grant Grant
		if err := rows.Scan(&grant.ID, &grant.OwnerUserID, &grant.ProjectID); err != nil {
			return nil, err
		}
		grants = append(grants, grant)
	}
	return grants, rows.Err()
}

// RevokeGrant revokes a grant and lets go of its assets. The grant's row
// stays, marked, so that its project cannot register again until the owner's
// account is removed; revoking again, or a grant already gone with its
// owner's account, changes nothing.
func (s *Service) RevokeGrant(ctx context.Context, grantID string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE montage_grants SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL",
			formatTime(s.now()), grantID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM montage_grant_assets WHERE grant_id = ?", grantID)
		return err
	})
}
