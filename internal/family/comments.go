package family

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const maxCommentRunes = 1000

// Comment is a message under a record, or on the channel's message wall
// when RecordID is empty.
type Comment struct {
	ID         string    `json:"id"`
	RecordID   string    `json:"recordId,omitempty"`
	Text       string    `json:"text"`
	AuthorID   string    `json:"authorId"`
	AuthorName string    `json:"authorName"`
	CreatedAt  time.Time `json:"createdAt"`
}

// CommentInput is a new comment. A request repeated with the same
// RequestKey returns the first comment.
type CommentInput struct {
	RequestKey string `json:"requestKey"`
	Text       string `json:"text"`
}

const commentColumns = "id, COALESCE(record_id, ''), text, author_id, author_name, created_at"

func scanComment(scanner interface{ Scan(...any) error }) (Comment, error) {
	var comment Comment
	var created string
	if err := scanner.Scan(&comment.ID, &comment.RecordID, &comment.Text, &comment.AuthorID, &comment.AuthorName, &created); err != nil {
		return Comment{}, err
	}
	comment.CreatedAt = parseTime(created)
	return comment, nil
}

// commentsFor returns the comments under rows' records, oldest first.
func (s *Store) commentsFor(ctx context.Context, q queryer, rows []*row) (map[string][]Comment, error) {
	found := map[string][]Comment{}
	if len(rows) == 0 {
		return found, nil
	}
	ids := make([]any, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.record.ID)
	}
	result, err := q.QueryContext(ctx, "SELECT "+commentColumns+" FROM comments WHERE record_id IN ("+placeholders(len(ids))+
		") ORDER BY created_at, id", ids...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = result.Close() }()
	for result.Next() {
		comment, err := scanComment(result)
		if err != nil {
			return nil, err
		}
		found[comment.RecordID] = append(found[comment.RecordID], comment)
	}
	return found, result.Err()
}

func cleanComment(input CommentInput) (CommentInput, bool) {
	text := strings.TrimSpace(input.Text)
	if text == "" || !utf8.ValidString(text) || utf8.RuneCountInString(text) > maxCommentRunes || strings.ContainsRune(text, 0) {
		return input, false
	}
	if len(input.RequestKey) > maxRequestKey || !utf8.ValidString(input.RequestKey) {
		return input, false
	}
	input.Text = text
	return input, true
}

// Comment adds a comment under a record.
func (s *Store) Comment(ctx context.Context, actor Actor, recordID string, input CommentInput) (Comment, error) {
	input, ok := cleanComment(input)
	if !ok {
		return Comment{}, ErrInvalid
	}
	var comment Comment
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if _, _, err := s.requireMember(ctx, tx, actor); err != nil {
			return err
		}
		var babyID string
		err := tx.QueryRowContext(ctx, `SELECT r.baby_id FROM records r JOIN babies b ON b.id = r.baby_id
WHERE r.id = ? AND r.deleted_at IS NULL AND b.deleted_at IS NULL`, recordID).Scan(&babyID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		comment, err = s.insertComment(ctx, tx, actor, babyID, sql.NullString{String: recordID, Valid: true}, input)
		return err
	})
	return comment, err
}

// WallComment adds a message to a baby's message wall in the 亲友团 board.
func (s *Store) WallComment(ctx context.Context, actor Actor, babyID string, input CommentInput) (Comment, error) {
	input, ok := cleanComment(input)
	if !ok {
		return Comment{}, ErrInvalid
	}
	var comment Comment
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if _, _, err := s.requireMember(ctx, tx, actor); err != nil {
			return err
		}
		if _, err := s.baby(ctx, tx, babyID); err != nil {
			return err
		}
		var err error
		comment, err = s.insertComment(ctx, tx, actor, babyID, sql.NullString{}, input)
		return err
	})
	return comment, err
}

func (s *Store) insertComment(ctx context.Context, tx *sql.Tx, actor Actor, babyID string, recordID sql.NullString, input CommentInput) (Comment, error) {
	if input.RequestKey != "" {
		existing, err := scanComment(tx.QueryRowContext(ctx, "SELECT "+commentColumns+
			" FROM comments WHERE author_id = ? AND request_key = ?", actor.AccountID, input.RequestKey))
		if err == nil {
			return existing, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Comment{}, err
		}
	}
	comment := Comment{ID: newID("cmt"), RecordID: recordID.String, Text: input.Text,
		AuthorID: actor.AccountID, AuthorName: actor.Username, CreatedAt: s.now()}
	if _, err := tx.ExecContext(ctx, `INSERT INTO comments(id, baby_id, record_id, text, author_id, author_name, created_at, request_key)
VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, comment.ID, babyID, recordID, comment.Text, actor.AccountID, actor.Username,
		formatTime(comment.CreatedAt), nullString(input.RequestKey)); err != nil {
		return Comment{}, err
	}
	return comment, nil
}

// Wall lists a baby's message wall, newest first.
func (s *Store) Wall(ctx context.Context, actor Actor, babyID string) ([]Comment, error) {
	if _, _, err := s.requireMember(ctx, s.db, actor); err != nil {
		return nil, err
	}
	if _, err := s.baby(ctx, s.db, babyID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+commentColumns+
		" FROM comments WHERE baby_id = ? AND record_id IS NULL ORDER BY created_at DESC, id DESC LIMIT 200", babyID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	comments := []Comment{}
	for rows.Next() {
		comment, err := scanComment(rows)
		if err != nil {
			return nil, err
		}
		comments = append(comments, comment)
	}
	return comments, rows.Err()
}

// DeleteComment removes a comment for good. Authors delete their own;
// guardians delete anyone's.
func (s *Store) DeleteComment(ctx context.Context, actor Actor, id string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		_, me, err := s.requireMember(ctx, tx, actor)
		if err != nil {
			return err
		}
		var author string
		err = tx.QueryRowContext(ctx, "SELECT author_id FROM comments WHERE id = ?", id).Scan(&author)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if author != actor.AccountID && !me.Guardian {
			return ErrForbidden
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM comments WHERE id = ?", id); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, "family.comment_deleted", id, "")
	})
}
