package feedback

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	owner  = Actor{ID: "user:owner", Username: "owner"}
	member = Actor{ID: "user:member", Username: "member"}
)

func openStore(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	store, err := Open(filepath.Join(t.TempDir(), "feedback.db"), Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, &now
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, image.NewGray(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestEveryAccountEditsResolvesAndDeletesTheSameList(t *testing.T) {
	ctx := context.Background()
	store, now := openStore(t)
	first, err := store.Create(ctx, owner, Content{Text: "  上传大文件时进度条卡住\r\n第二行  ", New: [][]byte{pngBytes(t)}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if first.Text != "上传大文件时进度条卡住\n第二行" || first.CreatedBy != "owner" || first.Version != 1 || first.Resolved || first.UpdatedAt != nil {
		t.Fatalf("created = %+v", first)
	}
	if len(first.Images) != 1 || first.Images[0].ContentType != "image/png" {
		t.Fatalf("images = %+v", first.Images)
	}
	*now = now.Add(time.Minute)
	second, err := store.Create(ctx, member, Content{Text: "希望相册支持 HEIC"})
	if err != nil {
		t.Fatal(err)
	}

	items, err := store.List(ctx)
	if err != nil || len(items) != 2 || items[0].ID != second.ID || items[1].ID != first.ID {
		t.Fatalf("list = %+v, %v", items, err)
	}
	if len(items[0].Images) != 0 || len(items[1].Images) != 1 {
		t.Fatalf("list images = %+v / %+v", items[0].Images, items[1].Images)
	}

	// A member edits the administrator's item: drops the screenshot, adds another.
	*now = now.Add(time.Minute)
	edited, err := store.Edit(ctx, member, first.ID, first.Version, Content{Text: "进度条卡在 99%", New: [][]byte{pngBytes(t)}})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if edited.Text != "进度条卡在 99%" || edited.Version != 2 || edited.UpdatedBy != "member" || edited.UpdatedAt == nil {
		t.Fatalf("edited = %+v", edited)
	}
	if len(edited.Images) != 1 || edited.Images[0].ID == first.Images[0].ID {
		t.Fatalf("edited images = %+v", edited.Images)
	}
	if _, _, err := store.ImageContent(ctx, first.ID, first.Images[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed image = %v", err)
	}
	if _, data, err := store.ImageContent(ctx, first.ID, edited.Images[0].ID); err != nil || !bytes.Equal(data, pngBytes(t)) {
		t.Fatalf("image content = %v", err)
	}
	// An image is only reachable through its own item.
	if _, _, err := store.ImageContent(ctx, second.ID, edited.Images[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("image through another item = %v", err)
	}

	// The owner still holds version 1, so their edit would overwrite the member's.
	if _, err := store.Edit(ctx, owner, first.ID, first.Version, Content{Text: "旧内容"}); !errors.Is(err, ErrChanged) {
		t.Fatalf("stale edit = %v", err)
	}

	resolved, err := store.SetResolved(ctx, owner, first.ID, true)
	if err != nil || !resolved.Resolved || resolved.ResolvedBy != "owner" || resolved.ResolvedAt == nil || resolved.Version != 2 {
		t.Fatalf("resolved = %+v, %v", resolved, err)
	}
	// Marking it again keeps who handled it first.
	again, err := store.SetResolved(ctx, member, first.ID, true)
	if err != nil || again.ResolvedBy != "owner" {
		t.Fatalf("resolved again = %+v, %v", again, err)
	}
	reopened, err := store.SetResolved(ctx, member, first.ID, false)
	if err != nil || reopened.Resolved || reopened.ResolvedBy != "" || reopened.ResolvedAt != nil {
		t.Fatalf("reopened = %+v, %v", reopened, err)
	}

	// Images live in the database, so deleting them must return the space.
	var vacuum int
	if err := store.db.QueryRowContext(ctx, "PRAGMA auto_vacuum").Scan(&vacuum); err != nil || vacuum != 1 {
		t.Fatalf("auto_vacuum = %d, %v", vacuum, err)
	}
	if err := store.Delete(ctx, first.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := store.Delete(ctx, first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice = %v", err)
	}
	if _, _, err := store.ImageContent(ctx, first.ID, edited.Images[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("image after delete = %v", err)
	}
	if _, err := store.SetResolved(ctx, owner, first.ID, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resolve deleted = %v", err)
	}
}

func TestContentIsValidated(t *testing.T) {
	ctx := context.Background()
	store, _ := openStore(t)
	image := pngBytes(t)
	tooMany := make([][]byte, MaxImages+1)
	for index := range tooMany {
		tooMany[index] = image
	}
	for name, test := range map[string]struct {
		content Content
		want    error
	}{
		"empty":           {Content{Text: " \n "}, ErrEmpty},
		"long text":       {Content{Text: strings.Repeat("长", MaxTextRunes+1)}, ErrTextTooLong},
		"too many images": {Content{New: tooMany}, ErrTooManyImages},
		"large image":     {Content{New: [][]byte{append(image, make([]byte, MaxImageBytes)...)}}, ErrImageTooLarge},
		"svg":             {Content{New: [][]byte{[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)}}, ErrUnsupportedImage},
		"html":            {Content{New: [][]byte{[]byte("<!DOCTYPE html><script>alert(1)</script>")}}, ErrUnsupportedImage},
	} {
		if _, err := store.Create(ctx, owner, test.content); !errors.Is(err, test.want) {
			t.Errorf("%s: err = %v, want %v", name, err, test.want)
		}
	}
	if items, _ := store.List(ctx); len(items) != 0 {
		t.Fatalf("invalid content was stored: %+v", items)
	}

	// An image alone is enough, and removing the last image of an item
	// without text leaves nothing.
	item, err := store.Create(ctx, owner, Content{New: [][]byte{image}})
	if err != nil {
		t.Fatalf("image only: %v", err)
	}
	if _, err := store.Edit(ctx, owner, item.ID, item.Version, Content{}); !errors.Is(err, ErrEmpty) {
		t.Fatalf("empty edit = %v", err)
	}
	full := make([][]byte, MaxImages-1)
	for index := range full {
		full[index] = image
	}
	if _, err := store.Edit(ctx, owner, item.ID, item.Version, Content{Keep: []string{item.Images[0].ID}, New: append(full, image)}); !errors.Is(err, ErrTooManyImages) {
		t.Fatalf("edit past the limit = %v", err)
	}
	kept, err := store.Edit(ctx, owner, item.ID, item.Version, Content{Keep: []string{item.Images[0].ID}, New: full})
	if err != nil || len(kept.Images) != MaxImages || kept.Images[0].ID != item.Images[0].ID {
		t.Fatalf("edit to the limit = %+v, %v", kept.Images, err)
	}
	if _, err := store.Edit(ctx, owner, "feedback:missing", 1, Content{Text: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("edit missing = %v", err)
	}
}

func TestListOrdersByCreationAcrossSubseconds(t *testing.T) {
	ctx := context.Background()
	store, now := openStore(t)
	*now = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	whole, err := store.Create(ctx, owner, Content{Text: "整秒"})
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(100 * time.Millisecond)
	later, err := store.Create(ctx, owner, Content{Text: "稍后"})
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.List(ctx)
	if err != nil || items[0].ID != later.ID || items[1].ID != whole.ID {
		t.Fatalf("list = %+v, %v", items, err)
	}
}
