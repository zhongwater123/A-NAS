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
	rows, err := db.Query(`SELECT type, name, IFNULL(sql, '') FROM sqlite_master
WHERE name NOT LIKE 'sqlite_%' AND name <> 'schema_downgrades' ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	type object struct{ kind, name, sql string }
	var objects []object
	for rows.Next() {
		var o object
		if err := rows.Scan(&o.kind, &o.name, &o.sql); err != nil {
			t.Fatal(err)
		}
		objects = append(objects, o)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, o := range objects {
		if o.kind != "table" {
			// SQLite keeps an index's, trigger's or view's statement as written.
			fmt.Fprintf(&out, "%s %s: %s\n", o.kind, o.name, strings.Join(strings.Fields(o.sql), " "))
			continue
		}
		// ALTER TABLE rewrites a table's statement, so its columns stand for it.
		fmt.Fprintf(&out, "table %s\n", o.name)
		columns, err := db.Query("SELECT name, type, \"notnull\", IFNULL(dflt_value, ''), pk, hidden FROM pragma_table_xinfo(?) ORDER BY cid", o.name)
		if err != nil {
			t.Fatal(err)
		}
		for columns.Next() {
			var name, kind, defaultValue string
			var notNull, pk, hidden int
			if err := columns.Scan(&name, &kind, &notNull, &defaultValue, &pk, &hidden); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&out, "  %s %s notnull=%d default=%s pk=%d hidden=%d\n", name, kind, notNull, defaultValue, pk, hidden)
		}
		if err := columns.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return out.String()
}
