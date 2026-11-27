package family

import (
	"testing"
	"time"
)

func TestTodoBoard(t *testing.T) {
	today := time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC)
	created := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	todo := func(id, title, category, due string, change ...func(*Todo)) Todo {
		item := Todo{ID: id, Title: title, Category: category, Due: due, Scope: TodoShared, Status: TodoOpen, CreatedAt: created}
		for _, f := range change {
			f(&item)
		}
		return item
	}
	private := func(item *Todo) { item.Scope = TodoPrivate }
	done := func(item *Todo) { item.Status = TodoDone }
	snoozed := func(until string) func(*Todo) { return func(item *Todo) { item.SnoozedUntil = until } }

	for _, test := range []struct {
		name  string
		todos []Todo
		rows  []BoardRow
		more  int
		pet   string
	}{
		{name: "nothing to do", pet: PetSleep, rows: []BoardRow{}},
		{
			name: "only shared, open, dated to-dos close enough",
			todos: []Todo{
				todo("a", "交电费", "bill", "2026-11-29"),
				todo("b", "交水费", "bill", "2026-11-29", private),
				todo("c", "交燃气费", "bill", "2026-11-28", done),
				todo("d", "修热水器", "other", ""),
				todo("e", "交物业费", "bill", "2026-12-28"), // 31 days: not yet
			},
			rows: []BoardRow{{Text: "交电费", When: "还有 2 天", Urgent: true}},
			pet:  PetIdle,
		},
		{
			name: "documents and contracts show up earlier",
			todos: []Todo{
				todo("a", "换身份证", "id_document", "2027-01-26"), // 60 days
				todo("b", "续租房合同", "contract", "2027-01-11"),   // 45 days
				todo("c", "换护照", "id_document", "2027-01-27"),  // 61 days: not yet
				todo("d", "续保", "insurance", "2026-12-27"),     // 30 days
			},
			rows: []BoardRow{
				{Text: "续保", When: "还有 30 天"},
				{Text: "续租房合同", When: "还有 45 天"},
				{Text: "换身份证", When: "还有 60 天"},
			},
			pet: PetIdle,
		},
		{
			name: "most pressing first, medical by kind only, the rest counted",
			todos: []Todo{
				todo("a", "交电费", "bill", "2026-11-29"),
				todo("b", "儿保复查", "medical", "2026-11-28"),
				todo("c", "交停车费", "bill", "2026-11-24"),
				todo("d", "交网费", "bill", "2026-11-27"),
				todo("e", "续保", "insurance", "2026-12-20"),
			},
			rows: []BoardRow{
				{Text: "交停车费", When: "已过期 3 天", Urgent: true},
				{Text: "交网费", When: "今天截止", Urgent: true},
				{Text: "一件医疗的事", When: "还有 1 天", Urgent: true},
			},
			more: 2,
			pet:  PetBuried,
		},
		{
			name: "snoozed past today stays off; due back today shows",
			todos: []Todo{
				todo("a", "交电费", "bill", "2026-11-28", snoozed("2026-11-30")),
				todo("b", "交水费", "bill", "2026-11-28", snoozed("2026-11-27")),
			},
			rows: []BoardRow{{Text: "交水费", When: "还有 1 天", Urgent: true}},
			pet:  PetIdle,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			board := TodoBoard(test.todos, today)
			if len(board.Rows) != len(test.rows) {
				t.Fatalf("rows = %+v, want %+v", board.Rows, test.rows)
			}
			for i := range test.rows {
				if board.Rows[i] != test.rows[i] {
					t.Errorf("row %d = %+v, want %+v", i, board.Rows[i], test.rows[i])
				}
			}
			if board.More != test.more || board.Pet != test.pet {
				t.Errorf("more, pet = %d, %q; want %d, %q", board.More, board.Pet, test.more, test.pet)
			}
		})
	}
}

func TestTodoBoardKey(t *testing.T) {
	today := time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC)
	bill := Todo{ID: "a", Title: "交电费", Category: "bill", Due: "2026-11-29", Scope: TodoShared, Status: TodoOpen}
	first := TodoBoard([]Todo{bill}, today).Key()
	if again := TodoBoard([]Todo{bill}, today).Key(); again != first || len(first) != 16 {
		t.Fatalf("the same words gave %q and %q", first, again)
	}
	edited := bill
	edited.AmountCents = new(int64) // not on the screen
	edited.OwnerName = "alice"
	if TodoBoard([]Todo{edited}, today).Key() != first {
		t.Error("an amount or owner, which the screen does not show, changed the key")
	}
	if TodoBoard([]Todo{bill}, today.AddDate(0, 0, 1)).Key() == first {
		t.Error("a new day changed the words but kept the key")
	}
	if TodoBoard(nil, today).Key() == first {
		t.Error("an empty board has the key of a full one")
	}
}
