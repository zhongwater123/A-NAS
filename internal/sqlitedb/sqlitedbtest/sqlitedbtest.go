// Package sqlitedbtest checks a store's schema steps in its tests.
package sqlitedbtest

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// CheckReversible checks every step that has a Down: a database the whole
// schema made, opened by a build that lacks the step, has the structure a
// new database of that build has; opened again by the whole schema, the
// structure it had at first. open opens a database file as the store does.
func CheckReversible(t testing.TB, schema sqlitedb.Schema, open func(path string) (*sql.DB, error)) {
	t.Helper()
	dir := t.TempDir()
	at := func(path string, steps int) string {
		t.Helper()
		db, err := open(path)
		if err != nil {
			t.Fatalf("open %s: %v", path, err)
		}
		defer func() { _ = db.Close() }()
		build := schema
		build.Steps = schema.Steps[:steps]
		if err := sqlitedb.Migrate(context.Background(), db, build); err != nil {
			t.Fatalf("migrate %s with %d steps: %v", filepath.Base(path), steps, err)
		}
		return Structure(t, db)
	}
	whole := filepath.Join(dir, "whole.db")
	want := at(whole, len(schema.Steps))
	for steps := len(schema.Steps) - 1; steps >= schema.WithoutDown; steps-- {
		fresh := at(filepath.Join(dir, fmt.Sprintf("new-%d.db", steps)), steps)
		if got := at(whole, steps); got != fresh {
			t.Errorf("%s: the Down of step %d leaves\n%s\nnot the structure of version %d:\n%s", schema.Name, steps+1, got, steps, fresh)
		}
	}
	if got := at(whole, len(schema.Steps)); got != want {
		t.Errorf("%s: applied again, the steps leave\n%s\nnot\n%s", schema.Name, got, want)
	}
}

// Structure describes a database's version, tables with their columns, and
// its indexes, triggers and views, leaving out the stored Downs.
func Structure(t testing.TB, db *sql.DB) string {
	t.Helper()
	var out strings.Builder
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(&out, "version %d\n", version)
	objects := rowsOf(t, db, 3, `SELECT type, name, IFNULL(sql, '') FROM sqlite_master
WHERE name NOT LIKE 'sqlite_%' AND name <> 'schema_downgrades' ORDER BY type, name`)
	for _, o := range objects {
		kind, name, statement := o[0], o[1], o[2]
		if kind != "table" {
			// SQLite keeps an index's, trigger's or view's statement as written.
			fmt.Fprintf(&out, "%s %s: %s\n", kind, name, strings.Join(strings.Fields(statement), " "))
			continue
		}
		// ALTER TABLE rewrites a table's statement, so its columns stand for it.
		fmt.Fprintf(&out, "table %s\n", name)
		for _, c := range rowsOf(t, db, 6, `SELECT name, type, "notnull", IFNULL(dflt_value, ''), pk, hidden
FROM pragma_table_xinfo(?) ORDER BY cid`, name) {
			fmt.Fprintf(&out, "  %s %s notnull=%s default=%s pk=%s hidden=%s\n", c[0], c[1], c[2], c[3], c[4], c[5])
		}
	}
	return out.String()
}

// rowsOf is every row of a query with width columns, each as text.
func rowsOf(t testing.TB, db *sql.DB, width int, query string, args ...any) [][]string {
	t.Helper()
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var all [][]string
	for rows.Next() {
		row := make([]string, width)
		pointers := make([]any, width)
		for i := range row {
			pointers[i] = &row[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		all = append(all, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return all
}
