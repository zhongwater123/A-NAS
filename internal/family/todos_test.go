package family

import (
	"context"
	"slices"
	"testing"
	"time"
)

func todoIDs(todos []Todo) []string {
	ids := []string{}
	for _, todo := range todos {
		ids = append(ids, todo.ID)
	}
	return ids
}

func cents(value int64) *int64 { return new(value) }

func TestTodosAreSharedOrPrivate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.family(t)
	shared := must(f.store.AddTodo(ctx, mom, TodoFields{Title: "交电费", Category: "bill", Due: "2026-11-05", AmountCents: cents(12345)}))
	if shared.Scope != TodoShared || shared.Status != TodoOpen || shared.Source != TodoManual || shared.Version != 1 ||
		shared.CreatedByID != mom.AccountID || shared.CreatedByName != "mom" || *shared.AmountCents != 12345 {
		t.Fatalf("shared to-do = %+v", shared)
	}
	// Someone's own to-do comes only from their own sources, which do not
	// exist yet; the store keeps it to them.
	private := must(f.store.addTodo(ctx, mom, TodoFields{Title: "复查", Category: "medical"}, mom.AccountID))
	if private.Scope != TodoPrivate {
		t.Fatalf("private to-do = %+v", private)
	}
	if got := todoIDs(must(f.store.Todos(ctx, mom))); !slices.Equal(got, []string{shared.ID, private.ID}) {
		t.Fatalf("mom sees %v", got)
	}
	// dad is family, tenant is not; both see what is shared, neither what is mom's.
	for _, other := range []Actor{dad, tenant} {
		if got := todoIDs(must(f.store.Todos(ctx, other))); !slices.Equal(got, []string{shared.ID}) {
			t.Fatalf("%s sees %v", other.Username, got)
		}
		_, err := f.store.UpdateTodo(ctx, other, private.ID, TodoEdit{Title: "复查", Version: 1})
		wantErr(t, err, ErrNotFound)
		_, err = f.store.SetTodoStatus(ctx, other, private.ID, TodoDone)
		wantErr(t, err, ErrNotFound)
		_, err = f.store.SnoozeTodo(ctx, other, private.ID, 3)
		wantErr(t, err, ErrNotFound)
	}
	// tenant may change what is shared without joining the family.
	edited := must(f.store.UpdateTodo(ctx, tenant, shared.ID, TodoEdit{Title: "交水费", Category: "bill", Version: 1}))
	if edited.Title != "交水费" || edited.Version != 2 || edited.Due != "" || edited.AmountCents != nil {
		t.Fatalf("edited by tenant = %+v", edited)
	}
}

func TestTodoFieldsAreChecked(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for name, fields := range map[string]TodoFields{
		"empty title":      {Title: "  "},
		"long title":       {Title: "一二三四五六七八九十一"},
		"control":          {Title: "交\x07费"},
		"unknown category": {Title: "交费", Category: "tax"},
		"bad date":         {Title: "交费", Due: "2026-13-01"},
		"no such day":      {Title: "交费", Due: "2026-02-30"},
		"unknown owner":    {Title: "交费", OwnerID: "u-nobody"},
		"negative amount":  {Title: "交费", AmountCents: cents(-1)},
		"long request key": {Title: "交费", RequestKey: string(make([]byte, 129))},
	} {
		if _, err := f.store.AddTodo(ctx, mom, fields); err == nil {
			t.Errorf("%s: added", name)
		} else {
			wantErr(t, err, ErrInvalid)
		}
	}
	todo := must(f.store.AddTodo(ctx, mom, TodoFields{Title: " 一二三四五六七八九十 ", OwnerID: dad.AccountID}))
	if todo.Title != "一二三四五六七八九十" || todo.Category != "other" || todo.OwnerID != dad.AccountID || todo.OwnerName != "dad" {
		t.Fatalf("todo = %+v", todo)
	}
	_, err := f.store.UpdateTodo(ctx, mom, todo.ID, TodoEdit{Title: "交费"})
	wantErr(t, err, ErrInvalid)
	_, err = f.store.SetTodoStatus(ctx, mom, todo.ID, "pending")
	wantErr(t, err, ErrInvalid)
	for _, days := range []int{0, 31} {
		_, err = f.store.SnoozeTodo(ctx, mom, todo.ID, days)
		wantErr(t, err, ErrInvalid)
	}
	// An owner whose account is gone stays, under the name kept; only a new
	// owner must be an active account.
	delete(f.accounts, dad.AccountID)
	edited := must(f.store.UpdateTodo(ctx, mom, todo.ID, TodoEdit{Title: "交费", OwnerID: dad.AccountID, Version: 1}))
	if edited.OwnerID != dad.AccountID || edited.OwnerName != "dad" {
		t.Fatalf("edited = %+v", edited)
	}
	_, err = f.store.UpdateTodo(ctx, mom, todo.ID, TodoEdit{Title: "交费", OwnerID: "u-nobody", Version: 2})
	wantErr(t, err, ErrInvalid)
	cleared := must(f.store.UpdateTodo(ctx, mom, todo.ID, TodoEdit{Title: "交费", Version: 2}))
	if cleared.OwnerID != "" || cleared.OwnerName != "" {
		t.Fatalf("cleared = %+v", cleared)
	}
	// Once dad's account is gone, he is no longer offered, and a new to-do cannot be his.
	for _, owner := range must(f.store.TodoOwners(ctx)) {
		if owner.ID == dad.AccountID {
			t.Fatalf("a gone account is offered: %+v", owner)
		}
	}
	_, err = f.store.AddTodo(ctx, mom, TodoFields{Title: "交费", OwnerID: dad.AccountID})
	wantErr(t, err, ErrInvalid)
}

func TestTodoOwnersAreTheActiveAccounts(t *testing.T) {
	f := newFixture(t)
	owners := must(f.store.TodoOwners(context.Background()))
	if len(owners) != len(f.accounts) {
		t.Fatalf("owners = %+v", owners)
	}
	for i, owner := range owners {
		if f.accounts[owner.ID].Username != owner.Username || i > 0 && owners[i-1].Username > owner.Username {
			t.Fatalf("owners = %+v", owners)
		}
	}
}

func TestTodoStatusChanges(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	todo := must(f.store.AddTodo(ctx, mom, TodoFields{Title: "续保", Category: "insurance", Due: "2026-11-01"}))
	todo = must(f.store.SnoozeTodo(ctx, mom, todo.ID, 7))
	if todo.SnoozedUntil != "2026-11-05" || todo.RemindRound != 1 || todo.Version != 1 {
		t.Fatalf("snoozed = %+v", todo)
	}
	f.now = f.now.Add(time.Hour)
	done := must(f.store.SetTodoStatus(ctx, dad, todo.ID, TodoDone))
	if done.Status != TodoDone || done.ClosedByID != dad.AccountID || done.ClosedByName != "dad" || done.ClosedAt == nil ||
		done.SnoozedUntil != "" || done.Version != 1 {
		t.Fatalf("done = %+v", done)
	}
	// Marking it done again keeps who closed it first.
	f.now = f.now.Add(time.Hour)
	again := must(f.store.SetTodoStatus(ctx, mom, todo.ID, TodoDone))
	if again.ClosedByID != dad.AccountID || !again.ClosedAt.Equal(*done.ClosedAt) {
		t.Fatalf("done again = %+v", again)
	}
	// Ignoring it instead records the new closer.
	ignored := must(f.store.SetTodoStatus(ctx, mom, todo.ID, TodoIgnored))
	if ignored.Status != TodoIgnored || ignored.ClosedByID != mom.AccountID || !ignored.ClosedAt.After(*done.ClosedAt) {
		t.Fatalf("ignored = %+v", ignored)
	}
	_, err := f.store.SnoozeTodo(ctx, mom, todo.ID, 3)
	wantErr(t, err, ErrConflict)
	// A closed to-do can still be edited.
	edited := must(f.store.UpdateTodo(ctx, mom, todo.ID, TodoEdit{Title: "续车险", Category: "insurance", Due: "2026-11-01", Version: 1}))
	if edited.Status != TodoIgnored || edited.Version != 2 || edited.RemindRound != 1 {
		t.Fatalf("edited closed = %+v", edited)
	}
	opened := must(f.store.SetTodoStatus(ctx, mom, todo.ID, TodoOpen))
	if opened.Status != TodoOpen || opened.ClosedAt != nil || opened.ClosedByID != "" || opened.ClosedByName != "" ||
		opened.SnoozedUntil != "" || opened.RemindRound != 2 || opened.Version != 2 {
		t.Fatalf("opened = %+v", opened)
	}
	// Closed ones stay listed for ClosedTodoDays.
	must(f.store.SetTodoStatus(ctx, mom, todo.ID, TodoDone))
	f.now = f.now.AddDate(0, 0, ClosedTodoDays).Add(-time.Minute)
	if got := todoIDs(must(f.store.Todos(ctx, dad))); !slices.Equal(got, []string{todo.ID}) {
		t.Fatalf("listed before the cutoff: %v", got)
	}
	f.now = f.now.Add(2 * time.Minute)
	if got := must(f.store.Todos(ctx, dad)); len(got) != 0 {
		t.Fatalf("listed after the cutoff: %+v", got)
	}
}

func TestTodosAreListedMostUrgentFirst(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	later := must(f.store.AddTodo(ctx, mom, TodoFields{Title: "换证", Due: "2026-12-01"}))
	undated := must(f.store.AddTodo(ctx, mom, TodoFields{Title: "修水管"}))
	overdue := must(f.store.AddTodo(ctx, mom, TodoFields{Title: "交电费", Due: "2026-10-20"}))
	first := must(f.store.AddTodo(ctx, mom, TodoFields{Title: "复查", Due: "2026-10-01"}))
	second := must(f.store.AddTodo(ctx, mom, TodoFields{Title: "续约"}))
	must(f.store.SetTodoStatus(ctx, mom, first.ID, TodoDone))
	f.now = f.now.Add(time.Minute)
	must(f.store.SetTodoStatus(ctx, mom, second.ID, TodoIgnored))
	want := []string{overdue.ID, later.ID, undated.ID, second.ID, first.ID}
	if got := todoIDs(must(f.store.Todos(ctx, dad))); !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestTodoEditsAreVersioned(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	todo := must(f.store.AddTodo(ctx, mom, TodoFields{Title: "交电费", Due: "2026-11-01"}))
	edited := must(f.store.UpdateTodo(ctx, dad, todo.ID, TodoEdit{Title: "交电费", Due: "2026-11-01", AmountCents: cents(100), Version: 1}))
	if edited.Version != 2 || edited.RemindRound != 0 || edited.UpdatedAt == nil {
		t.Fatalf("edited = %+v", edited)
	}
	_, err := f.store.UpdateTodo(ctx, mom, todo.ID, TodoEdit{Title: "交燃气费", Version: 1})
	wantErr(t, err, ErrChanged)
	moved := must(f.store.UpdateTodo(ctx, mom, todo.ID, TodoEdit{Title: "交电费", Due: "2026-11-03", Version: 2}))
	if moved.RemindRound != 1 || moved.Version != 3 {
		t.Fatalf("new due date = %+v", moved)
	}
	_, err = f.store.UpdateTodo(ctx, mom, "todo_missing", TodoEdit{Title: "交电费", Version: 1})
	wantErr(t, err, ErrNotFound)
}

func TestTodoRequestKeyRepeats(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	first := must(f.store.AddTodo(ctx, mom, TodoFields{RequestKey: "k1", Title: "交电费"}))
	again := must(f.store.AddTodo(ctx, mom, TodoFields{RequestKey: "k1", Title: "交电费"}))
	if again.ID != first.ID {
		t.Fatalf("repeat = %s, want %s", again.ID, first.ID)
	}
	// The key is the person's own.
	other := must(f.store.AddTodo(ctx, dad, TodoFields{RequestKey: "k1", Title: "交电费"}))
	if other.ID == first.ID {
		t.Fatal("dad's request returned mom's to-do")
	}
	if got := must(f.store.Todos(ctx, mom)); len(got) != 2 {
		t.Fatalf("to-dos = %+v", got)
	}
}
