// Package sqlitedbtest checks a store's schema steps in its tests.
package sqlitedbtest

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// CheckReversible checks every step that has a Down: a database the whole
// schema made, opened by a build that lacks the step, has the structure a
// new database of that build has; opened again by the whole schema, the
// structure it had at first. open opens a database file as the store does.
//
// Taken back to version 0, the database is compared before the baseline
// runs again: the baseline's CREATE TABLE IF NOT EXISTS would otherwise put
// back, empty, a table the Down dropped by mistake. Rows are each step's own
// tests' to check; sqlitedb.Migrate refuses a step that leaves references
// without their row.
func CheckReversible(t testing.TB, schema sqlitedb.Schema, open func(path string) (*sql.DB, error)) {
	t.Helper()
	dir := t.TempDir()
	at := func(path string, steps int, baseline bool) string {
		t.Helper()
		db, err := open(path)
		if err != nil {
			t.Fatalf("open %s: %v", path, err)
		}
		defer func() { _ = db.Close() }()
		build := schema
		build.Steps = schema.Steps[:steps]
		if !baseline {
			build.Baseline = nil
		}
		if err := sqlitedb.Migrate(context.Background(), db, build); err != nil {
			t.Fatalf("migrate %s with %d steps: %v", filepath.Base(path), steps, err)
		}
		return Structure(t, db)
	}
	whole := filepath.Join(dir, "whole.db")
	want := at(whole, len(schema.Steps), true)
	for steps := len(schema.Steps) - 1; steps >= schema.WithoutDown; steps-- {
		fresh := at(filepath.Join(dir, fmt.Sprintf("new-%d.db", steps)), steps, true)
		if got := at(whole, steps, steps > 0); got != fresh {
			t.Errorf("%s: the Down of step %d leaves\n%s\nnot the structure of version %d:\n%s", schema.Name, steps+1, got, steps, fresh)
		}
	}
	if got := at(whole, len(schema.Steps), true); got != want {
		t.Errorf("%s: applied again, the steps leave\n%s\nnot\n%s", schema.Name, got, want)
	}
}

// Structure describes a database's version; its indexes, triggers and views;
// and its tables with their columns, the indexes their UNIQUE and PRIMARY KEY
// constraints make, their foreign keys, and the CHECK constraints and
// collations their statements declare. It leaves out the stored Downs.
func Structure(t testing.TB, db *sql.DB) string {
	t.Helper()
	var out strings.Builder
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(&out, "version %d\n", version)
	objects := rowsOf(t, db, 3, `SELECT type, name, IFNULL(sql, '') FROM sqlite_master
WHERE name NOT LIKE 'sqlite\_%' ESCAPE '\' AND name <> 'schema_downgrades' ORDER BY type, name`)
	for _, o := range objects {
		kind, name, statement := o[0], o[1], o[2]
		if kind != "table" {
			// SQLite keeps an index's, trigger's or view's statement as written.
			fmt.Fprintf(&out, "%s %s: %s\n", kind, name, strings.Join(strings.Fields(statement), " "))
			continue
		}
		// ALTER TABLE rewrites a table's statement, so its columns and
		// constraints stand for it.
		fmt.Fprintf(&out, "table %s\n", name)
		for _, c := range rowsOf(t, db, 6, `SELECT name, type, "notnull", IFNULL(dflt_value, ''), pk, hidden
FROM pragma_table_xinfo(?) ORDER BY cid`, name) {
			fmt.Fprintf(&out, "  %s %s notnull=%s default=%s pk=%s hidden=%s\n", c[0], c[1], c[2], c[3], c[4], c[5])
		}
		var constraints []string
		// The indexes of UNIQUE and PRIMARY KEY constraints, by what they
		// cover: their names number them in the order the statement wrote them.
		for _, index := range rowsOf(t, db, 4, `SELECT name, "unique", origin, partial FROM pragma_index_list(?) WHERE origin <> 'c'`, name) {
			var columns []string
			for _, c := range rowsOf(t, db, 3, `SELECT IFNULL(name, '<expression>'), IFNULL(coll, ''), "desc"
FROM pragma_index_xinfo(?) WHERE key = 1 ORDER BY seqno`, index[0]) {
				columns = append(columns, fmt.Sprintf("%s collate %s desc=%s", c[0], c[1], c[2]))
			}
			constraints = append(constraints, fmt.Sprintf("index origin=%s unique=%s partial=%s on %s", index[2], index[1], index[3], strings.Join(columns, ", ")))
		}
		// A foreign key's rows share its id, one per column it pairs.
		type foreignKey struct{ parent, actions string }
		keys := map[string]*foreignKey{}
		pairs := map[string][]string{}
		for _, f := range rowsOf(t, db, 7, `SELECT id, "table", "from", IFNULL("to", ''), on_update, on_delete, "match"
FROM pragma_foreign_key_list(?) ORDER BY id, seq`, name) {
			keys[f[0]] = &foreignKey{parent: f[1], actions: fmt.Sprintf("on update %s on delete %s match %s", f[4], f[5], f[6])}
			pairs[f[0]] = append(pairs[f[0]], f[2]+"->"+f[3])
		}
		for id, key := range keys {
			constraints = append(constraints, fmt.Sprintf("foreign key %s references %s %s", strings.Join(pairs[id], ", "), key.parent, key.actions))
		}
		var statement string
		if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?", name).Scan(&statement); err != nil {
			t.Fatal(err)
		}
		constraints = append(constraints, Declared(statement)...)
		slices.Sort(constraints)
		for _, c := range constraints {
			fmt.Fprintf(&out, "  %s\n", c)
		}
	}
	return out.String()
}

// Declared lists the CHECK constraints and COLLATE clauses of a CREATE TABLE
// statement, each with the column it belongs to or "table", whitespace
// collapsed. No pragma reports them, and ALTER TABLE ADD COLUMN keeps the
// text of the definitions it does not add. It scans the definitions rather
// than parsing SQL: Go has no SQLite parser to ask, and the statements are
// the stores' own.
func Declared(statement string) []string {
	open := strings.IndexByte(statement, '(')
	if open < 0 {
		return nil
	}
	var definitions []string
	depth, start := 0, open+1
scan:
	for i := open; i < len(statement); i++ {
		switch statement[i] {
		case '\'', '"', '`', '[':
			i = skipQuoted(statement, i)
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				definitions = append(definitions, statement[start:i])
				break scan
			}
		case ',':
			if depth == 1 {
				definitions = append(definitions, statement[start:i])
				start = i + 1
			}
		}
	}
	var out []string
	for _, definition := range definitions {
		definition = strings.TrimSpace(definition)
		if definition == "" {
			continue
		}
		first := definition
		if end := strings.IndexFunc(definition, func(r rune) bool { return r < 128 && !isNameByte(byte(r)) }); end >= 0 {
			first = definition[:end]
		}
		var owner string
		switch {
		case strings.ContainsRune("'\"`[", rune(definition[0])):
			owner = "column " + definition[1:skipQuoted(definition, 0)]
		case slices.Contains([]string{"CONSTRAINT", "PRIMARY", "UNIQUE", "CHECK", "FOREIGN"}, strings.ToUpper(first)):
			owner = "table"
		default:
			owner = "column " + first
		}
		out = append(out, clauses(owner, definition)...)
	}
	return out
}

// clauses are the CHECK and COLLATE clauses of one definition.
func clauses(owner, definition string) []string {
	var out []string
	upper := strings.ToUpper(definition)
	for i := 0; i < len(definition); i++ {
		switch c := definition[i]; {
		case c == '\'' || c == '"' || c == '`' || c == '[':
			i = skipQuoted(definition, i)
		case wordAt(upper, i, "CHECK"):
			open := strings.IndexByte(definition[i:], '(')
			if open < 0 {
				return out
			}
			end := closing(definition, i+open)
			out = append(out, owner+": check "+strings.Join(strings.Fields(definition[i+open:end+1]), " "))
			i = end
		case wordAt(upper, i, "COLLATE"):
			name := strings.Fields(definition[i+len("COLLATE"):])
			if len(name) > 0 {
				out = append(out, owner+": collate "+strings.ToUpper(strings.Trim(strings.TrimRight(name[0], ",)"), "\"`[]")))
			}
			i += len("COLLATE")
		}
	}
	return out
}

// wordAt reports whether upper has word at i, not as part of a longer name.
func wordAt(upper string, i int, word string) bool {
	if !strings.HasPrefix(upper[i:], word) || i > 0 && isNameByte(upper[i-1]) {
		return false
	}
	end := i + len(word)
	return end == len(upper) || !isNameByte(upper[end])
}

func isNameByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}

// skipQuoted returns the index of the quote that closes the one at i; SQL
// doubles a quote inside quotes.
func skipQuoted(s string, i int) int {
	end := s[i]
	if end == '[' {
		end = ']'
	}
	for j := i + 1; j < len(s); j++ {
		if s[j] != end {
			continue
		}
		if end != ']' && j+1 < len(s) && s[j+1] == end {
			j++
			continue
		}
		return j
	}
	return len(s) - 1
}

// closing returns the index of the parenthesis that closes the one at open.
func closing(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '\'', '"', '`', '[':
			i = skipQuoted(s, i)
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return len(s) - 1
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
