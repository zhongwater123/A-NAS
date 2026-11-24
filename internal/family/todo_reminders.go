package family

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// RemindDays is how many days before its due date a to-do is coming up.
const RemindDays = 3

// The stages of a to-do's reminders. Each is told once a round: coming up,
// due today, overdue.
const (
	StageSoon    = "soon"
	StageToday   = "today"
	StageOverdue = "overdue"
)

// todoKeyPrefix starts the key of every to-do reminder in hints_seen.
const todoKeyPrefix = "todo:"

// TodoReminder is a to-do the caller is to be told about now. Pages word it
// from these fields.
type TodoReminder struct {
	Key      string `json:"key"`
	TodoID   string `json:"todoId"`
	Stage    string `json:"stage"`
	Title    string `json:"title"`
	Category string `json:"category"`
	Due      string `json:"due"`
	Scope    string `json:"scope"`
	OwnerID  string `json:"ownerId,omitempty"`
}

// remindStage is the stage a to-do is at on today, or "" when it reminds of
// nothing: it is closed, has no due date, is snoozed past today or is due
// later than RemindDays from now.
func remindStage(todo Todo, today time.Time) string {
	if todo.Status != TodoOpen || todo.Due == "" {
		return ""
	}
	if todo.SnoozedUntil != "" {
		if until, ok := parseDate(todo.SnoozedUntil); ok && until.After(today) {
			return ""
		}
	}
	due, ok := parseDate(todo.Due)
	if !ok {
		return ""
	}
	switch days := int(due.Sub(today).Hours() / 24); {
	case days < 0:
		return StageOverdue
	case days == 0:
		return StageToday
	case days <= RemindDays:
		return StageSoon
	}
	return ""
}

// reminderKey names a stage of a round. Snoozing, opening again and a new
// due date start a new round, so what was acknowledged before no longer
// matches.
func reminderKey(id string, round int64, stage string) string {
	return todoKeyPrefix + id + ":" + strconv.FormatInt(round, 10) + ":" + stage
}

// dueReminders lists every reminder the caller's to-dos give today,
// acknowledged or not, by due date.
func (s *Store) dueReminders(ctx context.Context, q queryer, actor Actor) ([]TodoReminder, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+todoColumns+" FROM todos WHERE "+visibleTodos+
		" AND status = 'open' AND due_on IS NOT NULL ORDER BY due_on, created_at, id", actor.AccountID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	today := s.today()
	reminders := []TodoReminder{}
	for rows.Next() {
		todo, err := scanTodo(rows)
		if err != nil {
			return nil, err
		}
		stage := remindStage(todo, today)
		if stage == "" {
			continue
		}
		reminders = append(reminders, TodoReminder{Key: reminderKey(todo.ID, todo.RemindRound, stage), TodoID: todo.ID,
			Stage: stage, Title: todo.Title, Category: todo.Category, Due: todo.Due, Scope: todo.Scope, OwnerID: todo.OwnerID})
	}
	return reminders, rows.Err()
}

// TodoReminders lists what the caller has not acknowledged yet. Everyone
// who sees a to-do is reminded of it, each acknowledging for themselves;
// joining the family is not needed. Nothing runs in the background: the
// stages follow from today's date whenever someone asks.
func (s *Store) TodoReminders(ctx context.Context, actor Actor) ([]TodoReminder, error) {
	due, err := s.dueReminders(ctx, s.db, actor)
	if err != nil {
		return nil, err
	}
	seen, err := s.seenTodoKeys(ctx, actor, due)
	if err != nil {
		return nil, err
	}
	reminders := []TodoReminder{}
	for _, reminder := range due {
		if !seen[reminder.Key] {
			reminders = append(reminders, reminder)
		}
	}
	return reminders, nil
}

// seenTodoKeys says which of these reminders the caller acknowledged. Only
// their keys are read, not every key the caller ever acknowledged.
func (s *Store) seenTodoKeys(ctx context.Context, actor Actor, due []TodoReminder) (map[string]bool, error) {
	seen := map[string]bool{}
	if len(due) == 0 {
		return seen, nil
	}
	args := []any{actor.AccountID}
	for _, reminder := range due {
		args = append(args, reminder.Key)
	}
	rows, err := s.db.QueryContext(ctx, "SELECT hint_key FROM hints_seen WHERE account_id = ? AND hint_key IN (?"+
		strings.Repeat(", ?", len(due)-1)+")", args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		seen[key] = true
	}
	return seen, rows.Err()
}

// purgeTodoKeys forgets the acknowledgements of to-dos closed more than
// ClosedTodoDays ago, which no longer list or remind. Opening one again
// starts a new round, whose keys are new.
func purgeTodoKeys(ctx context.Context, tx *sql.Tx, cutoff string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM hints_seen WHERE hint_key LIKE 'todo:%'
AND substr(hint_key, 6, instr(substr(hint_key, 6), ':') - 1) IN (SELECT id FROM todos WHERE status <> 'open' AND closed_at <= ?)`,
		cutoff)
	return err
}

// AcknowledgeTodoReminders records that the caller acknowledged these
// reminders, for them alone. Only keys their reminders give now are
// recorded; the others are ignored, so a to-do someone closed meanwhile
// does not fail the request, and nobody can fill hints_seen with keys of
// their own making. Keys that are not to-do keys are ErrInvalid.
func (s *Store) AcknowledgeTodoReminders(ctx context.Context, actor Actor, keys []string) error {
	if len(keys) > maxHintKeys {
		return ErrInvalid
	}
	for _, key := range keys {
		if len(key) > maxHintKeyBytes || !utf8.ValidString(key) || !strings.HasPrefix(key, todoKeyPrefix) {
			return ErrInvalid
		}
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		due, err := s.dueReminders(ctx, tx, actor)
		if err != nil {
			return err
		}
		current := map[string]bool{}
		for _, reminder := range due {
			current[reminder.Key] = true
		}
		now := formatTime(s.now())
		for _, key := range keys {
			if !current[key] {
				continue
			}
			if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO hints_seen(account_id, hint_key, seen_at) VALUES(?, ?, ?)",
				actor.AccountID, key, now); err != nil {
				return err
			}
		}
		return nil
	})
}
