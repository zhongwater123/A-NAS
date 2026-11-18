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
	Revoked     bool   `json:"revoked"`
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
	// Windows, Cards and Quality are where the montage analysis stands.
	Windows AnalysisState `json:"windows,omitempty"`
	Cards   AnalysisState `json:"cards,omitempty"`
	Quality AnalysisState `json:"quality,omitempty"`
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

// granted reports which of ids are in the grant.
func (s *Service) granted(ctx context.Context, grantID string, ids []string) (map[string]bool, error) {
	in := map[string]bool{}
	if len(ids) == 0 {
		return in, nil
	}
	args := []any{grantID}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, "SELECT asset_id FROM montage_grant_assets WHERE grant_id = ? AND asset_id IN ("+
		sqlitedb.Placeholders(len(ids))+")", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		in[id] = true
	}
	return in, rows.Err()
}

// LookupGrant says where each of assetIDs stands for the grant's owner: an
// asset outside the grant, gone or no longer seen is missing; one the owner
// put in the trash is trashed.
func (s *Service) LookupGrant(ctx context.Context, owner Principal, grantID string, assetIDs []string) ([]GrantedAsset, error) {
	if err := s.usableGrant(ctx, owner, grantID); err != nil {
		return nil, err
	}
	if len(assetIDs) > MaxGrantAssets {
		return nil, ErrTooManyIDs
	}
	ids := uniqueIDs(assetIDs)
	in, err := s.granted(ctx, grantID, ids)
	if err != nil {
		return nil, err
	}
	now := s.now()
	found := make([]GrantedAsset, 0, len(ids))
	for _, id := range ids {
		entry := GrantedAsset{AssetID: id, Status: GrantMissing}
		record, err := s.assetByID(ctx, s.db, id)
		switch {
		case errors.Is(err, ErrNotFound) || !in[id]:
		case err != nil:
			return nil, err
		case !canView(owner, record.library, now) || !seesAsset(owner, record) || record.Video == nil:
		case record.Trash != nil:
			entry.Status = GrantTrashed
		default:
			entry.Status, entry.ObjectID = GrantAvailable, record.objectID
			entry.Duration, entry.Width, entry.Height, entry.TakenAt = record.Video.Duration, record.Width, record.Height, record.TakenAt
			if entry.Windows, err = s.windowsState(ctx, s.db, record.objectID); err != nil {
				return nil, err
			}
			entry.Cards, entry.Quality, _, _, err = s.montageCardState(ctx, s.db, record.objectID)
			if err != nil {
				return nil, err
			}
		}
		found = append(found, entry)
	}
	return found, nil
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

// Grants lists every grant, revoked ones included, for the montage center to
// reconcile its projects with when it starts.
func (s *Service) Grants(ctx context.Context) ([]Grant, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, owner_user_id, project_id, revoked_at IS NOT NULL FROM montage_grants ORDER BY created_at, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	grants := []Grant{}
	for rows.Next() {
		var grant Grant
		if err := rows.Scan(&grant.ID, &grant.OwnerUserID, &grant.ProjectID, &grant.Revoked); err != nil {
			return nil, err
		}
		grants = append(grants, grant)
	}
	return grants, rows.Err()
}

// RevokeGrant marks a grant revoked; revoking it again, or a grant already
// gone with its owner's account, changes nothing.
func (s *Service) RevokeGrant(ctx context.Context, grantID string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE montage_grants SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL", formatTime(s.now()), grantID)
	return err
}
