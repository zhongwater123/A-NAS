package photos

import (
	"context"
	"database/sql"
	"strings"
	"unicode/utf8"
)

// User tags are user metadata of one photo asset (CONTEXT.md): they are
// never derived, rebuilt or cleared with derived data. Whoever may rename a
// photo may tag it.

// MaxTagRunes bounds a user tag.
const MaxTagRunes = 30

// AddTag gives a photo a user tag and returns the photo's details.
func (s *Service) AddTag(ctx context.Context, p Principal, assetID, name string) (Asset, error) {
	name, ok := cleanTag(name)
	if !ok {
		return Asset{}, ErrInvalidName
	}
	return s.changeMetadata(ctx, p, assetID, func(tx *sql.Tx, record assetRecord) error {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO user_tags(asset_id, name, created_by, created_at) VALUES(?, ?, ?, ?) ON CONFLICT DO NOTHING",
			record.ID, name, p.UserID, formatTime(s.now())); err != nil {
			return err
		}
		return s.audit(ctx, tx, p.UserID, "photo.tag_added", record.ID, name)
	})
}

// RemoveTag takes a user tag off a photo and returns the photo's details.
func (s *Service) RemoveTag(ctx context.Context, p Principal, assetID, name string) (Asset, error) {
	return s.changeMetadata(ctx, p, assetID, func(tx *sql.Tx, record assetRecord) error {
		result, err := tx.ExecContext(ctx, "DELETE FROM user_tags WHERE asset_id = ? AND name = ?", record.ID, name)
		if err != nil {
			return err
		}
		if removed, err := result.RowsAffected(); err != nil || removed == 0 {
			if err == nil {
				err = ErrNotFound
			}
			return err
		}
		return s.audit(ctx, tx, p.UserID, "photo.tag_removed", record.ID, name)
	})
}

// changeMetadata runs change on a photo p may change and returns the photo's
// details afterwards.
func (s *Service) changeMetadata(ctx context.Context, p Principal, assetID string, change func(*sql.Tx, assetRecord) error) (Asset, error) {
	if _, err := s.changeAsset(ctx, p, assetID, change); err != nil {
		return Asset{}, err
	}
	record, err := s.visibleAsset(ctx, s.db, p, assetID)
	if err != nil {
		return Asset{}, err
	}
	return s.details(ctx, p, record)
}

func cleanTag(name string) (string, bool) {
	name, ok := cleanName(name)
	return name, ok && utf8.RuneCountInString(name) <= MaxTagRunes
}

func (s *Service) tagsOf(ctx context.Context, assetID string) ([]string, error) {
	return s.strings(ctx, "SELECT name FROM user_tags WHERE asset_id = ? ORDER BY created_at, name", assetID)
}

func (s *Service) strings(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// tagMatches reports whether any of the tags, joined by unit separators,
// contains the folded query.
func tagMatches(tags, folded string) bool {
	for _, tag := range strings.Split(tags, "\x1f") {
		if tag != "" && strings.Contains(strings.ToLower(tag), folded) {
			return true
		}
	}
	return false
}
