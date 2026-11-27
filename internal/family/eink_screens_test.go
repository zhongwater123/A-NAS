package family

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestScreensArePairedWithATokenKeptAsAHash(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	screen, token, err := f.store.AddScreen(ctx, mom, " 客厅 ")
	if err != nil {
		t.Fatal(err)
	}
	if screen.Name != "客厅" || screen.SeenAt != nil || screen.Report != nil || len(token) < 40 {
		t.Fatalf("paired %+v with a token of %d characters", screen, len(token))
	}
	var stored string
	if err := f.store.db.QueryRowContext(ctx, "SELECT token_hash FROM eink_screens WHERE id = ?", screen.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == token || strings.Contains(stored, token) || stored != hashScreenToken(token) {
		t.Fatal("the store keeps the token itself instead of its hash")
	}
	if found := must(f.store.ScreenByToken(ctx, token)); found.ID != screen.ID {
		t.Fatalf("the token finds %+v", found)
	}
	for _, wrong := range []string{"", token + "x", strings.ToLower(token)} {
		if _, err := f.store.ScreenByToken(ctx, wrong); err != ErrScreenToken {
			t.Errorf("token %q: err = %v", wrong, err)
		}
	}
	_, second, err := f.store.AddScreen(ctx, mom, "玄关")
	if err != nil || second == token {
		t.Fatalf("a second screen: token reused or %v", err)
	}
}

func TestScreenNames(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, name := range []string{"", "   ", "一二三四五六七", "客\n厅"} {
		if _, _, err := f.store.AddScreen(ctx, mom, name); err != ErrInvalid {
			t.Errorf("name %q: err = %v", name, err)
		}
	}
	living := must3(f.store.AddScreen(ctx, mom, "客厅"))
	if _, _, err := f.store.AddScreen(ctx, dad, "客厅"); err != ErrConflict {
		t.Errorf("a second 客厅: err = %v", err)
	}
	door := must3(f.store.AddScreen(ctx, mom, "玄关"))
	if _, err := f.store.RenameScreen(ctx, door.ID, "客厅"); err != ErrConflict {
		t.Errorf("renaming onto 客厅: err = %v", err)
	}
	if renamed := must(f.store.RenameScreen(ctx, living.ID, "客厅")); renamed.Name != "客厅" {
		t.Errorf("keeping its own name: %+v", renamed)
	}
	if renamed := must(f.store.RenameScreen(ctx, door.ID, "冰箱")); renamed.Name != "冰箱" {
		t.Errorf("renamed = %+v", renamed)
	}
	if _, err := f.store.RenameScreen(ctx, "scr_missing", "书房"); err != ErrNotFound {
		t.Errorf("renaming a missing screen: err = %v", err)
	}
	if names := screenNames(must(f.store.Screens(ctx))); names != "客厅,冰箱" {
		t.Errorf("screens = %s", names)
	}
}

func TestRemovingAScreenRevokesItsToken(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	screen, token, _ := f.store.AddScreen(ctx, mom, "客厅")
	if err := f.store.RemoveScreen(ctx, screen.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ScreenByToken(ctx, token); err != ErrScreenToken {
		t.Errorf("a removed screen's token: err = %v", err)
	}
	if err := f.store.RemoveScreen(ctx, screen.ID); err != ErrNotFound {
		t.Errorf("removing twice: err = %v", err)
	}
	// What a removed screen said as it was being removed is dropped.
	if err := f.store.RecordAsk(ctx, screen.ID, "k", f.now, f.now, ScreenReport{}); err != nil {
		t.Fatal(err)
	}
	if screens := must(f.store.Screens(ctx)); len(screens) != 0 {
		t.Errorf("a removed screen came back: %+v", screens)
	}
}

func TestRecordAsk(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	screen, token, _ := f.store.AddScreen(ctx, mom, "客厅")
	battery, usb := 35, false
	next := f.now.Add(15 * time.Minute)
	report := ScreenReport{Battery: &battery, USB: &usb, Firmware: "0.2.5-s3", Fail: "wifi", History: "0712a-none-4"}
	if err := f.store.RecordAsk(ctx, screen.ID, "0123456789abcdef:bw", f.now, next, report); err != nil {
		t.Fatal(err)
	}
	got := must(f.store.ScreenByToken(ctx, token))
	if got.Shown != "0123456789abcdef:bw" || got.SeenAt == nil || !got.SeenAt.Equal(f.now) || got.NextAt == nil || !got.NextAt.Equal(next) {
		t.Fatalf("recorded %+v", got)
	}
	if r := got.Report; r == nil || *r.Battery != 35 || *r.USB || r.Firmware != "0.2.5-s3" || r.History != "0712a-none-4" || r.RSSI != nil {
		t.Fatalf("report = %+v", got.Report)
	}
}

func TestScreenTodosAreSharedOpenAndDated(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	shared := must(f.store.AddTodo(ctx, mom, TodoFields{Title: "交电费", Category: "bill", Due: "2026-10-31"}))
	must(f.store.AddTodo(ctx, mom, TodoFields{Title: "修热水器"}))
	done := must(f.store.AddTodo(ctx, mom, TodoFields{Title: "交水费", Category: "bill", Due: "2026-10-30"}))
	must(f.store.SetTodoStatus(ctx, mom, done.ID, TodoDone))
	must(f.store.addTodo(ctx, mom, TodoFields{Title: "复查", Category: "medical", Due: "2026-10-30"}, mom.AccountID))
	if got := todoIDs(must(f.store.ScreenTodos(ctx))); len(got) != 1 || got[0] != shared.ID {
		t.Fatalf("screen to-dos = %v", got)
	}
	board := TodoBoard(must(f.store.ScreenTodos(ctx)), f.store.Today())
	if len(board.Rows) != 1 || board.Rows[0] != (BoardRow{Text: "交电费", When: "还有 2 天", Urgent: true}) {
		t.Fatalf("board = %+v", board)
	}
}

func must3[T any](value T, _ string, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func screenNames(screens []Screen) string {
	names := make([]string, len(screens))
	for i, screen := range screens {
		names[i] = screen.Name
	}
	return strings.Join(names, ",")
}
