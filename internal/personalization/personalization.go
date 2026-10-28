// Package personalization keeps the device-wide choices shown under 设置 →
// 个性化: the desktop wallpaper every account sees, and how long the local
// console's screen saver plays before the screen goes black. The settings and
// the wallpaper picture live in personalization.db on the system disk, beside
// the Product Service's other state.
package personalization

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	_ "github.com/mattn/go-sqlite3"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
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
	db, err := sqlitedb.Open(path, sqlitedb.Options{})
	if err != nil {
		return nil, err
	}
	const schema = `
CREATE TABLE IF NOT EXISTS local_display (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    sleep_after_minutes INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS wallpaper (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    style TEXT NOT NULL,
    image BLOB,
    version TEXT NOT NULL DEFAULT '',
    width INTEGER NOT NULL DEFAULT 0,
    height INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL DEFAULT ''
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
