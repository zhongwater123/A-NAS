package workspace

import (
	"context"
	"database/sql"
	"time"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// TrashedPage is a deleted page as the trash shows it.
type TrashedPage struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedBy Person    `json:"createdBy"`
	DeletedBy Person    `json:"deletedBy"`
	DeletedAt time.Time `json:"deletedAt"`
	// PurgeAt is when the page and its versions go for good.
	PurgeAt time.Time `json:"purgeAt"`
	// Versions is how many versions come back with the page.
	Versions int `json:"versions"`
}

// Trash lists the deleted pages, the most recently deleted first. Everyone
// who uses the workspace sees them, as they saw the pages before.
func (s *Store) Trash(ctx context.Context) ([]TrashedPage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, title, created_by, created_by_name, deleted_by, deleted_by_name, deleted_at, latest_version
FROM pages WHERE deleted_at <> '' ORDER BY deleted_at DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	pages := []TrashedPage{}
	for rows.Next() {
		var page TrashedPage
		var deletedAt string
		if err := rows.Scan(&page.ID, &page.Title, &page.CreatedBy.ID, &page.CreatedBy.Name, &page.DeletedBy.ID, &page.DeletedBy.Name,
			&deletedAt, &page.Versions); err != nil {
			return nil, err
		}
		page.DeletedAt = parseTime(deletedAt)
		page.PurgeAt = page.DeletedAt.AddDate(0, 0, TrashDays)
		pages = append(pages, page)
	}
	return pages, rows.Err()
}

// RestorePage brings a page back from the trash with all its versions, in
// its place in the list. Only the account that uploaded it and
// administrators may.
func (s *Store) RestorePage(ctx context.Context, actor Actor, pageID string) (PageSummary, error) {
	s.writes.Lock()
	defer s.writes.Unlock()
	var page Page
	err := sqlitedb.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		title, err := s.mayManage(ctx, tx, actor, pageID, true)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE pages SET deleted_at = '', deleted_by = '', deleted_by_name = '' WHERE id = ?", pageID); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, actor, "workspace.page_restored", "page", pageID, title); err != nil {
			return err
		}
		page, err = s.page(ctx, tx, pageID)
		return err
	})
	if err != nil {
		return PageSummary{}, err
	}
	return page.PageSummary, nil
}

// PurgePage removes a page in the trash and all its versions for good. Only
// the account that uploaded it and administrators may.
func (s *Store) PurgePage(ctx context.Context, actor Actor, pageID string) error {
	s.writes.Lock()
	defer s.writes.Unlock()
	return sqlitedb.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		title, err := s.mayManage(ctx, tx, actor, pageID, true)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM pages WHERE id = ?", pageID); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, "workspace.page_purged", "page", pageID, title)
	})
}

// emptyTrash removes the pages deleted more than TrashDays ago. Each one is
// recorded in the activity of whoever deleted it.
func (s *Store) emptyTrash(ctx context.Context) error {
	s.writes.Lock()
	defer s.writes.Unlock()
	cutoff := formatTime(s.now().AddDate(0, 0, -TrashDays))
	return sqlitedb.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT id, title, deleted_by FROM pages WHERE deleted_at <> '' AND deleted_at <= ?", cutoff)
		if err != nil {
			return err
		}
		type expired struct{ id, title, by string }
		var pages []expired
		for rows.Next() {
			var page expired
			if err := rows.Scan(&page.id, &page.title, &page.by); err != nil {
				_ = rows.Close()
				return err
			}
			pages = append(pages, page)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, page := range pages {
			if _, err := tx.ExecContext(ctx, "DELETE FROM pages WHERE id = ?", page.id); err != nil {
				return err
			}
			if err := s.audit(ctx, tx, Actor{AccountID: page.by}, "workspace.page_expired", "page", page.id, page.title); err != nil {
				return err
			}
		}
		return nil
	})
}
