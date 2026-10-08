// Package photos owns the managed photo library (ADR 0006, ADR 0011): the
// Catalog, the immutable content objects behind photo assets, and the Policy
// that decides who may see or change them. Callers identify libraries,
// directories and assets only by stable IDs; object paths and content hashes
// never leave this package.
package photos

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zhongwater123/A-NAS/internal/capacity"
)

var (
	ErrForbidden         = errors.New("photo library access is forbidden")
	ErrNotFound          = errors.New("photo library item not found")
	ErrConflict          = errors.New("photo library item conflicts with its current state")
	ErrInvalidName       = errors.New("invalid photo library name")
	ErrUnsupportedType   = errors.New("unsupported photo type")
	ErrTooLarge          = errors.New("photo exceeds the import size limit")
	ErrInsufficientSpace = errors.New("data volume reserve would be exhausted")
)

const (
	// DefaultTrashRetention is how long trashed photo assets stay restorable.
	DefaultTrashRetention = 15 * 24 * time.Hour
	// DefaultMaxImportBytes caps one original until the product freezes a
	// per-file limit.
	DefaultMaxImportBytes = 256 << 20
	// maxImagePixels rejects decompression bombs before anything decodes the
	// full image.
	maxImagePixels = 1 << 28
)

// Principal is the caller as confirmed by the session lookup. The photo
// service never derives it from request data.
type Principal struct {
	UserID string
	// Username names the caller's private library to other members in
	// cross-member duplicate hints.
	Username string
	Admin    bool
	// Viewing lists the caller's Administrative Viewing Mode grants for
	// members' private libraries; each is read-only and expires on its own.
	Viewing []ViewingGrant
}

type ViewingGrant struct {
	GrantID     string
	OwnerUserID string
	ExpiresAt   time.Time
}

type LibraryKind string

const (
	LibraryKindPrivate LibraryKind = "private"
	LibraryKindShared  LibraryKind = "shared"
)

type Library struct {
	ID          string      `json:"id"`
	Kind        LibraryKind `json:"kind"`
	OwnerUserID string      `json:"ownerUserId,omitempty"`
	OwnerName   string      `json:"ownerName,omitempty"`
	CreatedAt   time.Time   `json:"createdAt"`
	// Viewing is set when the caller sees another member's private library
	// only through Administrative Viewing Mode; access is read-only.
	Viewing *LibraryViewing `json:"viewing,omitempty"`
}

type LibraryViewing struct {
	GrantID   string    `json:"grantId"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type Directory struct {
	ID        string    `json:"id"`
	LibraryID string    `json:"libraryId"`
	ParentID  string    `json:"parentId,omitempty"`
	Name      string    `json:"name"`
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
}

// DuplicateRole marks photo assets in one library whose original bytes are
// identical. The earliest import is the first; the rest are duplicates. Each
// keeps its own identity and lifecycle.
type DuplicateRole string

const (
	DuplicateNone     DuplicateRole = ""
	DuplicateFirst    DuplicateRole = "first"
	DuplicateRepeated DuplicateRole = "duplicate"
)

type Asset struct {
	ID          string    `json:"id"`
	LibraryID   string    `json:"libraryId"`
	DirectoryID string    `json:"directoryId,omitempty"`
	Name        string    `json:"name"`
	MediaType   string    `json:"mediaType"`
	SizeBytes   int64     `json:"sizeBytes"`
	UploadedBy  string    `json:"uploadedBy"`
	ImportedAt  time.Time `json:"importedAt"`
	// TakenAt is the capture time from the original's EXIF, when recorded.
	TakenAt *time.Time `json:"takenAt,omitempty"`
	// Width and Height are the upright display size; zero when unknown.
	Width     int            `json:"width"`
	Height    int            `json:"height"`
	Thumbnail ThumbnailState `json:"thumbnail"`
	Duplicate DuplicateRole  `json:"duplicate,omitempty"`
	// AlsoKeptBy names other members whose private libraries hold the same
	// original. Only the owner of a private library sees it, and it reveals
	// nothing else about those libraries.
	AlsoKeptBy []string    `json:"alsoKeptBy,omitempty"`
	Trash      *TrashState `json:"trash,omitempty"`
}

type TrashState struct {
	TrashedAt  time.Time `json:"trashedAt"`
	TrashedBy  string    `json:"trashedBy"`
	PurgeAfter time.Time `json:"purgeAfter"`
}

// Content is an original or derived file opened read-only; the caller must
// close Reader.
type Content struct {
	Name      string
	MediaType string
	SizeBytes int64
	// ETag is a strong validator: originals are immutable and derived files
	// change only with their derivation version.
	ETag   string
	Reader *os.File
}

type Options struct {
	Now    func() time.Time
	Random io.Reader
	// TrashRetention defaults to DefaultTrashRetention.
	TrashRetention time.Duration
	// MaxImportBytes defaults to DefaultMaxImportBytes.
	MaxImportBytes int64
	// DisableCapacityReserve skips the data-volume reserve check; only tests
	// on ordinary temporary directories may set it.
	DisableCapacityReserve bool
	// Location interprets EXIF capture times recorded without an offset;
	// it defaults to time.Local.
	Location *time.Location
}

// Service is the photo library Module. It must be the only accessor of its
// root directory, which in production is the data volume's photos subvolume
// owned by the photo service identity; the caller proves the volume is
// mounted before calling Open.
type Service struct {
	db             *sql.DB
	root           *os.Root
	rootPath       string
	now            func() time.Time
	random         io.Reader
	trashRetention time.Duration
	maxImportBytes int64
	// admits reports whether incoming bytes still leave the data-volume
	// reserve free.
	admits          func(path string, incoming int64) (bool, error)
	location        *time.Location
	sharedLibraryID string
	// mediaWake tells RunMedia that an import queued work.
	mediaWake chan struct{}

	// commitMu pairs every change to the set of content object files with the
	// catalog transaction that references or forgets them, so a purge never
	// unlinks an object that an import has just chosen to reuse.
	commitMu sync.Mutex
	// staging holds the names of staging files that imports are still writing.
	stagingMu sync.Mutex
	staging   map[string]struct{}
}

const (
	catalogFile = "catalog.db"
	objectsDir  = "objects"
	stagingDir  = "staging"
)

// Open prepares root, migrates its Catalog and ensures the shared library
// exists.
func Open(root string, options Options) (*Service, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("photo library root is empty")
	}
	root = filepath.Clean(root)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create photo library root: %w", err)
	}
	opened, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open photo library root: %w", err)
	}
	for _, dir := range []string{objectsDir, stagingDir, derivedDir} {
		if err := opened.MkdirAll(dir, 0o700); err != nil {
			_ = opened.Close()
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
	}
	db, err := openCatalog(filepath.Join(root, catalogFile))
	if err != nil {
		_ = opened.Close()
		return nil, err
	}
	service := &Service{
		db: db, root: opened, rootPath: root,
		now: options.Now, random: options.Random,
		trashRetention: options.TrashRetention, maxImportBytes: options.MaxImportBytes,
		admits: capacity.Admits, location: options.Location,
		staging: make(map[string]struct{}), mediaWake: make(chan struct{}, 1),
	}
	if options.DisableCapacityReserve {
		service.admits = func(string, int64) (bool, error) { return true, nil }
	}
	if service.now == nil {
		service.now = time.Now
	}
	if service.location == nil {
		service.location = time.Local
	}
	if service.random == nil {
		service.random = rand.Reader
	}
	if service.trashRetention <= 0 {
		service.trashRetention = DefaultTrashRetention
	}
	if service.maxImportBytes <= 0 {
		service.maxImportBytes = DefaultMaxImportBytes
	}
	if err := service.ensureSharedLibrary(); err != nil {
		_ = service.Close()
		return nil, err
	}
	return service, nil
}

func (s *Service) Close() error {
	return errors.Join(s.db.Close(), s.root.Close())
}

// StoreInfo describes the store directory the service holds open, so a
// caller can tell whether the store's path still names it.
func (s *Service) StoreInfo() (os.FileInfo, error) { return s.root.Stat(".") }
