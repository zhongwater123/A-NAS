package files

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// maxArchiveEntries bounds the selection of one archive download.
const maxArchiveEntries = 1000

// archiveSkip leaves A-NAS bookkeeping and in-flight temporaries out of
// archive downloads.
var archiveSkip = ScanSkip{
	Names:    reconcileSkip.Names,
	Prefixes: []string{uploadTemporaryPrefix, copyTemporaryPrefix},
}

// ArchiveItem is a file or folder of an archive download.
type ArchiveItem struct {
	// Path is slash-separated inside the archive.
	Path    string
	Kind    EntryKind
	Size    int64
	ModTime time.Time
	source  string
}

// Folder is one folder on the way from a space root to an entry.
type Folder struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ArchiveItems lists what a download of the given entries of one space
// holds, in the order an archive writes it: each entry under its own name,
// folders followed by everything below them. Folders the actor cannot read
// appear empty, as they do over SMB.
func (s *Service) ArchiveItems(ctx context.Context, actor accounts.User, entryIDs []string) ([]ArchiveItem, error) {
	if len(entryIDs) == 0 || len(entryIDs) > maxArchiveEntries {
		return nil, ErrInvalidSelection
	}
	var spaceID string
	if err := s.store.db.QueryRowContext(ctx, "SELECT space_id FROM entries WHERE id = ? AND trashed = 0", entryIDs[0]).Scan(&spaceID); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	_, root, err := s.authorizedSpace(ctx, actor, spaceID, false)
	if err != nil {
		return nil, err
	}
	var items []ArchiveItem
	used := make(map[string]bool)
	for _, entryID := range entryIDs {
		// entryPath only finds entries of this space, so a selection cannot
		// mix spaces.
		entry, source, err := s.entryPath(ctx, root, spaceID, entryID)
		if err != nil {
			return nil, err
		}
		name := entry.Name
		for number := 1; used[name]; number++ {
			name = numberedName(entry.Name, number)
		}
		used[name] = true
		info, err := s.fs.Lstat(ctx, source)
		if err != nil {
			return nil, err
		}
		if entry.Kind == EntryKindFile {
			items = append(items, ArchiveItem{Path: name, Kind: EntryKindFile, Size: info.Size, ModTime: info.ModTime, source: source})
			continue
		}
		items = append(items, ArchiveItem{Path: name, Kind: EntryKindDirectory, ModTime: info.ModTime, source: source})
		scanned, err := s.fs.Scan(ctx, source, archiveSkip)
		if err != nil {
			return nil, err
		}
		for _, item := range scanned {
			archived := ArchiveItem{Path: path.Join(name, item.Path), ModTime: item.Info.ModTime, source: filepath.Join(source, filepath.FromSlash(item.Path))}
			switch item.Info.Kind {
			case KindFile:
				archived.Kind, archived.Size = EntryKindFile, item.Info.Size
			case KindDirectory:
				archived.Kind = EntryKindDirectory
			default:
				continue
			}
			items = append(items, archived)
		}
	}
	return items, nil
}

// OpenArchiveItem opens a listed file as the actor of the context.
func (s *Service) OpenArchiveItem(ctx context.Context, item ArchiveItem) (*os.File, error) {
	if item.Kind != EntryKindFile {
		return nil, ErrUnsupportedType
	}
	return s.fs.Open(ctx, item.source)
}

// Folders returns the folders from the root of a space down to folderID, so
// a client can show and open where a file is. The space root is empty.
func (s *Service) Folders(ctx context.Context, actor accounts.User, spaceID, folderID string) ([]Folder, error) {
	allowed, err := s.policy.CanAccessSpace(ctx, actor, spaceID, false)
	if err != nil || !allowed {
		return nil, ErrForbidden
	}
	var folders []Folder
	for current := folderID; current != "" && len(folders) <= 4096; {
		var folder Folder
		var parentID string
		err := s.store.db.QueryRowContext(ctx, "SELECT id, name, parent_id FROM entries WHERE id = ? AND space_id = ? AND kind = 'directory' AND trashed = 0",
			current, spaceID).Scan(&folder.ID, &folder.Name, &parentID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		folders = append([]Folder{folder}, folders...)
		current = parentID
	}
	return folders, nil
}
