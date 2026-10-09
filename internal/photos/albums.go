package photos

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Albums (CONTEXT.md) are named groups of the photo assets of one library. An
// album refers to its library's photos; a photo can be in several albums, and
// leaving an album or deleting one never deletes a photo. A photo from
// another library is first copied into the album's library. Trashed photos
// stay members but are not listed until restored. Albums follow the rules of
// virtual directories: whoever may add to a library may create one, and its
// creator, the private library's owner or an administrator of the shared
// library may change it.

type Album struct {
	ID        string    `json:"id"`
	LibraryID string    `json:"libraryId"`
	Name      string    `json:"name"`
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	// Photos counts the album's photos outside the trash; CoverID is the
	// newest of them.
	Photos  int    `json:"photos"`
	CoverID string `json:"coverId,omitempty"`
}

// AlbumRef names an album a photo is in.
type AlbumRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type albumRecord struct {
	Album
	library library
}

const albumSelect = `
SELECT al.id, al.library_id, al.name, al.created_by, al.created_at,
       (SELECT COUNT(*) FROM album_assets aa JOIN assets a ON a.id = aa.asset_id
        WHERE aa.album_id = al.id AND a.trashed_at IS NULL),
       IFNULL((SELECT a.id FROM album_assets aa JOIN assets a ON a.id = aa.asset_id
               WHERE aa.album_id = al.id AND a.trashed_at IS NULL
               ORDER BY IFNULL(a.taken_at, a.imported_at) DESC, a.id DESC LIMIT 1), ''),
       l.kind, IFNULL(l.owner_user_id, ''), l.created_at
FROM albums al
JOIN libraries l ON l.id = al.library_id`

func scanAlbum(row rowScanner) (albumRecord, error) {
	var record albumRecord
	var createdAt, libraryCreatedAt string
	if err := row.Scan(&record.ID, &record.LibraryID, &record.Name, &record.CreatedBy, &createdAt, &record.Photos, &record.CoverID,
		&record.library.kind, &record.library.ownerUserID, &libraryCreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return albumRecord{}, ErrNotFound
		}
		return albumRecord{}, err
	}
	record.library.id = record.LibraryID
	var err error
	if record.CreatedAt, err = parseTime(createdAt); err != nil {
		return albumRecord{}, err
	}
	if record.library.createdAt, err = parseTime(libraryCreatedAt); err != nil {
		return albumRecord{}, err
	}
	return record, nil
}

// Albums lists the albums of a library p can see, newest first.
func (s *Service) Albums(ctx context.Context, p Principal, libraryID string) ([]Album, error) {
	lib, err := s.visibleLibrary(ctx, s.db, p, libraryID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, albumSelect+" WHERE al.library_id = ? ORDER BY al.created_at DESC, al.id DESC", lib.id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	albums := []Album{}
	for rows.Next() {
		record, err := scanAlbum(rows)
		if err != nil {
			return nil, err
		}
		albums = append(albums, record.Album)
	}
	return albums, rows.Err()
}

func (s *Service) CreateAlbum(ctx context.Context, p Principal, libraryID, name string) (Album, error) {
	name, ok := cleanName(name)
	if !ok {
		return Album{}, ErrInvalidName
	}
	var created Album
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		lib, err := s.visibleLibrary(ctx, tx, p, libraryID)
		if err != nil {
			return err
		}
		if !canAdd(p, lib) {
			return ErrForbidden
		}
		id := s.randomID("album")
		if _, err := tx.ExecContext(ctx, "INSERT INTO albums(id, library_id, name, created_by, created_at) VALUES(?, ?, ?, ?, ?)",
			id, lib.id, name, p.UserID, formatTime(s.now())); err != nil {
			return conflictOnUnique(err)
		}
		record, err := scanAlbum(tx.QueryRowContext(ctx, albumSelect+" WHERE al.id = ?", id))
		created = record.Album
		if err != nil {
			return err
		}
		return s.audit(ctx, tx, p.UserID, "photo.album_created", id, name)
	})
	return created, err
}

func (s *Service) RenameAlbum(ctx context.Context, p Principal, albumID, name string) (Album, error) {
	name, ok := cleanName(name)
	if !ok {
		return Album{}, ErrInvalidName
	}
	return s.changeAlbum(ctx, p, albumID, func(tx *sql.Tx, record albumRecord) error {
		if _, err := tx.ExecContext(ctx, "UPDATE albums SET name = ? WHERE id = ?", name, record.ID); err != nil {
			return conflictOnUnique(err)
		}
		return s.audit(ctx, tx, p.UserID, "photo.album_renamed", record.ID, name)
	})
}

// DeleteAlbum removes an album; its photos stay in the library.
func (s *Service) DeleteAlbum(ctx context.Context, p Principal, albumID string) error {
	_, err := s.changeAlbum(ctx, p, albumID, func(tx *sql.Tx, record albumRecord) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM albums WHERE id = ?", record.ID); err != nil {
			return err
		}
		return s.audit(ctx, tx, p.UserID, "photo.album_deleted", record.ID, record.Name)
	})
	return err
}

// AlbumPhotos pages through an album's photos outside the trash, newest
// first.
func (s *Service) AlbumPhotos(ctx context.Context, p Principal, albumID, cursor string, limit int) (Page, error) {
	record, err := s.visibleAlbum(ctx, s.db, p, albumID)
	if err != nil {
		return Page{}, err
	}
	return s.newestFirst(ctx, p, "WHERE a.id IN (SELECT asset_id FROM album_assets WHERE album_id = ?) AND a.trashed_at IS NULL",
		[]any{record.ID}, cursor, limit)
}

// AddToAlbum puts a photo in an album and returns the photo the album now
// holds: the same photo when it is in the album's library, otherwise a copy
// made there.
func (s *Service) AddToAlbum(ctx context.Context, p Principal, albumID, assetID string) (Asset, error) {
	var added assetRecord
	_, err := s.changeAlbum(ctx, p, albumID, func(tx *sql.Tx, album albumRecord) error {
		source, err := s.visibleAsset(ctx, tx, p, assetID)
		if err != nil {
			return err
		}
		added = source
		if source.library.id != album.library.id {
			if err := checkCopySource(p, source); err != nil {
				return err
			}
			if added, err = s.insertCopy(ctx, tx, p, source, album.library, ""); err != nil {
				return err
			}
		} else if source.Trash != nil {
			return ErrConflict
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO album_assets(album_id, asset_id, added_by, added_at) VALUES(?, ?, ?, ?) ON CONFLICT DO NOTHING",
			album.ID, added.ID, p.UserID, formatTime(s.now())); err != nil {
			return err
		}
		return s.audit(ctx, tx, p.UserID, "photo.album_added", album.ID, added.ID)
	})
	if err != nil {
		return Asset{}, err
	}
	return s.details(ctx, p, added)
}

// RemoveFromAlbum takes a photo out of an album without deleting it.
func (s *Service) RemoveFromAlbum(ctx context.Context, p Principal, albumID, assetID string) error {
	_, err := s.changeAlbum(ctx, p, albumID, func(tx *sql.Tx, album albumRecord) error {
		result, err := tx.ExecContext(ctx, "DELETE FROM album_assets WHERE album_id = ? AND asset_id = ?", album.ID, assetID)
		if err != nil {
			return err
		}
		if removed, err := result.RowsAffected(); err != nil || removed == 0 {
			if err == nil {
				err = ErrNotFound
			}
			return err
		}
		return s.audit(ctx, tx, p.UserID, "photo.album_removed", album.ID, assetID)
	})
	return err
}

func (s *Service) visibleAlbum(ctx context.Context, q queryer, p Principal, albumID string) (albumRecord, error) {
	if !p.valid() {
		return albumRecord{}, ErrForbidden
	}
	record, err := scanAlbum(q.QueryRowContext(ctx, albumSelect+" WHERE al.id = ?", albumID))
	if err != nil {
		return albumRecord{}, err
	}
	if !canView(p, record.library, s.now()) {
		return albumRecord{}, ErrNotFound
	}
	return record, nil
}

// changeAlbum runs change, which also writes its audit events, on an album p
// may change, and returns the album as it is afterwards.
func (s *Service) changeAlbum(ctx context.Context, p Principal, albumID string, change func(*sql.Tx, albumRecord) error) (Album, error) {
	var changed Album
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		record, err := s.visibleAlbum(ctx, tx, p, albumID)
		if err != nil {
			return err
		}
		if !canChange(p, record.library, record.CreatedBy) {
			return ErrForbidden
		}
		if err := change(tx, record); err != nil {
			return err
		}
		updated, err := scanAlbum(tx.QueryRowContext(ctx, albumSelect+" WHERE al.id = ?", albumID))
		if errors.Is(err, ErrNotFound) {
			return nil // deleted
		}
		changed = updated.Album
		return err
	})
	return changed, err
}

func (s *Service) albumsOf(ctx context.Context, assetID string) ([]AlbumRef, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT al.id, al.name FROM album_assets aa JOIN albums al ON al.id = aa.album_id WHERE aa.asset_id = ? ORDER BY al.name, al.id",
		assetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var albums []AlbumRef
	for rows.Next() {
		var album AlbumRef
		if err := rows.Scan(&album.ID, &album.Name); err != nil {
			return nil, err
		}
		albums = append(albums, album)
	}
	return albums, rows.Err()
}
