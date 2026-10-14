package feedback

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAnyAccountCommentsEditsAndDeletesComments(t *testing.T) {
	ctx := context.Background()
	store, now := openStore(t)
	item, err := store.Create(ctx, owner, Content{Text: "影视中心字幕偏小"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.Create(ctx, owner, Content{Text: "另一条"})
	if err != nil {
		t.Fatal(err)
	}

	*now = now.Add(time.Minute)
	withReply, err := store.AddComment(ctx, member, item.ID, Content{Text: "  我这边也一样\r\n全屏时更明显 ", New: [][]byte{pngBytes(t)}})
	if err != nil {
		t.Fatalf("add comment: %v", err)
	}
	if len(withReply.Comments) != 1 {
		t.Fatalf("comments = %+v", withReply.Comments)
	}
	first := withReply.Comments[0]
	if first.Text != "我这边也一样\n全屏时更明显" || first.CreatedBy != "member" || first.Version != 1 || len(first.Images) != 1 || first.UpdatedAt != nil {
		t.Fatalf("comment = %+v", first)
	}
	// A comment is not an edit of the item.
	if withReply.Version != item.Version || withReply.UpdatedAt != nil {
		t.Fatalf("item after comment = %+v", withReply)
	}
	*now = now.Add(time.Minute)
	if _, err := store.AddComment(ctx, owner, item.ID, Content{Text: "已在 1.0.2 修复"}); err != nil {
		t.Fatal(err)
	}

	// The comment's image is served through its item only.
	if _, data, err := store.ImageContent(ctx, item.ID, first.Images[0].ID); err != nil || !bytes.Equal(data, pngBytes(t)) {
		t.Fatalf("comment image = %v", err)
	}
	if _, _, err := store.ImageContent(ctx, other.ID, first.Images[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("comment image through another item = %v", err)
	}

	items, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var listed Item
	for _, candidate := range items {
		if candidate.ID == item.ID {
			listed = candidate
		} else if len(candidate.Comments) != 0 {
			t.Fatalf("comments leaked to %s: %+v", candidate.ID, candidate.Comments)
		}
	}
	if len(listed.Comments) != 2 || listed.Comments[0].ID != first.ID || listed.Comments[1].CreatedBy != "owner" || len(listed.Comments[0].Images) != 1 {
		t.Fatalf("listed comments = %+v", listed.Comments)
	}

	// The administrator rewrites the member's comment and drops its image; the
	// member's copy is now stale.
	*now = now.Add(time.Minute)
	edited, err := store.EditComment(ctx, owner, item.ID, first.ID, first.Version, Content{Text: "全屏时字幕偏小"})
	if err != nil {
		t.Fatalf("edit comment: %v", err)
	}
	changed := edited.Comments[0]
	if changed.Text != "全屏时字幕偏小" || changed.Version != 2 || changed.UpdatedBy != "owner" || changed.UpdatedAt == nil || len(changed.Images) != 0 {
		t.Fatalf("edited comment = %+v", changed)
	}
	if _, _, err := store.ImageContent(ctx, item.ID, first.Images[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed comment image = %v", err)
	}
	if _, err := store.EditComment(ctx, member, item.ID, first.ID, first.Version, Content{Text: "旧"}); !errors.Is(err, ErrCommentChanged) {
		t.Fatalf("stale comment edit = %v", err)
	}
	if _, err := store.EditComment(ctx, member, other.ID, first.ID, changed.Version, Content{Text: "x"}); !errors.Is(err, ErrCommentNotFound) {
		t.Fatalf("edit through another item = %v", err)
	}

	remaining, err := store.DeleteComment(ctx, item.ID, first.ID)
	if err != nil || len(remaining.Comments) != 1 || remaining.Comments[0].ID == first.ID {
		t.Fatalf("delete comment = %+v, %v", remaining.Comments, err)
	}
	if _, err := store.DeleteComment(ctx, item.ID, first.ID); !errors.Is(err, ErrCommentNotFound) {
		t.Fatalf("delete comment twice = %v", err)
	}
	if _, err := store.AddComment(ctx, member, "feedback:missing", Content{Text: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("comment on a missing item = %v", err)
	}
}

func TestCommentsAreValidatedAndGoWithTheirItem(t *testing.T) {
	ctx := context.Background()
	store, _ := openStore(t)
	item, err := store.Create(ctx, owner, Content{Text: "条目"})
	if err != nil {
		t.Fatal(err)
	}
	image := pngBytes(t)
	for name, test := range map[string]struct {
		content Content
		want    error
	}{
		"empty":           {Content{Text: "  "}, ErrEmpty},
		"long text":       {Content{Text: strings.Repeat("评", MaxCommentRunes+1)}, ErrTextTooLong},
		"too many images": {Content{New: [][]byte{image, image, image, image}}, ErrTooManyImages},
		"svg":             {Content{New: [][]byte{[]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)}}, ErrUnsupportedImage},
	} {
		if _, err := store.AddComment(ctx, member, item.ID, test.content); !errors.Is(err, test.want) {
			t.Errorf("%s: err = %v, want %v", name, err, test.want)
		}
	}
	withImages, err := store.AddComment(ctx, member, item.ID, Content{New: [][]byte{image, image}})
	if err != nil {
		t.Fatalf("image-only comment: %v", err)
	}
	comment := withImages.Comments[0]
	if _, err := store.EditComment(ctx, member, item.ID, comment.ID, comment.Version, Content{Keep: []string{comment.Images[0].ID, comment.Images[1].ID}, New: [][]byte{image, image}}); !errors.Is(err, ErrTooManyImages) {
		t.Fatalf("edit past the comment limit = %v", err)
	}

	// Deleting the item takes its comments and their images with it.
	if err := store.Delete(ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"comments", "comment_images"} {
		var count int
		if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s left = %d, %v", table, count, err)
		}
	}
}

// A database written before comments existed gains them on open.
func TestOpeningAnOlderDatabaseAddsComments(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "feedback.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE items (id TEXT PRIMARY KEY, text TEXT NOT NULL, version INTEGER NOT NULL DEFAULT 1,
created_by_id TEXT NOT NULL, created_by TEXT NOT NULL, created_at TEXT NOT NULL, updated_by TEXT NOT NULL DEFAULT '',
updated_at TEXT NOT NULL DEFAULT '', resolved_by TEXT NOT NULL DEFAULT '', resolved_at TEXT NOT NULL DEFAULT '');
CREATE TABLE images (id TEXT PRIMARY KEY, item_id TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE, position INTEGER NOT NULL,
content_type TEXT NOT NULL, size_bytes INTEGER NOT NULL, data BLOB NOT NULL);
INSERT INTO items (id, text, created_by_id, created_by, created_at) VALUES ('feedback:old', '旧条目', 'user:owner', 'owner', '2026-10-12T09:00:00.000000000Z');`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	store, err := Open(path, Options{})
	if err != nil {
		t.Fatalf("open older database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	item, err := store.AddComment(ctx, member, "feedback:old", Content{Text: "补一条评论"})
	if err != nil || len(item.Comments) != 1 || item.Text != "旧条目" {
		t.Fatalf("comment on an older item = %+v, %v", item, err)
	}
}
