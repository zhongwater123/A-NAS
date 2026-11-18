package photos

import (
	"context"
	"database/sql"
	"errors"
	"io"
)

// SaveFilm imports a film the montage center made into the owner's private
// library and adds it to the grant, so the center can reach it like the
// videos it was cut from. A film is saved once per (owner, versionKey): a
// save cut short and tried again returns the film saved the first time,
// recorded in the same transaction as the asset. The capacity check is the
// import's own (ErrInsufficientSpace).
func (s *Service) SaveFilm(ctx context.Context, owner Principal, grantID, versionKey, name string, content io.Reader) (string, error) {
	if err := s.usableGrant(ctx, owner, grantID); err != nil {
		return "", err
	}
	if versionKey == "" || len(versionKey) > 256 {
		return "", ErrInvalidName
	}
	s.filmMu.Lock()
	defer s.filmMu.Unlock()
	var saved string
	err := s.db.QueryRowContext(ctx, "SELECT asset_id FROM montage_films WHERE owner_user_id = ? AND version_key = ?", owner.UserID, versionKey).Scan(&saved)
	if err == nil {
		return saved, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if _, err := s.Libraries(ctx, owner); err != nil {
		return "", err
	}
	lib, err := s.privateLibrary(ctx, s.db, owner.UserID)
	if err != nil {
		return "", err
	}
	asset, err := s.Import(ctx, owner, ImportRequest{LibraryID: lib.id, Name: name, Content: content,
		recorded: func(ctx context.Context, tx *sql.Tx, assetID string) error {
			now := formatTime(s.now())
			if _, err := tx.ExecContext(ctx, "INSERT INTO montage_films(owner_user_id, version_key, asset_id, created_at) VALUES(?, ?, ?, ?)",
				owner.UserID, versionKey, assetID, now); err != nil {
				return err
			}
			result, err := tx.ExecContext(ctx, "INSERT INTO montage_grant_assets(grant_id, asset_id, added_at) SELECT id, ?, ? FROM montage_grants WHERE id = ? AND owner_user_id = ? AND revoked_at IS NULL",
				assetID, now, grantID, owner.UserID)
			if err != nil {
				return err
			}
			rows, err := result.RowsAffected()
			if err == nil && rows == 0 {
				return ErrGrantNotFound
			}
			return err
		}})
	if err != nil {
		return "", err
	}
	return asset.ID, nil
}
