package photos

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-sqlite3"
)

// Libraries lists the libraries p may open: the caller's private library
// (created on first use), the shared library and any member's private library
// the caller is currently viewing.
func (s *Service) Libraries(ctx context.Context, p Principal) ([]Library, error) {
	if !p.valid() {
		return nil, ErrForbidden
	}
	own, err := s.ensurePrivateLibrary(ctx, p)
	if err != nil {
		return nil, err
	}
	shared, err := s.libraryByID(ctx, s.db, s.sharedLibraryID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	libraries := []Library{publicLibrary(p, own, now), publicLibrary(p, shared, now)}
	if !p.Admin {
		return libraries, nil
	}
	seen := map[string]bool{p.UserID: true}
	for _, grant := range p.Viewing {
		if seen[grant.OwnerUserID] || !grant.ExpiresAt.After(now) {
			continue
		}
		seen[grant.OwnerUserID] = true
		viewed, err := s.privateLibrary(ctx, s.db, grant.OwnerUserID)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		libraries = append(libraries, publicLibrary(p, viewed, now))
	}
	return libraries, nil
}

func publicLibrary(p Principal, lib library, now time.Time) Library {
	public := Library{ID: lib.id, Kind: lib.kind, OwnerUserID: lib.ownerUserID, OwnerName: lib.ownerName, CreatedAt: lib.createdAt}
	if viewingOnly(p, lib) {
		if grant, ok := p.viewing(lib.ownerUserID, now); ok {
			public.Viewing = &LibraryViewing{GrantID: grant.GrantID, ExpiresAt: grant.ExpiresAt}
		}
	}
	return public
}

func (s *Service) visibleLibrary(ctx context.Context, q queryer, p Principal, libraryID string) (library, error) {
	if !p.valid() {
		return library{}, ErrForbidden
	}
	lib, err := s.libraryByID(ctx, q, libraryID)
	if err != nil {
		return library{}, err
	}
	if !canView(p, lib, s.now()) {
		return library{}, ErrNotFound
	}
	return lib, nil
}

type DirectoryListing struct {
	// Directories holds every subdirectory on the first page and nothing on
	// the pages after it.
	Directories []Directory `json:"directories"`
	Assets      []Asset     `json:"assets"`
	// Next continues the asset listing; empty on the last page.
	Next string `json:"next,omitempty"`
}

// ListDirectory returns one level of the library projection: the virtual
// directories and the available photo assets directly inside directoryID, or
// inside the library root when directoryID is empty. Assets come by name, a
// page at a time.
func (s *Service) ListDirectory(ctx context.Context, p Principal, libraryID, directoryID, cursor string, limit int) (DirectoryListing, error) {
	lib, err := s.visibleLibrary(ctx, s.db, p, libraryID)
	if err != nil {
		return DirectoryListing{}, err
	}
	if err := s.checkDirectory(ctx, s.db, lib, directoryID); err != nil {
		return DirectoryListing{}, err
	}
	limit = pageSize(limit)
	listing := DirectoryListing{Directories: []Directory{}, Assets: []Asset{}}
	where := "WHERE a.library_id = ? AND IFNULL(a.directory_id, '') = ? AND a.trashed_at IS NULL"
	args := []any{lib.id, directoryID}
	if cursor != "" {
		name, id, err := decodeCursor(cursor)
		if err != nil {
			return DirectoryListing{}, err
		}
		where += " AND (a.name > ? OR (a.name = ? AND a.id > ?))"
		args = append(args, name, name, id)
	} else if listing.Directories, err = s.subdirectories(ctx, lib, directoryID); err != nil {
		return DirectoryListing{}, err
	}
	records, err := s.queryAssets(ctx, s.db, where+" ORDER BY a.name, a.id LIMIT ?", append(args, limit+1)...)
	if err != nil {
		return DirectoryListing{}, err
	}
	if len(records) > limit {
		records = records[:limit]
		listing.Next = encodeCursor(records[limit-1].Name, records[limit-1].ID)
	}
	if err := s.addDuplicateHints(ctx, s.db, p, records); err != nil {
		return DirectoryListing{}, err
	}
	for _, record := range records {
		listing.Assets = append(listing.Assets, record.Asset)
	}
	return listing, nil
}

func (s *Service) subdirectories(ctx context.Context, lib library, directoryID string) ([]Directory, error) {
	rows, err := s.db.QueryContext(ctx, directorySelect+" WHERE d.library_id = ? AND IFNULL(d.parent_id, '') = ? ORDER BY d.name, d.id",
		lib.id, directoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	directories := []Directory{}
	for rows.Next() {
		record, err := scanDirectory(rows)
		if err != nil {
			return nil, err
		}
		directories = append(directories, record.Directory)
	}
	return directories, rows.Err()
}

func (s *Service) CreateDirectory(ctx context.Context, p Principal, libraryID, parentID, name string) (Directory, error) {
	name, ok := cleanName(name)
	if !ok {
		return Directory{}, ErrInvalidName
	}
	var created Directory
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		lib, err := s.visibleLibrary(ctx, tx, p, libraryID)
		if err != nil {
			return err
		}
		if !canAdd(p, lib) {
			return ErrForbidden
		}
		if err := s.checkDirectory(ctx, tx, lib, parentID); err != nil {
			return err
		}
		id := s.randomID("photo-directory")
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO directories(id, library_id, parent_id, name, created_by, created_at) VALUES(?, ?, NULLIF(?, ''), ?, ?, ?)",
			id, lib.id, parentID, name, p.UserID, formatTime(s.now())); err != nil {
			return conflictOnUnique(err)
		}
		record, err := s.directoryByID(ctx, tx, id)
		if err != nil {
			return err
		}
		created = record.Directory
		return s.audit(ctx, tx, p.UserID, "photo.directory_created", id, name)
	})
	return created, err
}

// DirectoryUpdate renames and/or moves a directory; nil fields stay
// unchanged. A ParentID of "" is the library root.
type DirectoryUpdate struct {
	Name     *string
	ParentID *string
}

// UpdateDirectory applies a DirectoryUpdate in one transaction. A directory
// moves only within its library and never under itself.
func (s *Service) UpdateDirectory(ctx context.Context, p Principal, directoryID string, update DirectoryUpdate) (Directory, error) {
	var name string
	if update.Name != nil {
		var ok bool
		if name, ok = cleanName(*update.Name); !ok {
			return Directory{}, ErrInvalidName
		}
	}
	return s.changeDirectory(ctx, p, directoryID, func(tx *sql.Tx, record directoryRecord) error {
		if update.ParentID != nil {
			parentID := *update.ParentID
			if err := s.checkDirectory(ctx, tx, record.library, parentID); err != nil {
				return err
			}
			for ancestor := parentID; ancestor != ""; {
				if ancestor == record.ID {
					return ErrConflict
				}
				if err := tx.QueryRowContext(ctx, "SELECT IFNULL(parent_id, '') FROM directories WHERE id = ?", ancestor).Scan(&ancestor); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, "UPDATE directories SET parent_id = NULLIF(?, '') WHERE id = ?", parentID, record.ID); err != nil {
				return conflictOnUnique(err)
			}
			if err := s.audit(ctx, tx, p.UserID, "photo.directory_moved", record.ID, record.Name); err != nil {
				return err
			}
		}
		if update.Name != nil {
			if _, err := tx.ExecContext(ctx, "UPDATE directories SET name = ? WHERE id = ?", name, record.ID); err != nil {
				return conflictOnUnique(err)
			}
			return s.audit(ctx, tx, p.UserID, "photo.directory_renamed", record.ID, record.Name)
		}
		return nil
	})
}

// DeleteDirectory removes an empty virtual directory. Trashed photo assets
// that were inside it are restored to the library root.
func (s *Service) DeleteDirectory(ctx context.Context, p Principal, directoryID string) error {
	_, err := s.changeDirectory(ctx, p, directoryID, func(tx *sql.Tx, record directoryRecord) error {
		var occupied bool
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM directories WHERE parent_id = ?)
			     OR EXISTS (SELECT 1 FROM assets WHERE directory_id = ? AND trashed_at IS NULL)`,
			record.ID, record.ID).Scan(&occupied); err != nil {
			return err
		}
		if occupied {
			return ErrConflict
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM directories WHERE id = ?", record.ID); err != nil {
			return err
		}
		return s.audit(ctx, tx, p.UserID, "photo.directory_deleted", record.ID, record.Name)
	})
	return err
}

// changeDirectory runs change, which also writes its audit events, on a
// directory p may change.
func (s *Service) changeDirectory(ctx context.Context, p Principal, directoryID string, change func(*sql.Tx, directoryRecord) error) (Directory, error) {
	var changed Directory
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if !p.valid() {
			return ErrForbidden
		}
		record, err := s.directoryByID(ctx, tx, directoryID)
		if err != nil {
			return err
		}
		if !canView(p, record.library, s.now()) {
			return ErrNotFound
		}
		if !canChange(p, record.library, record.CreatedBy) {
			return ErrForbidden
		}
		if err := change(tx, record); err != nil {
			return err
		}
		if updated, err := s.directoryByID(ctx, tx, directoryID); err == nil {
			changed = updated.Directory
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		return nil
	})
	return changed, err
}

// checkDirectory accepts the library root ("") or a directory of lib.
func (s *Service) checkDirectory(ctx context.Context, q queryer, lib library, directoryID string) error {
	if directoryID == "" {
		return nil
	}
	record, err := s.directoryByID(ctx, q, directoryID)
	if err != nil {
		return err
	}
	if record.LibraryID != lib.id {
		return ErrNotFound
	}
	return nil
}

// cleanName accepts names that every projection, including a future
// read-only network share, can show as a visible file or directory name.
func cleanName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || strings.HasPrefix(name, ".") || len(name) > 255 || !utf8.ValidString(name) {
		return "", false
	}
	for _, character := range name {
		if character < 0x20 || character == 0x7f || character == '/' || character == '\\' {
			return "", false
		}
	}
	return name, true
}

func conflictOnUnique(err error) error {
	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) && sqliteErr.ExtendedCode == sqlite3.ErrConstraintUnique {
		return ErrConflict
	}
	return err
}
