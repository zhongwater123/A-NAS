package feedback

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const (
	MaxCommentRunes  = 2000
	MaxCommentImages = 3
)

var (
	ErrCommentNotFound = errors.New("feedback comment not found")
	// ErrCommentChanged means someone else edited the comment after the
	// caller read it.
	ErrCommentChanged = errors.New("feedback comment changed since it was read")
)

// Comment is one reply under an item. Like items, any account may edit or
// delete any comment; the latest editor is recorded.
type Comment struct {
	ID        string     `json:"id"`
	Text      string     `json:"text"`
	Images    []Image    `json:"images"`
	Version   int64      `json:"version"`
	CreatedBy string     `json:"createdBy"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedBy string     `json:"updatedBy,omitempty"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
}

// AddComment replies under an item and returns the item with its comments.
func (s *Store) AddComment(ctx context.Context, actor Actor, itemID string, content Content) (Item, error) {
	text, types, err := validate(content.Text, 0, content.New, commentLimits)
	if err != nil {
		return Item{}, err
	}
	err = s.inTransaction(ctx, func(tx *sql.Tx) error {
		if err := itemExists(ctx, tx, itemID); err != nil {
			return err
		}
		id := s.randomID("feedback-comment")
		if _, err := tx.ExecContext(ctx, `INSERT INTO comments (id, item_id, text, created_by_id, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			id, itemID, text, actor.ID, actor.Username, formatTime(s.now())); err != nil {
			return err
		}
		return s.addImages(ctx, tx, insertCommentImage, id, 0, content.New, types)
	})
	if err != nil {
		return Item{}, err
	}
	return s.Get(ctx, itemID)
}

// EditComment replaces a comment's text, keeps the listed images and adds new
// ones, as long as nobody edited the comment since the caller read version.
func (s *Store) EditComment(ctx context.Context, actor Actor, itemID, commentID string, version int64, content Content) (Item, error) {
	err := s.inTransaction(ctx, func(tx *sql.Tx) error {
		if err := itemExists(ctx, tx, itemID); err != nil {
			return err
		}
		comments, err := s.listComments(ctx, tx, itemID)
		if err != nil {
			return err
		}
		var current *Comment
		for index := range comments {
			if comments[index].ID == commentID {
				current = &comments[index].Comment
			}
		}
		if current == nil {
			return ErrCommentNotFound
		}
		if current.Version != version {
			return ErrCommentChanged
		}
		kept, removed := keepImages(current.Images, content.Keep)
		text, types, err := validate(content.Text, kept, content.New, commentLimits)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE comments SET text = ?, version = version + 1, updated_by = ?, updated_at = ? WHERE id = ?`,
			text, actor.Username, formatTime(s.now()), commentID); err != nil {
			return err
		}
		for _, imageID := range removed {
			if _, err := tx.ExecContext(ctx, `DELETE FROM comment_images WHERE id = ?`, imageID); err != nil {
				return err
			}
		}
		var next int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), -1) + 1 FROM comment_images WHERE comment_id = ?`, commentID).Scan(&next); err != nil {
			return err
		}
		return s.addImages(ctx, tx, insertCommentImage, commentID, next, content.New, types)
	})
	if err != nil {
		return Item{}, err
	}
	return s.Get(ctx, itemID)
}

// DeleteComment removes a comment and its images and returns the item.
func (s *Store) DeleteComment(ctx context.Context, itemID, commentID string) (Item, error) {
	err := s.inTransaction(ctx, func(tx *sql.Tx) error {
		if err := itemExists(ctx, tx, itemID); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM comments WHERE id = ? AND item_id = ?`, commentID, itemID)
		if err != nil {
			return err
		}
		if changed, err := result.RowsAffected(); err != nil {
			return err
		} else if changed == 0 {
			return ErrCommentNotFound
		}
		return nil
	})
	if err != nil {
		return Item{}, err
	}
	return s.Get(ctx, itemID)
}

func itemExists(ctx context.Context, tx *sql.Tx, itemID string) error {
	var found int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM items WHERE id = ?`, itemID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

type itemComment struct {
	Comment
	itemID string
}

// listComments returns the comments of one item, or of every item when
// itemID is empty, oldest first and with their images.
func (s *Store) listComments(ctx context.Context, q queryer, itemID string) ([]itemComment, error) {
	rows, err := q.QueryContext(ctx, `SELECT item_id, id, text, version, created_by, created_at, updated_by, updated_at
FROM comments WHERE ? = '' OR item_id = ? ORDER BY created_at, id`, itemID, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	comments := []itemComment{}
	byID := map[string]int{}
	for rows.Next() {
		comment := itemComment{Images: []Image{}}
		var createdAt, updatedAt string
		if err := rows.Scan(&comment.itemID, &comment.ID, &comment.Text, &comment.Version, &comment.CreatedBy, &createdAt, &comment.UpdatedBy, &updatedAt); err != nil {
			return nil, err
		}
		comment.CreatedAt = parseTime(createdAt)
		if updatedAt != "" {
			at := parseTime(updatedAt)
			comment.UpdatedAt = &at
		}
		byID[comment.ID] = len(comments)
		comments = append(comments, comment)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	if len(comments) == 0 {
		return comments, nil
	}
	images, err := q.QueryContext(ctx, `SELECT ci.comment_id, ci.id, ci.content_type, ci.size_bytes
FROM comment_images ci JOIN comments c ON c.id = ci.comment_id
WHERE ? = '' OR c.item_id = ? ORDER BY ci.comment_id, ci.position`, itemID, itemID)
	if err != nil {
		return nil, err
	}
	defer images.Close()
	for images.Next() {
		var commentID string
		var image Image
		if err := images.Scan(&commentID, &image.ID, &image.ContentType, &image.SizeBytes); err != nil {
			return nil, err
		}
		if index, ok := byID[commentID]; ok {
			comments[index].Images = append(comments[index].Images, image)
		}
	}
	return comments, images.Err()
}
