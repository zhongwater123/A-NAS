// Package feedback keeps 问题反馈: one checklist of problems and suggestions
// that every signed-in account can file, read, edit, mark handled, delete and
// comment on. Items, comments and their images live in feedback.db on the
// system disk, beside the Product Service's other state; nothing here touches
// the data volume.
package feedback

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	_ "github.com/mattn/go-sqlite3"
)

const (
	MaxTextRunes  = 5000
	MaxImages     = 6
	MaxImageBytes = 10 << 20
)

var (
	ErrNotFound         = errors.New("feedback not found")
	ErrEmpty            = errors.New("feedback needs text or an image")
	ErrTextTooLong      = errors.New("feedback text is too long")
	ErrTooManyImages    = errors.New("feedback has too many images")
	ErrImageTooLarge    = errors.New("feedback image is too large")
	ErrUnsupportedImage = errors.New("feedback images must be PNG, JPEG, GIF or WebP")
	// ErrChanged means someone else edited the item after the caller read it.
	ErrChanged = errors.New("feedback changed since it was read")
)

// imageTypes are the formats browsers show inline and that cannot carry
// script, so every reader can open what anyone attached.
var imageTypes = []string{"image/png", "image/jpeg", "image/gif", "image/webp"}

// Actor is the signed-in account acting on the list.
type Actor struct {
	ID       string
	Username string
}

type Item struct {
	ID     string  `json:"id"`
	Text   string  `json:"text"`
	Images []Image `json:"images"`
	// Version counts edits to the text and images, so an edit made from a
	// stale copy is refused instead of overwriting someone else's.
	Version   int64     `json:"version"`
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	// UpdatedBy and UpdatedAt record the latest edit to the text or images.
	UpdatedBy  string     `json:"updatedBy,omitempty"`
	UpdatedAt  *time.Time `json:"updatedAt,omitempty"`
	Resolved   bool       `json:"resolved"`
	ResolvedBy string     `json:"resolvedBy,omitempty"`
	ResolvedAt *time.Time `json:"resolvedAt,omitempty"`
	// Comments are the replies under the item, oldest first.
	Comments []Comment `json:"comments"`
}

type Image struct {
	ID          string `json:"id"`
	ContentType string `json:"contentType"`
	SizeBytes   int64  `json:"sizeBytes"`
}

// Content is what a person writes. Keep lists the attached images that stay
// when editing; New holds the images added.
type Content struct {
	Text string
	Keep []string
	New  [][]byte
}

type Options struct {
	Now    func() time.Time
	Random io.Reader
}

type Store struct {
	db     *sql.DB
	now    func() time.Time
	random io.Reader
}

func Open(path string, options Options) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("SQLite path is empty")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	// Images are stored inline, so deleting them should return the space.
	db, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_busy_timeout=5000&_journal_mode=WAL&_auto_vacuum=full")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, now: options.Now, random: options.Random}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS items (
    id TEXT PRIMARY KEY,
    text TEXT NOT NULL,
    version INTEGER NOT NULL DEFAULT 1,
    created_by_id TEXT NOT NULL,
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_by TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL DEFAULT '',
    resolved_by TEXT NOT NULL DEFAULT '',
    resolved_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS images (
    id TEXT PRIMARY KEY,
    item_id TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    content_type TEXT NOT NULL,
    size_bytes INTEGER NOT NULL,
    data BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS images_item ON images(item_id, position);
-- Comments came later in their own tables, so an older release ignores them
-- rather than mistaking comment images for the item's own.
CREATE TABLE IF NOT EXISTS comments (
    id TEXT PRIMARY KEY,
    item_id TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    text TEXT NOT NULL,
    version INTEGER NOT NULL DEFAULT 1,
    created_by_id TEXT NOT NULL,
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_by TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS comments_item ON comments(item_id, created_at);
CREATE TABLE IF NOT EXISTS comment_images (
    id TEXT PRIMARY KEY,
    comment_id TEXT NOT NULL REFERENCES comments(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    content_type TEXT NOT NULL,
    size_bytes INTEGER NOT NULL,
    data BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS comment_images_comment ON comment_images(comment_id, position);
`
	_, err := s.db.ExecContext(ctx, schema)
	return err
}

// List returns every item, newest first, with its comments and without image
// content.
func (s *Store) List(ctx context.Context) ([]Item, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, text, version, created_by, created_at, updated_by, updated_at, resolved_by, resolved_at
FROM items ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Item{}
	byID := map[string]int{}
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		byID[item.ID] = len(items)
		items = append(items, item)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	images, err := s.db.QueryContext(ctx, `SELECT item_id, id, content_type, size_bytes FROM images ORDER BY item_id, position`)
	if err != nil {
		return nil, err
	}
	defer images.Close()
	for images.Next() {
		var itemID string
		var image Image
		if err := images.Scan(&itemID, &image.ID, &image.ContentType, &image.SizeBytes); err != nil {
			return nil, err
		}
		if index, ok := byID[itemID]; ok {
			items[index].Images = append(items[index].Images, image)
		}
	}
	if err := errors.Join(images.Err(), images.Close()); err != nil {
		return nil, err
	}
	comments, err := s.listComments(ctx, s.db, "")
	if err != nil {
		return nil, err
	}
	for _, comment := range comments {
		if index, ok := byID[comment.itemID]; ok {
			items[index].Comments = append(items[index].Comments, comment.Comment)
		}
	}
	return items, nil
}

func (s *Store) Get(ctx context.Context, id string) (Item, error) {
	return s.get(ctx, s.db, id)
}

func (s *Store) Create(ctx context.Context, actor Actor, content Content) (Item, error) {
	text, types, err := validate(content.Text, 0, content.New, itemLimits)
	if err != nil {
		return Item{}, err
	}
	id := s.randomID("feedback")
	now := formatTime(s.now())
	err = s.inTransaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO items (id, text, created_by_id, created_by, created_at) VALUES (?, ?, ?, ?, ?)`,
			id, text, actor.ID, actor.Username, now); err != nil {
			return err
		}
		return s.addImages(ctx, tx, insertItemImage, id, 0, content.New, types)
	})
	if err != nil {
		return Item{}, err
	}
	return s.Get(ctx, id)
}

// Edit replaces the text, keeps the listed images and adds new ones, as long
// as nobody edited the item since the caller read version.
func (s *Store) Edit(ctx context.Context, actor Actor, id string, version int64, content Content) (Item, error) {
	err := s.inTransaction(ctx, func(tx *sql.Tx) error {
		current, err := s.get(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.Version != version {
			return ErrChanged
		}
		kept, removed := keepImages(current.Images, content.Keep)
		text, types, err := validate(content.Text, kept, content.New, itemLimits)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE items SET text = ?, version = version + 1, updated_by = ?, updated_at = ? WHERE id = ?`,
			text, actor.Username, formatTime(s.now()), id); err != nil {
			return err
		}
		for _, imageID := range removed {
			if _, err := tx.ExecContext(ctx, `DELETE FROM images WHERE id = ?`, imageID); err != nil {
				return err
			}
		}
		var next int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), -1) + 1 FROM images WHERE item_id = ?`, id).Scan(&next); err != nil {
			return err
		}
		return s.addImages(ctx, tx, insertItemImage, id, next, content.New, types)
	})
	if err != nil {
		return Item{}, err
	}
	return s.Get(ctx, id)
}

// SetResolved marks the item handled or open again. It is not an edit: it
// leaves the version alone so it never invalidates someone's open editor.
func (s *Store) SetResolved(ctx context.Context, actor Actor, id string, resolved bool) (Item, error) {
	by, at := "", ""
	if resolved {
		by, at = actor.Username, formatTime(s.now())
	}
	// Marking an already handled item again keeps who handled it first.
	if _, err := s.db.ExecContext(ctx, `UPDATE items SET resolved_by = ?, resolved_at = ? WHERE id = ? AND (resolved_at = '') = ?`, by, at, id, resolved); err != nil {
		return Item{}, err
	}
	return s.Get(ctx, id)
}

func (s *Store) Delete(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM items WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed == 0 {
		return ErrNotFound
	}
	return nil
}

// ImageContent returns one image of an item or of a comment under it, with
// its bytes.
func (s *Store) ImageContent(ctx context.Context, itemID, imageID string) (Image, []byte, error) {
	image := Image{ID: imageID}
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT content_type, size_bytes, data FROM images WHERE id = ? AND item_id = ?
UNION ALL
SELECT ci.content_type, ci.size_bytes, ci.data FROM comment_images ci JOIN comments c ON c.id = ci.comment_id WHERE ci.id = ? AND c.item_id = ?
LIMIT 1`, imageID, itemID, imageID, itemID).
		Scan(&image.ContentType, &image.SizeBytes, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return Image{}, nil, ErrNotFound
	}
	return image, data, err
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *Store) get(ctx context.Context, q queryer, id string) (Item, error) {
	item, err := scanItem(q.QueryRowContext(ctx, `SELECT id, text, version, created_by, created_at, updated_by, updated_at, resolved_by, resolved_at
FROM items WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Item{}, ErrNotFound
	}
	if err != nil {
		return Item{}, err
	}
	rows, err := q.QueryContext(ctx, `SELECT id, content_type, size_bytes FROM images WHERE item_id = ? ORDER BY position`, id)
	if err != nil {
		return Item{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var image Image
		if err := rows.Scan(&image.ID, &image.ContentType, &image.SizeBytes); err != nil {
			return Item{}, err
		}
		item.Images = append(item.Images, image)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return Item{}, err
	}
	comments, err := s.listComments(ctx, q, id)
	if err != nil {
		return Item{}, err
	}
	for _, comment := range comments {
		item.Comments = append(item.Comments, comment.Comment)
	}
	return item, nil
}

func scanItem(row interface{ Scan(...any) error }) (Item, error) {
	item := Item{Images: []Image{}, Comments: []Comment{}}
	var createdAt, updatedAt, resolvedAt string
	if err := row.Scan(&item.ID, &item.Text, &item.Version, &item.CreatedBy, &createdAt, &item.UpdatedBy, &updatedAt, &item.ResolvedBy, &resolvedAt); err != nil {
		return Item{}, err
	}
	item.CreatedAt = parseTime(createdAt)
	if updatedAt != "" {
		at := parseTime(updatedAt)
		item.UpdatedAt = &at
	}
	if resolvedAt != "" {
		at := parseTime(resolvedAt)
		item.Resolved, item.ResolvedAt = true, &at
	}
	return item, nil
}

const (
	insertItemImage    = `INSERT INTO images (id, item_id, position, content_type, size_bytes, data) VALUES (?, ?, ?, ?, ?, ?)`
	insertCommentImage = `INSERT INTO comment_images (id, comment_id, position, content_type, size_bytes, data) VALUES (?, ?, ?, ?, ?, ?)`
)

// addImages stores new images for an item or a comment; insert names the table.
func (s *Store) addImages(ctx context.Context, tx *sql.Tx, insert, ownerID string, position int, images [][]byte, types []string) error {
	for index, data := range images {
		if _, err := tx.ExecContext(ctx, insert, s.randomID("feedback-image"), ownerID, position+index, types[index], len(data), data); err != nil {
			return err
		}
	}
	return nil
}

// keepImages counts the attached images an edit keeps and lists the others.
func keepImages(current []Image, keep []string) (int, []string) {
	var removed []string
	kept := 0
	for _, image := range current {
		if slices.Contains(keep, image.ID) {
			kept++
		} else {
			removed = append(removed, image.ID)
		}
	}
	return kept, removed
}

// limits bound what one item or one comment may hold.
type limits struct{ text, images int }

var (
	itemLimits    = limits{text: MaxTextRunes, images: MaxImages}
	commentLimits = limits{text: MaxCommentRunes, images: MaxCommentImages}
)

// validate normalizes the text and checks the images, returning each new
// image's sniffed type.
func validate(text string, kept int, images [][]byte, limit limits) (string, []string, error) {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) > limit.text {
		return "", nil, ErrTextTooLong
	}
	if kept+len(images) > limit.images {
		return "", nil, ErrTooManyImages
	}
	if text == "" && kept+len(images) == 0 {
		return "", nil, ErrEmpty
	}
	types := make([]string, len(images))
	for index, data := range images {
		if len(data) > MaxImageBytes {
			return "", nil, ErrImageTooLarge
		}
		types[index] = http.DetectContentType(data)
		if !slices.Contains(imageTypes, types[index]) {
			return "", nil, ErrUnsupportedImage
		}
	}
	return text, types, nil
}

func (s *Store) inTransaction(ctx context.Context, apply func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := apply(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) randomID(prefix string) string {
	buffer := make([]byte, 12)
	if _, err := io.ReadFull(s.random, buffer); err != nil {
		panic(err)
	}
	return prefix + ":" + hex.EncodeToString(buffer)
}

// Times are stored at a fixed width so that they sort as text.
func formatTime(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
}

func parseTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}
