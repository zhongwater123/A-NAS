package family

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRemindStage(t *testing.T) {
	today := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	open := Todo{Status: TodoOpen}
	for _, test := range []struct {
		name string
		todo Todo
		want string
	}{
		{"four days ahead", Todo{Status: TodoOpen, Due: "2026-11-02"}, ""},
		{"three days ahead", Todo{Status: TodoOpen, Due: "2026-11-01"}, StageSoon},
		{"tomorrow", Todo{Status: TodoOpen, Due: "2026-10-30"}, StageSoon},
		{"today", Todo{Status: TodoOpen, Due: "2026-10-29"}, StageToday},
		{"yesterday", Todo{Status: TodoOpen, Due: "2026-10-28"}, StageOverdue},
		{"long ago", Todo{Status: TodoOpen, Due: "2025-10-28"}, StageOverdue},
		{"across the year", Todo{Status: TodoOpen, Due: "2027-01-01"}, ""},
		{"no due date", open, ""},
		{"done", Todo{Status: TodoDone, Due: "2026-10-29"}, ""},
		{"ignored", Todo{Status: TodoIgnored, Due: "2026-10-28"}, ""},
		{"snoozed", Todo{Status: TodoOpen, Due: "2026-10-28", SnoozedUntil: "2026-10-30"}, ""},
		{"snooze ends today", Todo{Status: TodoOpen, Due: "2026-10-28", SnoozedUntil: "2026-10-29"}, StageOverdue},
	} {
		if got := remindStage(test.todo, today); got != test.want {
			t.Errorf("%s: stage = %q, want %q", test.name, got, test.want)
		}
	}
}

func reminderKeys(reminders []TodoReminder) []string {
	keys := []string{}
	for _, reminder := range reminders {
		keys = append(keys, reminder.Key)
	}
	return keys
}

func TestTodoRemindersAreToldOncePerPerson(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// Today is 2026-10-29.
	bill := must(f.store.AddTodo(ctx, mom, TodoFields{Title: "交电费", Category: "bill", Due: "2026-10-31", OwnerID: dad.AccountID}))
	must(f.store.AddTodo(ctx, mom, TodoFields{Title: "换证", Due: "2026-11-20"}))
	must(f.store.AddTodo(ctx, mom, TodoFields{Title: "修水管"}))
	reminders := must(f.store.TodoReminders(ctx, mom))
	want := TodoReminder{Key: "todo:" + bill.ID + ":0:soon", TodoID: bill.ID, Stage: StageSoon, Title: "交电费",
		Category: "bill", Due: "2026-10-31", Scope: TodoShared, OwnerID: dad.AccountID}
	if len(reminders) != 1 || reminders[0] != want {
		t.Fatalf("reminders = %+v", reminders)
	}
	// tenant, who never joined the family, is reminded too.
	if got := reminderKeys(must(f.store.TodoReminders(ctx, tenant))); !slices.Equal(got, []string{want.Key}) {
		t.Fatalf("tenant's reminders = %v", got)
	}
	// mom acknowledges for herself alone. Keys that are no reminder of hers
	// now are ignored.
	if err := f.store.AcknowledgeTodoReminders(ctx, mom, []string{want.Key, "todo:" + bill.ID + ":0:today", "todo:made-up"}); err != nil {
		t.Fatal(err)
	}
	if got := must(f.store.TodoReminders(ctx, mom)); len(got) != 0 {
		t.Fatalf("mom is told again: %+v", got)
	}
	if got := reminderKeys(must(f.store.TodoReminders(ctx, dad))); !slices.Equal(got, []string{want.Key}) {
		t.Fatalf("dad's reminders = %v", got)
	}
	var stored int
	if err := f.store.db.QueryRow("SELECT COUNT(*) FROM hints_seen WHERE account_id = ?", mom.AccountID).Scan(&stored); err != nil || stored != 1 {
		t.Fatalf("mom's acknowledgements = %d, %v", stored, err)
	}
	// The next stage is told again, and then the last.
	f.now = f.now.AddDate(0, 0, 2)
	if got := reminderKeys(must(f.store.TodoReminders(ctx, mom))); !slices.Equal(got, []string{"todo:" + bill.ID + ":0:today"}) {
		t.Fatalf("on the due date = %v", got)
	}
	f.now = f.now.AddDate(0, 0, 1)
	overdue := "todo:" + bill.ID + ":0:overdue"
	if got := reminderKeys(must(f.store.TodoReminders(ctx, mom))); !slices.Equal(got, []string{overdue}) {
		t.Fatalf("a day late = %v", got)
	}
	wantErr(t, f.store.AcknowledgeTodoReminders(ctx, mom, []string{overdue}), nil)
	if got := must(f.store.TodoReminders(ctx, mom)); len(got) != 0 {
		t.Fatalf("told twice of the same stage: %+v", got)
	}
	// Snoozing quiets it until the snooze ends, then starts over.
	must(f.store.SnoozeTodo(ctx, dad, bill.ID, 2))
	if got := must(f.store.TodoReminders(ctx, mom)); len(got) != 0 {
		t.Fatalf("told while snoozed: %+v", got)
	}
	f.now = f.now.AddDate(0, 0, 2)
	if got := reminderKeys(must(f.store.TodoReminders(ctx, mom))); !slices.Equal(got, []string{"todo:" + bill.ID + ":1:overdue"}) {
		t.Fatalf("after the snooze = %v", got)
	}
	wantErr(t, f.store.AcknowledgeTodoReminders(ctx, mom, []string{"todo:" + bill.ID + ":1:overdue"}), nil)
	// Done, it reminds nobody; opened again, it starts over.
	must(f.store.SetTodoStatus(ctx, dad, bill.ID, TodoDone))
	if got := must(f.store.TodoReminders(ctx, dad)); len(got) != 0 {
		t.Fatalf("a done to-do reminds: %+v", got)
	}
	must(f.store.SetTodoStatus(ctx, dad, bill.ID, TodoOpen))
	if got := reminderKeys(must(f.store.TodoReminders(ctx, mom))); !slices.Equal(got, []string{"todo:" + bill.ID + ":2:overdue"}) {
		t.Fatalf("opened again = %v", got)
	}
	wantErr(t, f.store.AcknowledgeTodoReminders(ctx, mom, []string{"todo:" + bill.ID + ":2:overdue"}), nil)
	// A new due date starts over too.
	moved := must(f.store.UpdateTodo(ctx, mom, bill.ID, TodoEdit{Title: "交电费", Category: "bill", Due: f.now.Format(dateLayout), Version: 1}))
	if got := reminderKeys(must(f.store.TodoReminders(ctx, mom))); !slices.Equal(got, []string{"todo:" + bill.ID + ":3:today"}) || moved.RemindRound != 3 {
		t.Fatalf("new due date = %v, round %d", got, moved.RemindRound)
	}
}

func TestOwnTodosRemindOnlyTheirOwner(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	private := must(f.store.addTodo(ctx, mom, TodoFields{Title: "复查", Category: "medical", Due: "2026-10-29"}, mom.AccountID))
	key := "todo:" + private.ID + ":0:today"
	if got := must(f.store.TodoReminders(ctx, mom)); len(got) != 1 || got[0].Key != key || got[0].Scope != TodoPrivate {
		t.Fatalf("mom's reminders = %+v", got)
	}
	if got := must(f.store.TodoReminders(ctx, dad)); len(got) != 0 {
		t.Fatalf("dad is told of mom's own to-do: %+v", got)
	}
	// dad cannot record a key of mom's own to-do either.
	wantErr(t, f.store.AcknowledgeTodoReminders(ctx, dad, []string{key}), nil)
	var stored int
	if err := f.store.db.QueryRow("SELECT COUNT(*) FROM hints_seen").Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("acknowledgements = %d, %v", stored, err)
	}
}

func TestTodoReminderKeysAreChecked(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tooMany := make([]string, maxHintKeys+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("todo:%d", i)
	}
	for name, keys := range map[string][]string{
		"not a to-do": {"vaccine:baby:hepb:1"},
		"empty":       {""},
		"too long":    {"todo:" + strings.Repeat("x", maxHintKeyBytes)},
		"not UTF-8":   {"todo:\xff"},
		"too many":    tooMany,
	} {
		if err := f.store.AcknowledgeTodoReminders(ctx, mom, keys); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: error = %v, want %v", name, err, ErrInvalid)
		}
	}
	if err := f.store.AcknowledgeTodoReminders(ctx, mom, nil); err != nil {
		t.Fatal(err)
	}
}

func TestNewDueDateEndsSnooze(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// Today is 2026-10-29. A bill due 11-01 is snoozed for 7 days, to 11-05.
	bill := must(f.store.AddTodo(ctx, mom, TodoFields{Title: "交电费", Category: "bill", Due: "2026-11-01"}))
	must(f.store.SnoozeTodo(ctx, mom, bill.ID, 7))
	if got := must(f.store.TodoReminders(ctx, dad)); len(got) != 0 {
		t.Fatalf("told while snoozed: %+v", got)
	}
	// Someone moves the due date to today: the snooze ends, and today's stage is told at once.
	moved := must(f.store.UpdateTodo(ctx, dad, bill.ID, TodoEdit{Title: "交电费", Category: "bill", Due: "2026-10-29", Version: 1}))
	if moved.SnoozedUntil != "" || moved.RemindRound != 2 {
		t.Fatalf("moved = %+v", moved)
	}
	if got := reminderKeys(must(f.store.TodoReminders(ctx, mom))); !slices.Equal(got, []string{"todo:" + bill.ID + ":2:today"}) {
		t.Fatalf("after the new due date = %v", got)
	}
	// An edit that keeps the due date keeps the snooze.
	snoozed := must(f.store.SnoozeTodo(ctx, mom, bill.ID, 3))
	kept := must(f.store.UpdateTodo(ctx, mom, bill.ID, TodoEdit{Title: "交水费", Category: "bill", Due: "2026-10-29", Version: 2}))
	if kept.SnoozedUntil != snoozed.SnoozedUntil || kept.RemindRound != snoozed.RemindRound {
		t.Fatalf("an edit of the title = %+v", kept)
	}
}

func TestPurgeForgetsKeysOfLongClosedTodos(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	closed := must(f.store.AddTodo(ctx, mom, TodoFields{Title: "交电费", Due: "2026-10-29"}))
	open := must(f.store.AddTodo(ctx, mom, TodoFields{Title: "交水费", Due: "2026-10-29"}))
	wantErr(t, f.store.AcknowledgeTodoReminders(ctx, mom, []string{
		"todo:" + closed.ID + ":0:today", "todo:" + open.ID + ":0:today"}), nil)
	// A key of the family center's own hints is left alone.
	if _, err := f.store.db.Exec("INSERT INTO hints_seen(account_id, hint_key, seen_at) VALUES(?, ?, ?)",
		mom.AccountID, "vaccine:baby:hepb:1", formatTime(f.now)); err != nil {
		t.Fatal(err)
	}
	must(f.store.SetTodoStatus(ctx, mom, closed.ID, TodoDone))
	keys := func() []string {
		rows, err := f.store.db.Query("SELECT hint_key FROM hints_seen ORDER BY hint_key")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		var got []string
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				t.Fatal(err)
			}
			got = append(got, key)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return got
	}
	// Within ClosedTodoDays the closed to-do still lists, and its keys stay.
	f.now = f.now.AddDate(0, 0, ClosedTodoDays-1)
	wantErr(t, f.store.Purge(ctx), nil)
	if got := keys(); len(got) != 3 {
		t.Fatalf("purged too early: %v", got)
	}
	f.now = f.now.AddDate(0, 0, 2)
	wantErr(t, f.store.Purge(ctx), nil)
	if got := keys(); !slices.Equal(got, []string{"todo:" + open.ID + ":0:today", "vaccine:baby:hepb:1"}) {
		t.Fatalf("after the purge = %v", got)
	}
}
