// Package sqlitetest cuts SQLite reads short, as a failing disk or a corrupt
// page would. database/sql reports such a failure only through Rows.Err:
// Next returns false and closes the rows, so a later Close returns nil and a
// loop that checks only Close takes the rows it read for all of them (#269).
package sqlitetest

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	sqlite3 "github.com/mattn/go-sqlite3"
)

// ErrCut is the error a read that was cut short fails with.
var ErrCut = errors.New("sqlitetest: read cut short")

var registered atomic.Int64

// Register registers a database/sql driver that opens SQLite databases as
// the sqlite3 driver does, except that a query whose text contains match,
// run directly or as a prepared statement, returns its first after rows and
// then fails with ErrCut. It returns the driver's name for sql.Open.
func Register(match string, after int) string {
	name := fmt.Sprintf("sqlite3-cut-%d", registered.Add(1))
	sql.Register(name, cutDriver{match: match, after: after})
	return name
}

type cutDriver struct {
	match string
	after int
}

func (d cutDriver) Open(dsn string) (driver.Conn, error) {
	conn, err := (&sqlite3.SQLiteDriver{}).Open(dsn)
	if err != nil {
		return nil, err
	}
	return &cutConn{SQLiteConn: conn.(*sqlite3.SQLiteConn), cut: d}, nil
}

// cutConn is a sqlite3 connection, with all its methods, whose matching
// queries are cut.
type cutConn struct {
	*sqlite3.SQLiteConn
	cut cutDriver
}

func (c *cutConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.SQLiteConn.QueryContext(ctx, query, args)
	if err != nil || !strings.Contains(query, c.cut.match) {
		return rows, err
	}
	return &cutRows{Rows: rows, left: c.cut.after}, nil
}

func (c *cutConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	stmt, err := c.SQLiteConn.PrepareContext(ctx, query)
	if err != nil || !strings.Contains(query, c.cut.match) {
		return stmt, err
	}
	return &cutStmt{SQLiteStmt: stmt.(*sqlite3.SQLiteStmt), after: c.cut.after}, nil
}

// cutStmt is a prepared statement, with all its methods, whose queries are
// cut.
type cutStmt struct {
	*sqlite3.SQLiteStmt
	after int
}

func (s *cutStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := s.SQLiteStmt.QueryContext(ctx, args)
	if err != nil {
		return nil, err
	}
	return &cutRows{Rows: rows, left: s.after}, nil
}

type cutRows struct {
	driver.Rows
	left int
}

func (r *cutRows) Next(dest []driver.Value) error {
	if r.left == 0 {
		return ErrCut
	}
	r.left--
	return r.Rows.Next(dest)
}
