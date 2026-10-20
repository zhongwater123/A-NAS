// Package personalization keeps the device-wide choices shown under 设置 →
// 个性化. Today that is how long the local console's screen saver plays before
// the screen goes black. The settings live in personalization.db on the
// system disk, beside the Product Service's other state.
package personalization

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

// DefaultSleepAfterMinutes applies until an administrator chooses otherwise.
const DefaultSleepAfterMinutes = 30

// SleepChoices are the minutes the screen may stay on after the screen saver
// starts; 0 keeps it on.
var SleepChoices = []int{0, 10, 30, 60}

var ErrInvalidSetting = errors.New("sleepAfterMinutes must be 0, 10, 30 or 60")

// LocalDisplay is how the screen attached to the NAS behaves.
type LocalDisplay struct {
	SleepAfterMinutes int `json:"sleepAfterMinutes"`
}

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("SQLite path is empty")
	}
	db, err := sql.Open("sqlite3", path+"?_busy_timeout=5000&_journal_mode=WAL")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	const schema = `
CREATE TABLE IF NOT EXISTS local_display (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    sleep_after_minutes INTEGER NOT NULL
);`
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) LocalDisplay(ctx context.Context) (LocalDisplay, error) {
	display := LocalDisplay{SleepAfterMinutes: DefaultSleepAfterMinutes}
	err := s.db.QueryRowContext(ctx, "SELECT sleep_after_minutes FROM local_display WHERE id = 1").Scan(&display.SleepAfterMinutes)
	if errors.Is(err, sql.ErrNoRows) {
		return display, nil
	}
	return display, err
}

func (s *Store) SetLocalDisplay(ctx context.Context, display LocalDisplay) (LocalDisplay, error) {
	if !slices.Contains(SleepChoices, display.SleepAfterMinutes) {
		return LocalDisplay{}, ErrInvalidSetting
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO local_display(id, sleep_after_minutes) VALUES(1, ?)
ON CONFLICT(id) DO UPDATE SET sleep_after_minutes = excluded.sleep_after_minutes`, display.SleepAfterMinutes)
	if err != nil {
		return LocalDisplay{}, err
	}
	return display, nil
}
