package family

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// E-ink screens (docs/specs/eink-screens.md, “配对一块屏”). An administrator
// pairs a screen and hands it a device token; the store keeps only the
// token's SHA-256, so a screen proves who it is and nothing else. The token
// belongs to the screen, not to an account: disabling the administrator who
// paired it leaves the screen working.

// maxScreenName bounds a screen's name in characters, such as 客厅.
const maxScreenName = 6

// ErrScreenToken is returned for a device token no paired screen holds.
var ErrScreenToken = errors.New("unknown device token")

// Screen is a paired e-ink screen and what it last said about itself.
type Screen struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	// Shown is the key of the picture the screen draws now; empty before it
	// first asked.
	Shown string `json:"-"`
	// SeenAt is when the screen last asked; nil before its first question.
	SeenAt *time.Time `json:"seenAt,omitempty"`
	// NextAt is when the screen was told to ask again.
	NextAt *time.Time    `json:"nextAt,omitempty"`
	Report *ScreenReport `json:"report,omitempty"`
}

// ScreenReport is what a screen reports when it asks. A field the screen
// did not send, or sent malformed, is nil.
type ScreenReport struct {
	Battery   *int   `json:"battery,omitempty"`
	Millivolt *int   `json:"millivolt,omitempty"`
	Charging  *bool  `json:"charging,omitempty"`
	USB       *bool  `json:"usb,omitempty"`
	Firmware  string `json:"firmware,omitempty"`
	RSSI      *int   `json:"rssi,omitempty"`
	AwakeMs   *int64 `json:"awakeMs,omitempty"`
	Wake      string `json:"wake,omitempty"`
	Reset     string `json:"reset,omitempty"`
	Fail      string `json:"fail,omitempty"`
	// History is the screen's last few wakes, as the firmware writes them.
	History string `json:"history,omitempty"`
}

const screenColumns = `id, name, created_at, shown, seen_at, next_at, report`

func scanScreen(row interface{ Scan(...any) error }) (Screen, error) {
	var screen Screen
	var created string
	var seen, next sql.NullString
	var report string
	if err := row.Scan(&screen.ID, &screen.Name, &created, &screen.Shown, &seen, &next, &report); err != nil {
		return Screen{}, err
	}
	screen.CreatedAt = parseTime(created)
	if seen.Valid {
		at := parseTime(seen.String)
		screen.SeenAt = &at
	}
	if next.Valid {
		at := parseTime(next.String)
		screen.NextAt = &at
	}
	if report != "" && report != "{}" {
		var r ScreenReport
		if json.Unmarshal([]byte(report), &r) == nil {
			screen.Report = &r
		}
	}
	return screen, nil
}

func checkScreenName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	n := utf8.RuneCountInString(name)
	return name, utf8.ValidString(name) && n >= 1 && n <= maxScreenName && !strings.ContainsAny(name, "\x00\r\n\t")
}

// hashScreenToken is what the store keeps of a device token.
func hashScreenToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Screens lists the paired screens, oldest first.
func (s *Store) Screens(ctx context.Context) ([]Screen, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+screenColumns+" FROM eink_screens ORDER BY created_at, rowid")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	screens := []Screen{}
	for rows.Next() {
		screen, err := scanScreen(rows)
		if err != nil {
			return nil, err
		}
		screens = append(screens, screen)
	}
	return screens, rows.Err()
}

// AddScreen pairs a screen named name and returns it with its device token,
// which is never shown again. Names are unique.
func (s *Store) AddScreen(ctx context.Context, actor Actor, name string) (Screen, string, error) {
	name, ok := checkScreenName(name)
	if !ok {
		return Screen{}, "", ErrInvalid
	}
	token := rand.Text() + rand.Text()
	id := newID("scr")
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var taken int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM eink_screens WHERE name = ?", name).Scan(&taken); err != nil {
			return err
		}
		if taken > 0 {
			return ErrConflict
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO eink_screens(id, name, token_hash, created_at, created_by) VALUES (?, ?, ?, ?, ?)`,
			id, name, hashScreenToken(token), formatTime(s.now()), actor.AccountID)
		return err
	})
	if err != nil {
		return Screen{}, "", err
	}
	screen, err := s.screen(ctx, s.db, id)
	return screen, token, err
}

// RenameScreen renames a paired screen.
func (s *Store) RenameScreen(ctx context.Context, id, name string) (Screen, error) {
	name, ok := checkScreenName(name)
	if !ok {
		return Screen{}, ErrInvalid
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var other int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM eink_screens WHERE name = ? AND id <> ?", name, id).Scan(&other); err != nil {
			return err
		}
		if other > 0 {
			return ErrConflict
		}
		result, err := tx.ExecContext(ctx, "UPDATE eink_screens SET name = ? WHERE id = ?", name, id)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return Screen{}, err
	}
	return s.screen(ctx, s.db, id)
}

// RemoveScreen unpairs a screen; its token stops working at once.
func (s *Store) RemoveScreen(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM eink_screens WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) screen(ctx context.Context, q queryer, id string) (Screen, error) {
	screen, err := scanScreen(q.QueryRowContext(ctx, "SELECT "+screenColumns+" FROM eink_screens WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Screen{}, ErrNotFound
	}
	return screen, err
}

// ScreenByToken finds the screen a device token belongs to.
func (s *Store) ScreenByToken(ctx context.Context, token string) (Screen, error) {
	if token == "" {
		return Screen{}, ErrScreenToken
	}
	screen, err := scanScreen(s.db.QueryRowContext(ctx, "SELECT "+screenColumns+" FROM eink_screens WHERE token_hash = ?",
		hashScreenToken(token)))
	if errors.Is(err, sql.ErrNoRows) {
		return Screen{}, ErrScreenToken
	}
	return screen, err
}

// RecordAsk keeps what a screen said and what it was told: the picture it
// now shows, when it asked, when to ask again and its report. A field the
// screen did not report keeps its last value, except the history of wakes,
// which is only what this question carried: firmware that stops sending it
// leaves none behind. A screen removed meanwhile is not brought back.
func (s *Store) RecordAsk(ctx context.Context, id, shown string, seen, next time.Time, report ScreenReport) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		screen, err := s.screen(ctx, tx, id)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if screen.Report != nil {
			report = report.over(*screen.Report)
		}
		encoded, err := json.Marshal(report)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE eink_screens SET shown = ?, seen_at = ?, next_at = ?, report = ? WHERE id = ?",
			shown, formatTime(seen), formatTime(next), string(encoded), id)
		return err
	})
}

// over fills what r leaves out from old, all but the history.
func (r ScreenReport) over(old ScreenReport) ScreenReport {
	r.Battery = cmp.Or(r.Battery, old.Battery)
	r.Millivolt = cmp.Or(r.Millivolt, old.Millivolt)
	r.Charging = cmp.Or(r.Charging, old.Charging)
	r.USB = cmp.Or(r.USB, old.USB)
	r.Firmware = cmp.Or(r.Firmware, old.Firmware)
	r.RSSI = cmp.Or(r.RSSI, old.RSSI)
	r.AwakeMs = cmp.Or(r.AwakeMs, old.AwakeMs)
	r.Wake = cmp.Or(r.Wake, old.Wake)
	r.Reset = cmp.Or(r.Reset, old.Reset)
	r.Fail = cmp.Or(r.Fail, old.Fail)
	return r
}

// ScreenTodos lists what a shared screen may show: open shared to-dos with a
// due date. TodoBoard picks from them.
func (s *Store) ScreenTodos(ctx context.Context) ([]Todo, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+todoColumns+
		" FROM todos WHERE viewer_id = '' AND status = 'open' AND due_on IS NOT NULL ORDER BY due_on, created_at, id")
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

// Today is the family's calendar date, as boards count days.
func (s *Store) Today() time.Time { return s.today() }

// Now is the store's clock, which screens set theirs from.
func (s *Store) Now() time.Time { return s.now() }
