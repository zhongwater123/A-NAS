package photos

import (
	"context"
	"database/sql"
	"errors"
	"strings"
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
	own, err := s.ensurePrivateLibrary(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	shared, err := s.libraryByID(ctx, s.db, s.sharedLibraryID)
	if err != nil {
		return nil, err
	}
	libraries := []Library{publicLibrary(p, own), publicLibrary(p, shared)}
	if !p.Admin {
		return libraries, nil
	}
	now := s.now()
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
		libraries = append(libraries, publicLibrary(p, viewed))
	}
	return libraries, nil
}

func publicLibrary(p Principal, lib library) Library {
	return Library{
		ID: lib.id, Kind: lib.kind, OwnerUserID: lib.ownerUserID, CreatedAt: lib.createdAt,
		Viewing: viewingOnly(p, lib),
	}
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
	Directories []Directory `json:"directories"`
	Assets      []Asset     `json:"assets"`
}

// ListDirectory returns one level of the library projection: the virtual
// directories and the available photo assets directly inside directoryID, or
// inside the library root when directoryID is empty.
func (s *Service) ListDirectory(ctx context.Context, p Principal, libraryID, directoryID string) (DirectoryListing, error) {
	lib, err := s.visibleLibrary(ctx, s.db, p, libraryID)
	if err != nil {
		return DirectoryListing{}, err
	}
	if err := s.checkDirectory(ctx, s.db, lib, directoryID); err != nil {
		return DirectoryListing{}, err
	}
	rows, err := s.db.QueryContext(ctx, directorySelect+" WHERE d.library_id = ? AND IFNULL(d.parent_id, '') = ? ORDER BY d.name, d.id",
		lib.id, directoryID)
	if err != nil {
		return DirectoryListing{}, err
	}
	defer rows.Close()
	listing := DirectoryListing{Directories: []Directory{}, Assets: []Asset{}}
	for rows.Next() {
		record, err := scanDirectory(rows)
		if err != nil {
			return DirectoryListing{}, err
		}
		listing.Directories = append(listing.Directories, record.Directory)
	}
	if err := rows.Err(); err != nil {
		return DirectoryListing{}, err
	}
	records, err := s.queryAssets(ctx, s.db,
		"WHERE a.library_id = ? AND IFNULL(a.directory_id, '') = ? AND a.trashed_at IS NULL ORDER BY a.name, a.id",
		lib.id, directoryID)
	if err != nil {
		return DirectoryListing{}, err
	}
	for _, record := range records {
		listing.Assets = append(listing.Assets, record.Asset)
	}
	return listing, nil
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

func (s *Service) RenameDirectory(ctx context.Context, p Principal, directoryID, name string) (Directory, error) {
	name, ok := cleanName(name)
	if !ok {
		return Directory{}, ErrInvalidName
	}
	return s.changeDirectory(ctx, p, directoryID, "photo.directory_renamed", func(tx *sql.Tx, record directoryRecord) error {
		_, err := tx.ExecContext(ctx, "UPDATE directories SET name = ? WHERE id = ?", name, record.ID)
		return conflictOnUnique(err)
	})
}

// MoveDirectory moves a directory under parentID in the same library, or to
// the library root when parentID is empty.
func (s *Service) MoveDirectory(ctx context.Context, p Principal, directoryID, parentID string) (Directory, error) {
	return s.changeDirectory(ctx, p, directoryID, "photo.directory_moved", func(tx *sql.Tx, record directoryRecord) error {
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
		_, err := tx.ExecContext(ctx, "UPDATE directories SET parent_id = NULLIF(?, '') WHERE id = ?", parentID, record.ID)
		return conflictOnUnique(err)
	})
}

// DeleteDirectory removes an empty virtual directory. Trashed photo assets
// that were inside it are restored to the library root.
func (s *Service) DeleteDirectory(ctx context.Context, p Principal, directoryID string) error {
	_, err := s.changeDirectory(ctx, p, directoryID, "photo.directory_deleted", func(tx *sql.Tx, record directoryRecord) error {
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
		_, err := tx.ExecContext(ctx, "DELETE FROM directories WHERE id = ?", record.ID)
		return err
	})
	return err
}

func (s *Service) changeDirectory(ctx context.Context, p Principal, directoryID, action string, change func(*sql.Tx, directoryRecord) error) (Directory, error) {
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
		return s.audit(ctx, tx, p.UserID, action, directoryID, record.Name)
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
