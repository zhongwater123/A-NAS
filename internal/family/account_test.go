package family

import (
	"context"
	"testing"
)

func TestRemovingADeletedAccountKeepsWhatItWrote(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	baby := f.family(t)
	record := must(f.store.AddRecord(ctx, mom, baby.ID, RecordInput{Kind: KindJournal, Text: "今天会笑了", OccurredOn: "2026-10-28"}))
	// The account is deleted: it leaves the directory, then the family.
	delete(f.accounts, mom.AccountID)
	if err := f.store.RemoveAccount(ctx, mom.AccountID); err != nil {
		t.Fatal(err)
	}
	overview := must(f.store.Overview(ctx, dad))
	if !overview.Me.Guardian {
		t.Fatalf("the family has no guardian after its only one was deleted: %+v", overview.Me)
	}
	for _, member := range overview.Members {
		if member.AccountID == mom.AccountID {
			t.Fatalf("the deleted account is still a member: %+v", overview.Members)
		}
	}
	if got := must(f.store.Record(ctx, dad, record.ID)); got.AuthorName != "mom" {
		t.Fatalf("the deleted account's record = %+v", got)
	}
	// A new account given the name joins as anyone new does.
	f.accounts["u-mom-2"] = Account{ID: "u-mom-2", Username: "mom"}
	again := Actor{AccountID: "u-mom-2", Username: "mom"}
	if me := must(f.store.Join(ctx, again, "妈妈")); me.Status != StatusPending {
		t.Fatalf("the new account = %+v, want a pending request", me)
	}
	if err := f.store.RemoveAccount(ctx, mom.AccountID); err != nil {
		t.Fatalf("removing again: %v", err)
	}
}

func TestRemovingAnAccountDropsItsOwnTodos(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	own := must(f.store.addTodo(ctx, dad, TodoFields{Title: "复查", Category: "medical", Due: "2026-10-29"}, dad.AccountID))
	shared := must(f.store.AddTodo(ctx, dad, TodoFields{Title: "交电费", Due: "2026-10-29", OwnerID: dad.AccountID}))
	must(f.store.SetTodoStatus(ctx, dad, shared.ID, TodoIgnored))
	must(f.store.SetTodoStatus(ctx, mom, shared.ID, TodoOpen))
	keys := []string{}
	for _, reminder := range must(f.store.TodoReminders(ctx, dad)) {
		keys = append(keys, reminder.Key)
	}
	if len(keys) != 2 {
		t.Fatalf("dad's reminders = %v", keys)
	}
	wantErr(t, f.store.AcknowledgeTodoReminders(ctx, dad, keys), nil)
	delete(f.accounts, dad.AccountID)
	if err := f.store.RemoveAccount(ctx, dad.AccountID); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := f.store.db.QueryRow("SELECT COUNT(*) FROM todos WHERE id = ?", own.ID).Scan(&left); err != nil || left != 0 {
		t.Fatalf("dad's own to-do is left: %d, %v", left, err)
	}
	if err := f.store.db.QueryRow("SELECT COUNT(*) FROM hints_seen").Scan(&left); err != nil || left != 0 {
		t.Fatalf("dad's acknowledgements are left: %d, %v", left, err)
	}
	// The shared to-do stays, under the names it was written with.
	todos := must(f.store.Todos(ctx, mom))
	if len(todos) != 1 || todos[0].CreatedByID != dad.AccountID || todos[0].CreatedByName != "dad" ||
		todos[0].OwnerID != dad.AccountID || todos[0].OwnerName != "dad" {
		t.Fatalf("shared to-dos = %+v", todos)
	}
}
