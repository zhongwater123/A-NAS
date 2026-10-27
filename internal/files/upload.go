package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// ConflictPolicy decides what an upload does when its name is already taken
// in the target folder.
type ConflictPolicy string

const (
	// ConflictAsk refuses an upload whose name is taken before any byte is
	// sent, so the user can choose. A name taken while the bytes arrive keeps
	// both files instead of failing the finished upload.
	ConflictAsk ConflictPolicy = "ask"
	// ConflictRename keeps both: the upload takes the first free "name (n)".
	ConflictRename ConflictPolicy = "rename"
	// ConflictReplace moves the existing file to the trash, where it can be
	// restored, and puts the upload in its place. A folder is never replaced.
	ConflictReplace ConflictPolicy = "replace"
)

var (
	ErrInvalidUpload = errors.New("upload request is invalid")
	// ErrFolderBlocked reports a folder upload whose folder name is taken by a file.
	ErrFolderBlocked = errors.New("a file has the name of a folder to create")
	// ErrUploadOffset reports a part that does not continue where the upload stands.
	ErrUploadOffset = errors.New("upload part does not continue the upload")
	// ErrUploadIncomplete reports a commit before every byte arrived.
	ErrUploadIncomplete = errors.New("upload is incomplete")
)

const (
	uploadTemporaryPrefix = ".a-nas-upload-"
	// maxNameBytes is the Linux limit for one path component.
	maxNameBytes = 255
	// maxUploadDirectories bounds how deep a folder upload may nest.
	maxUploadDirectories = 32
	// maxNumberedCopies bounds the "name (n)" search when keeping both files.
	maxNumberedCopies = 999
	// Uploads hold their temporaries while they run, so a temporary nobody
	// holds is left over from a restart or a crash. staleUploadAge only
	// spares the moment between creating a temporary and holding it.
	staleUploadAge = 10 * time.Minute
)

// UploadRequest describes a file whose bytes arrive in parts.
type UploadRequest struct {
	SpaceID  string
	ParentID string
	// Directories are folders created below the parent as needed, so a folder
	// upload keeps its structure; folders that exist are reused.
	Directories []string
	Name        string
	Size        int64
	Conflict    ConflictPolicy
}

// PendingUpload is a file whose bytes are still arriving. It keeps open the
// temporary file that the File Broker created as the user, so each part is
// written without another file operation (ADR 0019). Calls on one upload
// must not overlap; Offset may be read at any time.
type PendingUpload struct {
	SpaceID string
	// ParentID is the folder the file goes to, after Directories were created.
	ParentID string
	Name     string
	Size     int64
	Conflict ConflictPolicy

	file *os.File
	// modTime, when set, is the modification time the file is saved with.
	modTime time.Time
	// temporary is the temporary's name in the parent folder; path is where
	// it was when the upload began, which a moved folder makes stale.
	temporary string
	path      string
	written   atomic.Int64
}

// Offset is how many bytes have been written.
func (u *PendingUpload) Offset() int64 { return u.written.Load() }

// CommittedUpload is an upload saved in its folder.
type CommittedUpload struct {
	Entry Entry
	// Replaced is set when an existing file went to the trash to make room.
	Replaced bool
}

// BeginUpload checks that the file may be written and fits on the volume,
// creates any folders of a folder upload and opens the temporary that the
// parts are written to. With ConflictAsk a taken name fails at once with
// ErrConflict, before the client sends anything.
func (s *Service) BeginUpload(ctx context.Context, actor accounts.User, request UploadRequest) (*PendingUpload, error) {
	switch request.Conflict {
	case ConflictAsk, ConflictRename, ConflictReplace:
	default:
		return nil, ErrInvalidUpload
	}
	if request.Size < 0 || len(request.Directories) > maxUploadDirectories {
		return nil, ErrInvalidUpload
	}
	if err := checkNewName(request.Name); err != nil {
		return nil, err
	}
	space, root, err := s.authorizedSpace(ctx, actor, request.SpaceID, true)
	if err != nil {
		return nil, err
	}
	parentID := request.ParentID
	parentPath, err := s.parentPath(ctx, root, space.ID, parentID)
	if err != nil {
		return nil, err
	}
	for _, name := range request.Directories {
		folder, folderPath, err := s.ensureDirectory(ctx, actor, space.ID, root, parentID, parentPath, name)
		if err != nil {
			return nil, err
		}
		parentID, parentPath = folder.ID, folderPath
	}
	// The whole file must fit before any of it is sent; each part is checked
	// again because other writers share the volume.
	if err := s.ensureCapacity(request.Size); err != nil {
		return nil, err
	}
	target := filepath.Join(parentPath, request.Name)
	if isAgentPolicyDirectory(root, space.ID, target) {
		return nil, ErrForbidden
	}
	if !withinRoot(root, target) {
		return nil, ErrInvalidName
	}
	if request.Conflict == ConflictAsk {
		if _, err := s.fs.Lstat(ctx, target); err == nil {
			return nil, ErrConflict
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		if err := s.refuseCaseTwin(ctx, space.ID, parentID, request.Name, ""); err != nil {
			return nil, err
		}
	}
	file, temporaryPath, err := s.fs.CreateTemp(ctx, parentPath, uploadTemporaryPrefix)
	if err != nil {
		return nil, err
	}
	upload := &PendingUpload{
		SpaceID: space.ID, ParentID: parentID, Name: request.Name, Size: request.Size, Conflict: request.Conflict,
		file: file, temporary: filepath.Base(temporaryPath), path: temporaryPath,
	}
	s.holdTemporary(upload.temporary, true)
	return upload, nil
}

// WriteUpload writes a part that starts at offset, which must be where the
// upload stands. It reports the bytes written even when the part ends early,
// for example because the connection dropped, so the client resumes there.
// Bytes beyond the declared size are not read.
func (s *Service) WriteUpload(ctx context.Context, upload *PendingUpload, offset int64, part io.Reader) (int64, error) {
	if upload.file == nil {
		return 0, ErrUploadIncomplete
	}
	if offset != upload.written.Load() {
		return 0, ErrUploadOffset
	}
	buffer := make([]byte, 1024*1024)
	var total int64
	for {
		position := upload.written.Load()
		remaining := upload.Size - position
		if remaining <= 0 {
			return total, nil
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
		count, readErr := part.Read(buffer[:min(int64(len(buffer)), remaining)])
		if count > 0 {
			// Bytes already written show in the free space, so only this
			// chunk is checked against the reserve.
			if err := s.ensureCapacity(int64(count)); err != nil {
				return total, err
			}
			written, err := upload.file.WriteAt(buffer[:count], position)
			total += int64(written)
			upload.written.Add(int64(written))
			if err != nil {
				return total, uploadWriteError(err)
			}
		}
		if errors.Is(readErr, io.EOF) {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}

// CommitUpload saves a complete upload under its name, applying its conflict
// policy. The temporary sits in the target folder, so a folder moved while
// the bytes arrived takes the upload along.
func (s *Service) CommitUpload(ctx context.Context, actor accounts.User, upload *PendingUpload) (CommittedUpload, error) {
	if upload.written.Load() != upload.Size {
		return CommittedUpload{}, ErrUploadIncomplete
	}
	space, root, err := s.authorizedSpace(ctx, actor, upload.SpaceID, true)
	if err != nil {
		return CommittedUpload{}, err
	}
	if upload.file != nil {
		// Sync before the rename so that a crash cannot show a file with
		// missing data under its real name.
		if err := upload.file.Sync(); err != nil {
			return CommittedUpload{}, uploadWriteError(err)
		}
		if err := upload.file.Close(); err != nil {
			return CommittedUpload{}, uploadWriteError(err)
		}
		upload.file = nil
	}
	parentPath, err := s.uploadParent(ctx, space, root, upload)
	if err != nil {
		return CommittedUpload{}, err
	}
	if isAgentPolicyDirectory(root, space.ID, filepath.Join(parentPath, upload.Name)) {
		return CommittedUpload{}, ErrForbidden
	}
	done, err := s.change(ctx, space.ID)
	if err != nil {
		return CommittedUpload{}, err
	}
	defer done()
	temporary := filepath.Join(parentPath, upload.temporary)
	s.keepModTime(ctx, temporary, upload.modTime)
	replaced := false
	if upload.Conflict == ConflictReplace {
		existing := upload.Name
		if twin, found, err := s.caseTwin(ctx, space.ID, upload.ParentID, upload.Name, ""); err != nil {
			return CommittedUpload{}, err
		} else if found && twin.Kind == EntryKindFile {
			existing = twin.Name
		}
		if replaced, err = s.trashForReplacement(ctx, actor, space.ID, root, upload.ParentID, filepath.Join(parentPath, existing)); err != nil {
			return CommittedUpload{}, err
		}
	}
	// Renames never replace, so a name taken in the meantime keeps both.
	var target string
	for number := 0; ; number++ {
		if number > maxNumberedCopies {
			return CommittedUpload{}, ErrConflict
		}
		name := numberedName(upload.Name, number)
		if _, taken, err := s.caseTwin(ctx, space.ID, upload.ParentID, name, ""); err != nil {
			return CommittedUpload{}, err
		} else if taken {
			continue
		}
		target = filepath.Join(parentPath, name)
		err = s.fs.Rename(ctx, temporary, target)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) {
			return CommittedUpload{}, err
		}
	}
	s.holdTemporary(upload.temporary, false)
	entry, err := s.registerCreated(ctx, space.ID, upload.ParentID, root, target)
	if err != nil {
		return CommittedUpload{}, err
	}
	detail := entry.Name
	if replaced {
		detail += " (replaced)"
	}
	_ = s.audit(ctx, actor.ID, "file.uploaded", space.ID, entry.ID, detail)
	return CommittedUpload{Entry: entry, Replaced: replaced}, nil
}

// AbortUpload discards an upload. Its space is released at once; the
// temporary itself is removed as the actor, and when that is impossible, for
// example because no session is left, reconciliation removes it later.
func (s *Service) AbortUpload(ctx context.Context, actor accounts.User, upload *PendingUpload) error {
	if upload.file != nil {
		_ = upload.file.Truncate(0)
		_ = upload.file.Close()
		upload.file = nil
	}
	defer s.holdTemporary(upload.temporary, false)
	temporary := upload.path
	if space, root, err := s.authorizedSpace(ctx, actor, upload.SpaceID, true); err == nil {
		if parentPath, err := s.parentPath(ctx, root, space.ID, upload.ParentID); err == nil {
			temporary = filepath.Join(parentPath, upload.temporary)
		}
	}
	if err := s.fs.Remove(ctx, temporary); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove upload temporary: %w", err)
	}
	return nil
}

// uploadParent finds the upload's folder, reconciling once when the folder
// was moved outside A-NAS (for example over SMB) and the catalog is behind.
func (s *Service) uploadParent(ctx context.Context, space accounts.Space, root string, upload *PendingUpload) (string, error) {
	parentPath, err := s.parentPath(ctx, root, space.ID, upload.ParentID)
	if err == nil {
		if _, statErr := s.fs.Lstat(ctx, filepath.Join(parentPath, upload.temporary)); statErr == nil {
			return parentPath, nil
		}
	}
	if err := s.reconcileNow(ctx, space, root); err != nil {
		return "", err
	}
	parentPath, err = s.parentPath(ctx, root, space.ID, upload.ParentID)
	if err != nil {
		return "", err
	}
	if _, err := s.fs.Lstat(ctx, filepath.Join(parentPath, upload.temporary)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", ErrNotFound
		}
		return "", err
	}
	return parentPath, nil
}

// ensureDirectory returns the folder name below parentPath, creating it when
// it does not exist. A file with that name blocks it.
func (s *Service) ensureDirectory(ctx context.Context, actor accounts.User, spaceID, root, parentID, parentPath, name string) (Entry, string, error) {
	if err := checkNewName(name); err != nil {
		return Entry{}, "", err
	}
	done, err := s.change(ctx, spaceID)
	if err != nil {
		return Entry{}, "", err
	}
	defer done()
	// A folder already there in another letter case is the same folder to an
	// SMB client, so the upload goes into it.
	if twin, found, err := s.caseTwin(ctx, spaceID, parentID, name, ""); err != nil {
		return Entry{}, "", err
	} else if found && twin.Kind == EntryKindDirectory {
		name = twin.Name
	}
	target := filepath.Join(parentPath, name)
	if !withinRoot(root, target) {
		return Entry{}, "", ErrInvalidName
	}
	if isAgentPolicyDirectory(root, spaceID, target) {
		// Folder uploads may traverse an existing managed root, but may
		// never create a replacement at a reserved name.
		info, err := s.fs.Lstat(ctx, target)
		if err != nil || info.Kind != KindDirectory {
			return Entry{}, "", ErrForbidden
		}
		entry, err := s.registerCreated(ctx, spaceID, parentID, root, target)
		return entry, target, err
	}
	created := true
	if err := s.fs.Mkdir(ctx, target); errors.Is(err, fs.ErrExist) {
		// Another upload of the same folder may have just created it.
		created = false
	} else if err != nil {
		return Entry{}, "", err
	}
	info, err := s.fs.Lstat(ctx, target)
	if err != nil {
		return Entry{}, "", err
	}
	if info.Kind != KindDirectory {
		return Entry{}, "", ErrFolderBlocked
	}
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return Entry{}, "", ErrInvalidName
	}
	if created {
		if err := newInode(ctx, s.store.db, spaceID, info.Inode); err != nil {
			return Entry{}, "", err
		}
	}
	entry, err := s.registerInfo(ctx, s.store.db, root, spaceID, parentID, filepath.ToSlash(relative), info)
	if err != nil {
		return Entry{}, "", err
	}
	if created {
		_ = s.audit(ctx, actor.ID, "file.directory_created", spaceID, entry.ID, entry.Name)
	}
	return entry, target, nil
}

// trashForReplacement moves the file at target to the actor's trash so an
// upload can take its place. A folder is kept, and the upload goes beside it.
// The caller's change must be in flight.
func (s *Service) trashForReplacement(ctx context.Context, actor accounts.User, spaceID, root, parentID, target string) (bool, error) {
	info, err := s.fs.Lstat(ctx, target)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Kind != KindFile {
		return false, nil
	}
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return false, ErrInvalidName
	}
	existing, err := s.registerInfo(ctx, s.store.db, root, spaceID, parentID, filepath.ToSlash(relative), info)
	if err != nil {
		return false, err
	}
	if _, err := s.Delete(ctx, actor, spaceID, existing.ID); err != nil {
		return false, err
	}
	return true, nil
}

// holdTemporary marks a temporary as owned by a pending upload or a copy in
// progress, so that reconciliation never removes it however long the upload
// is paused or the copy runs.
func (s *Service) holdTemporary(temporary string, held bool) {
	s.temporariesMu.Lock()
	defer s.temporariesMu.Unlock()
	if held {
		s.temporaries[temporary] = struct{}{}
	} else {
		delete(s.temporaries, temporary)
	}
}

func (s *Service) temporaryHeld(temporary string) bool {
	s.temporariesMu.Lock()
	defer s.temporariesMu.Unlock()
	_, held := s.temporaries[temporary]
	return held
}

// removeStaleUploads deletes upload temporaries that no upload holds, left
// over from a restart or a crash. Temporaries the actor may not delete are
// left for someone who may.
func (s *Service) removeStaleUploads(ctx context.Context, root string, temporaries []ScanEntry) {
	cutoff := s.now().Add(-staleUploadAge)
	for _, item := range temporaries {
		if item.Info.Kind != KindFile || item.Info.ModTime.After(cutoff) || s.temporaryHeld(item.Info.Name) {
			continue
		}
		_ = s.fs.Remove(ctx, filepath.Join(root, filepath.FromSlash(item.Path)))
	}
}

func isUploadTemporary(name string) bool {
	return strings.HasPrefix(path.Base(name), uploadTemporaryPrefix)
}

// numberedName is name itself for 0 and "name (n).ext" after that.
func numberedName(name string, number int) string {
	if number == 0 {
		return name
	}
	return withSuffix(name, fmt.Sprintf(" (%d)", number), true)
}

// withSuffix puts suffix at the end of name, before its extension when
// extension is set. The stem is shortened, at a character boundary, when the
// suffix would take the name past the byte limit.
func withSuffix(name, suffix string, extension bool) string {
	stem, tail := name, ""
	if dot := strings.LastIndex(name, "."); extension && dot > 0 {
		stem, tail = name[:dot], name[dot:]
	}
	for len(stem)+len(suffix)+len(tail) > maxNameBytes && stem != "" {
		_, size := utf8.DecodeLastRuneInString(stem)
		stem = stem[:len(stem)-size]
	}
	return stem + suffix + tail
}

// uploadWriteError names the volume conditions a client can act on.
func uploadWriteError(err error) error {
	switch {
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT):
		return ErrInsufficientSpace
	case errors.Is(err, syscall.EROFS), errors.Is(err, syscall.EIO):
		return ErrVolumeUnavailable
	}
	return err
}
