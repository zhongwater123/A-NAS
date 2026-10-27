// Package sqlitedb opens the services' SQLite databases one way (#222):
// WAL, foreign keys, a 5 s busy timeout and one connection, so that writers
// queue in database/sql instead of failing with SQLITE_BUSY.
package sqlitedb

import (
	"database/sql"
	"errors"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

// Options are a store's own choices.
type Options struct {
	// Durable makes a commit wait until it is on disk (synchronous=FULL).
	// Otherwise WAL's NORMAL may lose the last commits on a power cut, but
	// never consistency.
	Durable bool
	// AutoVacuum returns the space of deleted rows to the file system. It
	// applies to databases created with it.
	AutoVacuum bool
}

// Open opens, and creates when missing, the database at path.
func Open(path string, options Options) (*sql.DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("SQLite path is empty")
	}
	query := "?_foreign_keys=on&_busy_timeout=5000&_journal_mode=WAL"
	if options.Durable {
		query += "&_synchronous=FULL"
	}
	if options.AutoVacuum {
		query += "&_auto_vacuum=full"
	}
	db, err := sql.Open("sqlite3", path+query)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// OpenReadOnly opens another process's database without writing it or
// creating SQLite side files next to it.
func OpenReadOnly(path string) (*sql.DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("SQLite path is empty")
	}
	db, err := sql.Open("sqlite3", "file:"+path+"?mode=ro&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}
