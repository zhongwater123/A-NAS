package family

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// A to-do is open until someone marks it done or ignores it; either can be
// undone by opening it again.
const (
	TodoOpen    = "open"
	TodoDone    = "done"
	TodoIgnored = "ignored"
)

// TodoShared is seen and changed by every active account; TodoPrivate by the
// one account it belongs to. To-dos added by hand are always shared.
const (
	TodoShared  = "shared"
	TodoPrivate = "private"
)

const (
	// TodoManual is where a to-do added by hand comes from.
	TodoManual = "manual"
	// maxTodoTitle leaves room for a shared screen to show the title whole.
	maxTodoTitle = 10
	// ClosedTodoDays is how long a done or ignored to-do stays listed.
	ClosedTodoDays = 30
	// MaxSnoozeDays bounds how far a snooze reaches.
	MaxSnoozeDays = 30
	// maxTodoAmount keeps an amount exact as a JavaScript number.
	maxTodoAmount = 1<<53 - 1
)

// todoCategories name what kind of matter a to-do is; pages derive the
// action from it, such as paying a bill.
var todoCategories = []string{"bill", "medical", "insurance", "id_document", "contract", "other"}

// Todo is one thing the family has to do. Who added it, who closed it and
// whom it is for are kept by account ID and by the name they had then, which
// pages show once the account is gone.
type Todo struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Category string `json:"category"`
	Due      string `json:"due,omitempty"`
	// OwnerID is whom it is for, only for display; empty means the family.
	OwnerID   string `json:"ownerId,omitempty"`
	OwnerName string `json:"ownerName,omitempty"`
	// AmountCents is in fen of renminbi.
	AmountCents *int64 `json:"amountCents,omitempty"`
	Scope       string `json:"scope"`
	Status      string `json:"status"`
	// SnoozedUntil is the day reminders resume.
	SnoozedUntil string `json:"snoozedUntil,omitempty"`
	// RemindRound grows whenever the reminders start over.
	RemindRound int64  `json:"remindRound"`
	Source      string `json:"source"`
	// Version counts edits; the status and snoozes leave it.
	Version       int64      `json:"version"`
	CreatedAt     time.Time  `json:"createdAt"`
	CreatedByID   string     `json:"createdById"`
	CreatedByName string     `json:"createdByName"`
	UpdatedAt     *time.Time `json:"updatedAt,omitempty"`
	ClosedAt      *time.Time `json:"closedAt,omitempty"`
	ClosedByID    string     `json:"closedById,omitempty"`
	ClosedByName  string     `json:"closedByName,omitempty"`
}

// TodoFields is a new to-do. Repeating a request with the same RequestKey
// returns the first to-do.
type TodoFields struct {
	RequestKey  string `json:"requestKey,omitempty"`
	Title       string `json:"title"`
	Category    string `json:"category,omitempty"`
	Due         string `json:"due,omitempty"`
	OwnerID     string `json:"ownerId,omitempty"`
	AmountCents *int64 `json:"amountCents,omitempty"`
}

// TodoEdit replaces a to-do's fields, clearing those left out. Version is
// the version the edit was made from.
type TodoEdit struct {
	Title       string `json:"title"`
	Category    string `json:"category,omitempty"`
	Due         string `json:"due,omitempty"`
	OwnerID     string `json:"ownerId,omitempty"`
	AmountCents *int64 `json:"amountCents,omitempty"`
	Version     int64  `json:"version"`
}

// todoValues are the checked fields both a new to-do and an edit store.
type todoValues struct {
	title, category, due, ownerID string
	amount                        sql.NullInt64
}

// checkTodo cleans the fields a person fills in. The owner is checked
// against the active accounts by the caller, which knows whether it changed.
func checkTodo(title, category, due, ownerID string, amount *int64) (todoValues, bool) {
	title, ok := cleanName(title, true)
	if !ok || utf8.RuneCountInString(title) > maxTodoTitle {
		return todoValues{}, false
	}
	if category == "" {
		category = "other"
	}
	if !slices.Contains(todoCategories, category) {
		return todoValues{}, false
	}
	if due != "" {
		if _, ok := parseDate(due); !ok {
			return todoValues{}, false
		}
	}
	values := todoValues{title: title, category: category, due: due, ownerID: ownerID}
	if amount != nil {
		if *amount < 0 || *amount > maxTodoAmount {
			return todoValues{}, false
		}
		values.amount = sql.NullInt64{Int64: *amount, Valid: true}
	}
	return values, true
}

// TodoOwner is an active account a to-do can be given to.
type TodoOwner struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

// TodoOwners lists the active accounts a to-do can be given to, by
// username. Disabled accounts and those waiting to be deleted are left out:
// a to-do cannot be given to them.
func (s *Store) TodoOwners(ctx context.Context) ([]TodoOwner, error) {
	accounts, err := s.accounts(ctx)
	if err != nil {
		return nil, err
	}
	owners := make([]TodoOwner, 0, len(accounts))
	for _, account := range accounts {
		owners = append(owners, TodoOwner(account))
	}
	slices.SortFunc(owners, func(a, b TodoOwner) int { return strings.Compare(a.Username, b.Username) })
	return owners, nil
}

// activeOwner is the username of the active account ownerID, and whether it
// is one. It asks the accounts service, so it runs before a transaction:
// family.db has one connection, and the question would hold it.
func (s *Store) activeOwner(ctx context.Context, ownerID string) (string, bool, error) {
	if ownerID == "" {
		return "", true, nil
	}
	accounts, err := s.accounts(ctx)
	if err != nil {
		return "", false, err
	}
	account, ok := accounts[ownerID]
	return account.Username, ok, nil
}

const todoColumns = `id, title, category, due_on, owner_id, owner_name, amount_cents, viewer_id, status, snoozed_until,
remind_round, source, version, created_at, created_by, created_by_name, updated_at, closed_at, closed_by, closed_by_name`

func scanTodo(row interface{ Scan(...any) error }) (Todo, error) {
	var todo Todo
	var due, snoozed, updated, closed, closedBy, closedByName sql.NullString
	var amount sql.NullInt64
	var viewer, created string
	if err := row.Scan(&todo.ID, &todo.Title, &todo.Category, &due, &todo.OwnerID, &todo.OwnerName, &amount, &viewer,
		&todo.Status, &snoozed, &todo.RemindRound, &todo.Source, &todo.Version, &created, &todo.CreatedByID,
		&todo.CreatedByName, &updated, &closed, &closedBy, &closedByName); err != nil {
		return Todo{}, err
	}
	todo.Due, todo.SnoozedUntil = due.String, snoozed.String
	if amount.Valid {
		todo.AmountCents = &amount.Int64
	}
	todo.Scope = TodoShared
	if viewer != "" {
		todo.Scope = TodoPrivate
	}
	todo.CreatedAt = parseTime(created)
	todo.UpdatedAt = parseOptionalTime(updated)
	todo.ClosedAt = parseOptionalTime(closed)
	todo.ClosedByID, todo.ClosedByName = closedBy.String, closedByName.String
	if todo.OwnerID == "" {
		todo.OwnerName = ""
	}
	return todo, nil
}

// visibleTodos is the condition every read and change of to-dos shares: a
// shared to-do, or one of the caller's own.
const visibleTodos = "(viewer_id = '' OR viewer_id = ?)"

// visibleTodo reads a to-do the caller can see. Any other answers
// ErrNotFound, so a person's own to-dos do not show that they exist.
func visibleTodo(ctx context.Context, q queryer, actor Actor, id string) (Todo, error) {
	todo, err := scanTodo(q.QueryRowContext(ctx, "SELECT "+todoColumns+" FROM todos WHERE id = ? AND "+visibleTodos, id, actor.AccountID))
	if errors.Is(err, sql.ErrNoRows) {
		return Todo{}, ErrNotFound
	}
	return todo, err
}

// Todos lists the to-dos the caller can see: the open ones by due date,
// those without one last, then those closed in the last ClosedTodoDays,
// most recently closed first. Any active account may read them; joining the
// family is not needed.
func (s *Store) Todos(ctx context.Context, actor Actor) ([]Todo, error) {
	cutoff := formatTime(s.now().AddDate(0, 0, -ClosedTodoDays))
	rows, err := s.db.QueryContext(ctx, "SELECT "+todoColumns+" FROM todos WHERE "+visibleTodos+`
AND (status = 'open' OR closed_at > ?)
ORDER BY status <> 'open', CASE WHEN status = 'open' THEN due_on IS NULL END, CASE WHEN status = 'open' THEN due_on END,
closed_at DESC, created_at, id`,
		actor.AccountID, cutoff)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	todos := []Todo{}
	for rows.Next() {
		todo, err := scanTodo(rows)
		if err != nil {
			return nil, err
		}
		todos = append(todos, todo)
	}
	return todos, rows.Err()
}

// AddTodo adds a to-do by hand, shared with every active account.
func (s *Store) AddTodo(ctx context.Context, actor Actor, fields TodoFields) (Todo, error) {
	return s.addTodo(ctx, actor, fields, "")
}

// addTodo adds a to-do that viewerID alone sees, or everyone when it is
// empty. Only to-dos from a person's own sources will be private; nothing
// adds them yet.
func (s *Store) addTodo(ctx context.Context, actor Actor, fields TodoFields, viewerID string) (Todo, error) {
	values, ok := checkTodo(fields.Title, fields.Category, fields.Due, fields.OwnerID, fields.AmountCents)
	if !ok || len(fields.RequestKey) > maxRequestKey || !utf8.ValidString(fields.RequestKey) {
		return Todo{}, ErrInvalid
	}
	ownerName, active, err := s.activeOwner(ctx, values.ownerID)
	if err != nil {
		return Todo{}, err
	}
	if !active {
		return Todo{}, ErrInvalid
	}
	var todo Todo
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		if fields.RequestKey != "" {
			existing, err := s.todoByRequest(ctx, tx, actor, fields.RequestKey)
			if err == nil {
				todo = existing
				return nil
			}
			if !errors.Is(err, ErrNotFound) {
				return err
			}
		}
		id := newID("todo")
		if _, err := tx.ExecContext(ctx, `INSERT INTO todos(id, title, category, due_on, owner_id, owner_name, amount_cents,
viewer_id, status, source, created_by, created_by_name, created_at, request_key)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, values.title, values.category, nullString(values.due), values.ownerID, ownerName, values.amount,
			viewerID, TodoOpen, TodoManual, actor.AccountID, actor.Username, formatTime(s.now()), nullString(fields.RequestKey)); err != nil {
			if isUniqueConstraint(err) {
				return ErrConflict
			}
			return err
		}
		if todo, err = visibleTodo(ctx, tx, actor, id); err != nil {
			return err
		}
		return s.auditAs(ctx, tx, actor, "todo", "family.todo_added", id, todo.Title)
	})
	if errors.Is(err, ErrConflict) && fields.RequestKey != "" {
		// Another request with the key won the race; answer with its to-do.
		return s.todoByRequest(ctx, s.db, actor, fields.RequestKey)
	}
	return todo, err
}

func (s *Store) todoByRequest(ctx context.Context, q queryer, actor Actor, key string) (Todo, error) {
	todo, err := scanTodo(q.QueryRowContext(ctx, "SELECT "+todoColumns+" FROM todos WHERE created_by = ? AND request_key = ?",
		actor.AccountID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return Todo{}, ErrNotFound
	}
	return todo, err
}

// UpdateTodo replaces a to-do's fields. Someone editing from a stale copy
// gets ErrChanged. A new due date starts the reminders over and ends a
// snooze, as opening it again does. Only a new owner must be an active
// account, so a to-do whose owner left can still be edited. A done or
// ignored to-do can be edited too.
func (s *Store) UpdateTodo(ctx context.Context, actor Actor, id string, edit TodoEdit) (Todo, error) {
	values, ok := checkTodo(edit.Title, edit.Category, edit.Due, edit.OwnerID, edit.AmountCents)
	if !ok || edit.Version < 1 {
		return Todo{}, ErrInvalid
	}
	newOwnerName, active, err := s.activeOwner(ctx, values.ownerID)
	if err != nil {
		return Todo{}, err
	}
	var todo Todo
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		current, err := visibleTodo(ctx, tx, actor, id)
		if err != nil {
			return err
		}
		if current.Version != edit.Version {
			return ErrChanged
		}
		ownerName := current.OwnerName
		if values.ownerID != current.OwnerID {
			if !active {
				return ErrInvalid
			}
			ownerName = newOwnerName
		}
		round, snoozed := current.RemindRound, current.SnoozedUntil
		if values.due != current.Due {
			round, snoozed = round+1, ""
		}
		if _, err := tx.ExecContext(ctx, `UPDATE todos SET title = ?, category = ?, due_on = ?, owner_id = ?, owner_name = ?,
amount_cents = ?, snoozed_until = ?, remind_round = ?, version = version + 1, updated_at = ? WHERE id = ?`,
			values.title, values.category, nullString(values.due), values.ownerID, ownerName, values.amount,
			nullString(snoozed), round, formatTime(s.now()), id); err != nil {
			return err
		}
		if todo, err = visibleTodo(ctx, tx, actor, id); err != nil {
			return err
		}
		return s.auditAs(ctx, tx, actor, "todo", "family.todo_updated", id, todo.Title)
	})
	return todo, err
}

// SetTodoStatus marks a to-do done or ignored, recording who and when, or
// opens it again, which clears the snooze and starts the reminders over.
// Setting the status it has changes nothing, so the first to close it stays
// recorded; switching between done and ignored records the new closer. This
// is not an edit and keeps the version.
func (s *Store) SetTodoStatus(ctx context.Context, actor Actor, id, status string) (Todo, error) {
	if status != TodoOpen && status != TodoDone && status != TodoIgnored {
		return Todo{}, ErrInvalid
	}
	var todo Todo
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		current, err := visibleTodo(ctx, tx, actor, id)
		if err != nil {
			return err
		}
		if current.Status == status {
			todo = current
			return nil
		}
		action := "family.todo_reopened"
		if status == TodoOpen {
			_, err = tx.ExecContext(ctx, `UPDATE todos SET status = ?, snoozed_until = NULL, closed_at = NULL, closed_by = NULL,
closed_by_name = NULL, remind_round = remind_round + 1 WHERE id = ?`, status, id)
		} else {
			action = "family.todo_" + status
			_, err = tx.ExecContext(ctx, `UPDATE todos SET status = ?, snoozed_until = NULL, closed_at = ?, closed_by = ?,
closed_by_name = ? WHERE id = ?`, status, formatTime(s.now()), actor.AccountID, actor.Username, id)
		}
		if err != nil {
			return err
		}
		if todo, err = visibleTodo(ctx, tx, actor, id); err != nil {
			return err
		}
		return s.auditAs(ctx, tx, actor, "todo", action, id, todo.Title)
	})
	return todo, err
}

// SnoozeTodo quiets an open to-do's reminders until today plus days, by the
// device's calendar; they start over on that day. A done or ignored to-do
// answers ErrConflict.
func (s *Store) SnoozeTodo(ctx context.Context, actor Actor, id string, days int) (Todo, error) {
	if days < 1 || days > MaxSnoozeDays {
		return Todo{}, ErrInvalid
	}
	until := s.today().AddDate(0, 0, days).Format(dateLayout)
	var todo Todo
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		current, err := visibleTodo(ctx, tx, actor, id)
		if err != nil {
			return err
		}
		if current.Status != TodoOpen {
			return ErrConflict
		}
		if _, err := tx.ExecContext(ctx, "UPDATE todos SET snoozed_until = ?, remind_round = remind_round + 1 WHERE id = ?",
			until, id); err != nil {
			return err
		}
		if todo, err = visibleTodo(ctx, tx, actor, id); err != nil {
			return err
		}
		return s.auditAs(ctx, tx, actor, "todo", "family.todo_snoozed", id, todo.Title)
	})
	return todo, err
}
