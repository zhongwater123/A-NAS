package media

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
)

const (
	maxLibraryName    = 40
	maxLibraryFolders = 16
)

type libraryRow struct {
	ID          string
	Name        string
	Kind        LibraryKind
	SpaceID     string
	SpaceKind   accounts.SpaceKind
	OwnerUserID string
	CreatedBy   string
	CreatedAt   string
	ScannedAt   string
	ScanError   string
}

// visible is the media Policy: a library in the shared space is visible to
// every active account, one in a personal space to its owner only. That is
// the spaces' own read permission, so the media center adds no new rule;
// Administrative Viewing Mode does not extend to media libraries.
func (l libraryRow) visible(actor accounts.User) bool {
	if actor.Status != accounts.UserStatusActive {
		return false
	}
	return l.SpaceKind == accounts.SpaceKindShared || l.OwnerUserID == actor.ID
}

// manageable: administrators manage the shared space's libraries, owners
// their own.
func (l libraryRow) manageable(actor accounts.User) bool {
	if !l.visible(actor) {
		return false
	}
	if l.SpaceKind == accounts.SpaceKindShared {
		return actor.Role == accounts.RoleAdmin
	}
	return l.OwnerUserID == actor.ID
}

const libraryColumns = "id, name, kind, space_id, space_kind, owner_user_id, created_by, created_at, scanned_at, scan_error"

func scanLibrary(row interface{ Scan(...any) error }) (libraryRow, error) {
	var library libraryRow
	err := row.Scan(&library.ID, &library.Name, &library.Kind, &library.SpaceID, &library.SpaceKind,
		&library.OwnerUserID, &library.CreatedBy, &library.CreatedAt, &library.ScannedAt, &library.ScanError)
	return library, err
}

func (s *Service) allLibraries(ctx context.Context) ([]libraryRow, error) {
	rows, err := s.store.db.QueryContext(ctx, "SELECT "+libraryColumns+" FROM libraries ORDER BY created_at, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var libraries []libraryRow
	for rows.Next() {
		library, err := scanLibrary(rows)
		if err != nil {
			return nil, err
		}
		libraries = append(libraries, library)
	}
	return libraries, rows.Err()
}

func (s *Service) visibleLibraries(ctx context.Context, actor accounts.User) ([]libraryRow, error) {
	all, err := s.allLibraries(ctx)
	if err != nil {
		return nil, err
	}
	var visible []libraryRow
	for _, library := range all {
		if library.visible(actor) {
			visible = append(visible, library)
		}
	}
	return visible, nil
}

func (s *Service) library(ctx context.Context, actor accounts.User, id string) (libraryRow, error) {
	library, err := scanLibrary(s.store.db.QueryRowContext(ctx, "SELECT "+libraryColumns+" FROM libraries WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !library.visible(actor)) {
		return libraryRow{}, ErrNotFound
	}
	return library, err
}

// ListLibraries returns the libraries the actor can see.
func (s *Service) ListLibraries(ctx context.Context, actor accounts.User) ([]Library, error) {
	rows, err := s.visibleLibraries(ctx, actor)
	if err != nil {
		return nil, err
	}
	libraries := make([]Library, 0, len(rows))
	for _, row := range rows {
		library, err := s.describeLibrary(ctx, actor, row)
		if err != nil {
			return nil, err
		}
		libraries = append(libraries, library)
	}
	return libraries, nil
}

func (s *Service) GetLibrary(ctx context.Context, actor accounts.User, id string) (Library, error) {
	row, err := s.library(ctx, actor, id)
	if err != nil {
		return Library{}, err
	}
	return s.describeLibrary(ctx, actor, row)
}

func (s *Service) describeLibrary(ctx context.Context, actor accounts.User, row libraryRow) (Library, error) {
	library := Library{
		ID: row.ID, Name: row.Name, Kind: row.Kind, SpaceID: row.SpaceID, SpaceKind: row.SpaceKind,
		OwnerUserID: row.OwnerUserID, CreatedBy: row.CreatedBy, CreatedAt: parseTime(row.CreatedAt),
		CanManage: row.manageable(actor), Folders: []LibraryFolder{}, Covers: []string{},
	}
	folders, err := s.store.db.QueryContext(ctx, "SELECT entry_id, name, path, missing FROM library_folders WHERE library_id = ? ORDER BY position", row.ID)
	if err != nil {
		return Library{}, err
	}
	for folders.Next() {
		var folder LibraryFolder
		if err := folders.Scan(&folder.EntryID, &folder.Name, &folder.Path, &folder.Missing); err != nil {
			_ = folders.Close()
			return Library{}, err
		}
		library.Folders = append(library.Folders, folder)
	}
	if err := folders.Close(); err != nil {
		return Library{}, err
	}
	err = s.store.db.QueryRowContext(ctx, `
SELECT
  (SELECT COUNT(*) FROM videos WHERE library_id = ?1 AND kind = 'movie'),
  (SELECT COUNT(*) FROM shows WHERE library_id = ?1),
  (SELECT COUNT(*) FROM videos WHERE library_id = ?1 AND kind = 'episode'),
  (SELECT COUNT(*) FROM videos WHERE library_id = ?1 AND kind = 'other'),
  (SELECT COALESCE(SUM(size_bytes), 0) FROM videos WHERE library_id = ?1),
  (SELECT COUNT(*) FROM videos WHERE library_id = ?1 AND (probe_state = 'pending' OR frame_state = 'pending'))`, row.ID).Scan(
		&library.Counts.Movies, &library.Counts.Shows, &library.Counts.Episodes, &library.Counts.Others,
		&library.Counts.SizeBytes, &library.Scan.Pending)
	if err != nil {
		return Library{}, err
	}
	library.Scan.State = "idle"
	s.mu.Lock()
	scanning := s.scanning[row.ID]
	s.mu.Unlock()
	if scanning {
		library.Scan.State = "scanning"
	} else if library.Scan.Pending > 0 && s.processor != nil {
		library.Scan.State = "processing"
	}
	if row.ScannedAt != "" {
		scanned := parseTime(row.ScannedAt)
		library.Scan.ScannedAt = &scanned
	}
	library.Scan.Error = row.ScanError
	covers, err := s.store.db.QueryContext(ctx, `
SELECT id FROM (
  SELECT id, added_at FROM shows WHERE library_id = ?1 AND poster_entry <> ''
  UNION ALL
  SELECT id, added_at FROM videos WHERE library_id = ?1 AND kind = 'movie' AND (poster_entry <> '' OR frame_state = 'ready' OR backdrop_entry <> '' OR thumb_entry <> '')
  UNION ALL
  SELECT id, added_at FROM videos WHERE library_id = ?1 AND kind = 'other' AND frame_state = 'ready'
) ORDER BY added_at DESC LIMIT 4`, row.ID)
	if err != nil {
		return Library{}, err
	}
	defer covers.Close()
	for covers.Next() {
		var id string
		if err := covers.Scan(&id); err != nil {
			return Library{}, err
		}
		library.Covers = append(library.Covers, id)
	}
	return library, covers.Err()
}

type resolvedFolder struct {
	entry files.Entry
	path  string
}

// resolveInput checks a library's name, kind and folders for actor, who must
// be able to manage a library in that space.
func (s *Service) resolveInput(ctx context.Context, actor accounts.User, input LibraryInput, exclude string) (accounts.Space, []resolvedFolder, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || utf8.RuneCountInString(input.Name) > maxLibraryName {
		return accounts.Space{}, nil, fmt.Errorf("%w: name must be 1-%d characters", ErrInvalid, maxLibraryName)
	}
	switch input.Kind {
	case LibraryMovies, LibraryShows, LibraryMixed, LibraryOther:
	default:
		return accounts.Space{}, nil, fmt.Errorf("%w: unknown library kind", ErrInvalid)
	}
	if len(input.FolderIDs) == 0 || len(input.FolderIDs) > maxLibraryFolders {
		return accounts.Space{}, nil, fmt.Errorf("%w: choose 1-%d folders", ErrInvalid, maxLibraryFolders)
	}
	spaces, err := s.spaces.ListSpaces(ctx, actor)
	if err != nil {
		return accounts.Space{}, nil, err
	}
	var space accounts.Space
	for _, candidate := range spaces {
		if candidate.ID == input.SpaceID && candidate.Viewing == nil {
			space = candidate
		}
	}
	switch {
	case space.ID == "":
		return accounts.Space{}, nil, ErrNotFound
	case space.Kind == accounts.SpaceKindShared && actor.Role != accounts.RoleAdmin:
		return accounts.Space{}, nil, fmt.Errorf("%w: only administrators manage shared libraries", ErrForbidden)
	case space.Kind == accounts.SpaceKindPrivate && space.OwnerUserID != actor.ID:
		return accounts.Space{}, nil, ErrForbidden
	}
	var folders []resolvedFolder
	seen := map[string]bool{}
	for _, id := range input.FolderIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		entry, path, err := s.files.Lookup(ctx, actor, id)
		if errors.Is(err, files.ErrNotFound) || errors.Is(err, files.ErrForbidden) {
			return accounts.Space{}, nil, fmt.Errorf("%w: folder %s", ErrNotFound, id)
		}
		if err != nil {
			return accounts.Space{}, nil, err
		}
		if entry.Kind != files.EntryKindDirectory || entry.SpaceID != space.ID {
			return accounts.Space{}, nil, fmt.Errorf("%w: every folder must be a folder of the chosen space", ErrInvalid)
		}
		folders = append(folders, resolvedFolder{entry: entry, path: path})
	}
	// A video in two libraries would show up twice, so folders may not
	// overlap within the space.
	others, err := s.store.db.QueryContext(ctx, `
SELECT f.path FROM library_folders f JOIN libraries l ON l.id = f.library_id
WHERE l.space_id = ? AND l.id <> ?`, space.ID, exclude)
	if err != nil {
		return accounts.Space{}, nil, err
	}
	var taken []string
	for others.Next() {
		var path string
		if err := others.Scan(&path); err != nil {
			_ = others.Close()
			return accounts.Space{}, nil, err
		}
		taken = append(taken, path)
	}
	if err := others.Close(); err != nil {
		return accounts.Space{}, nil, err
	}
	for i, folder := range folders {
		for _, other := range taken {
			if overlaps(folder.path, other) {
				return accounts.Space{}, nil, fmt.Errorf("%w: %s is already part of another library", ErrConflict, folder.path)
			}
		}
		for _, other := range folders[:i] {
			if overlaps(folder.path, other.path) {
				return accounts.Space{}, nil, fmt.Errorf("%w: %s and %s overlap", ErrConflict, folder.path, other.path)
			}
		}
	}
	return space, folders, nil
}

func overlaps(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func (s *Service) CreateLibrary(ctx context.Context, actor accounts.User, input LibraryInput) (Library, error) {
	space, folders, err := s.resolveInput(ctx, actor, input, "")
	if err != nil {
		return Library{}, err
	}
	row := libraryRow{
		ID: s.randomID("library"), Name: strings.TrimSpace(input.Name), Kind: input.Kind, SpaceID: space.ID,
		SpaceKind: space.Kind, CreatedBy: actor.ID, CreatedAt: formatTime(s.now()),
	}
	if space.Kind == accounts.SpaceKindPrivate {
		row.OwnerUserID = space.OwnerUserID
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return Library{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "INSERT INTO libraries("+libraryColumns+") VALUES(?,?,?,?,?,?,?,?,'','')",
		row.ID, row.Name, row.Kind, row.SpaceID, row.SpaceKind, row.OwnerUserID, row.CreatedBy, row.CreatedAt); err != nil {
		return Library{}, err
	}
	if err := insertFolders(ctx, tx, row.ID, folders); err != nil {
		return Library{}, err
	}
	if err := tx.Commit(); err != nil {
		return Library{}, err
	}
	s.ScanInBackground(ctx, actor, row.ID)
	return s.describeLibrary(ctx, actor, row)
}

func insertFolders(ctx context.Context, tx *sql.Tx, libraryID string, folders []resolvedFolder) error {
	for position, folder := range folders {
		if _, err := tx.ExecContext(ctx, "INSERT INTO library_folders(library_id, entry_id, position, name, path) VALUES(?,?,?,?,?)",
			libraryID, folder.entry.ID, position, folder.entry.Name, folder.path); err != nil {
			return err
		}
	}
	return nil
}

// UpdateLibrary renames a library or changes its kind or folders. Changing
// the kind or folders rescans it; videos keep their progress where they
// stay in the library.
func (s *Service) UpdateLibrary(ctx context.Context, actor accounts.User, id string, input LibraryInput) (Library, error) {
	row, err := s.library(ctx, actor, id)
	if err != nil {
		return Library{}, err
	}
	if !row.manageable(actor) {
		return Library{}, ErrForbidden
	}
	input.SpaceID = row.SpaceID
	if input.Kind == "" {
		input.Kind = row.Kind
	}
	if input.FolderIDs == nil {
		existing, err := s.describeLibrary(ctx, actor, row)
		if err != nil {
			return Library{}, err
		}
		for _, folder := range existing.Folders {
			input.FolderIDs = append(input.FolderIDs, folder.EntryID)
		}
	}
	_, folders, err := s.resolveInput(ctx, actor, input, row.ID)
	if err != nil {
		return Library{}, err
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return Library{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "UPDATE libraries SET name = ?, kind = ? WHERE id = ?", strings.TrimSpace(input.Name), input.Kind, row.ID); err != nil {
		return Library{}, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM library_folders WHERE library_id = ?", row.ID); err != nil {
		return Library{}, err
	}
	if err := insertFolders(ctx, tx, row.ID, folders); err != nil {
		return Library{}, err
	}
	if err := tx.Commit(); err != nil {
		return Library{}, err
	}
	row.Name, row.Kind = strings.TrimSpace(input.Name), input.Kind
	s.ScanInBackground(ctx, actor, row.ID)
	return s.describeLibrary(ctx, actor, row)
}

// DeleteLibrary forgets a library's catalog, progress and favorites. Files
// are never touched.
func (s *Service) DeleteLibrary(ctx context.Context, actor accounts.User, id string) error {
	row, err := s.library(ctx, actor, id)
	if err != nil {
		return err
	}
	if !row.manageable(actor) {
		return ErrForbidden
	}
	ids, err := s.videoIDs(ctx, "library_id = ?", row.ID)
	if err != nil {
		return err
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Videos first, so their triggers clear favorites and collections.
	for _, statement := range []string{"DELETE FROM videos WHERE library_id = ?", "DELETE FROM shows WHERE library_id = ?", "DELETE FROM libraries WHERE id = ?"} {
		if _, err := tx.ExecContext(ctx, statement, row.ID); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.dropFrames(ids)
	return nil
}
