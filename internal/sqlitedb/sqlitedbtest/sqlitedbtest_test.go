package sqlitedbtest_test

import (
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
	"github.com/zhongwater123/A-NAS/internal/sqlitedb/sqlitedbtest"
)

// recorder keeps what CheckReversible reports instead of failing the test.
type recorder struct {
	testing.TB
	errors []string
}

func (r *recorder) Helper() {}

func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func openStore(path string) (*sql.DB, error) { return sqlitedb.Open(path, sqlitedb.Options{}) }

// peopleSchema has a step that adds a column; down is its Down.
func peopleSchema(down string) sqlitedb.Schema {
	return sqlitedb.Schema{
		Name: "people.db",
		Baseline: sqlitedb.Statements(`
CREATE TABLE IF NOT EXISTS teams (id INTEGER PRIMARY KEY);
CREATE TABLE IF NOT EXISTS people (
    id INTEGER PRIMARY KEY,
    email TEXT NOT NULL UNIQUE COLLATE NOCASE,
    team_id INTEGER NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    age INTEGER NOT NULL CHECK (age >= 0)
);`),
		Steps: []sqlitedb.Step{{Up: `ALTER TABLE people ADD COLUMN nick TEXT NOT NULL DEFAULT ''`, Down: down}},
	}
}

// rebuild is a Down that rebuilds people with these column definitions.
func rebuild(columns string) string {
	return `CREATE TABLE people_before (` + columns + `);
INSERT INTO people_before (id, email, team_id, age) SELECT id, email, team_id, age FROM people;
DROP TABLE people;
ALTER TABLE people_before RENAME TO people;`
}

func TestCheckReversibleFindsADownThatLosesAConstraint(t *testing.T) {
	whole := `id INTEGER PRIMARY KEY, email TEXT NOT NULL UNIQUE COLLATE NOCASE,
team_id INTEGER NOT NULL REFERENCES teams(id) ON DELETE CASCADE, age INTEGER NOT NULL CHECK (age >= 0)`
	for _, c := range []struct{ name, down string }{
		{"UNIQUE", strings.Replace(whole, "UNIQUE ", "", 1)},
		{"COLLATE", strings.Replace(whole, " COLLATE NOCASE", "", 1)},
		{"REFERENCES", strings.Replace(whole, " REFERENCES teams(id) ON DELETE CASCADE", "", 1)},
		{"ON DELETE", strings.Replace(whole, " ON DELETE CASCADE", "", 1)},
		{"CHECK", strings.Replace(whole, " CHECK (age >= 0)", "", 1)},
		{"dropped baseline table", "ALTER TABLE people DROP COLUMN nick; DROP TABLE teams;"},
	} {
		t.Run(c.name, func(t *testing.T) {
			down := c.down
			if !strings.HasPrefix(down, "ALTER") {
				down = rebuild(down)
			}
			r := &recorder{TB: t}
			sqlitedbtest.CheckReversible(r, peopleSchema(down), openStore)
			if len(r.errors) == 0 {
				t.Fatal("CheckReversible accepted the Down")
			}
		})
	}
}

func TestCheckReversibleAcceptsARebuildThatKeepsEveryConstraint(t *testing.T) {
	r := &recorder{TB: t}
	sqlitedbtest.CheckReversible(r, peopleSchema(rebuild(`id INTEGER PRIMARY KEY,
    email TEXT NOT NULL UNIQUE COLLATE NOCASE,
    team_id INTEGER NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    age INTEGER NOT NULL CHECK (age >= 0)`)), openStore)
	if len(r.errors) != 0 {
		t.Fatalf("CheckReversible refused a faithful rebuild:\n%s", strings.Join(r.errors, "\n"))
	}
}

func TestDeclaredNamesEachClauseWithItsColumn(t *testing.T) {
	got := sqlitedbtest.Declared(`CREATE TABLE t (
    a TEXT NOT NULL DEFAULT 'x, CHECK (no)' COLLATE NOCASE,
    "b c" INTEGER CHECK (b IN (1, 2)),
    CHECK (a <> ''), UNIQUE (a, "b c"))`)
	want := []string{"column a: collate NOCASE", "column b c: check (b IN (1, 2))", "table: check (a <> '')"}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("Declared gave %q, want %q", got, want)
	}
}
