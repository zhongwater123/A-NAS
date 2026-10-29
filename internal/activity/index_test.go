package activity

import (
	"database/sql"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// The log reads a page by walking IndexStatement's index from the newest
// event, rather than computing every event's time and sorting the whole
// table, however the page is filtered (#167).
func TestPagesWalkTheTimeIndex(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE audit_events (id TEXT PRIMARY KEY, actor_user_id TEXT, action TEXT NOT NULL,
resource_type TEXT NOT NULL, resource_id TEXT NOT NULL, occurred_at TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '',
space_id TEXT NOT NULL DEFAULT '');` + IndexStatement); err != nil {
		t.Fatal(err)
	}
	cursor := Cursor{At: "2026-10-12T10:00:00.000", ID: "audit:x"}.String()
	for _, values := range []url.Values{
		{},
		{"before": {cursor}},
		{"category": {"file"}, "before": {cursor}},
		{"since": {"2026-10-01T00:00:00Z"}, "q": {"报告"}},
	} {
		q, err := ParseQuery(values)
		if err != nil {
			t.Fatal(err)
		}
		statement, arguments, ok := Table{DB: db, SpaceColumn: true}.query(q)
		if !ok {
			t.Fatalf("query(%v) matches nothing", values)
		}
		joined := strings.Join(queryPlan(t, db, statement, arguments), "; ")
		if !strings.Contains(joined, "audit_events_at") || strings.Contains(joined, "TEMP B-TREE") {
			t.Fatalf("plan for %v = %q, want a walk of audit_events_at without sorting", values, joined)
		}
	}
}

// queryPlan returns the steps of SQLite's plan for statement.
func queryPlan(t *testing.T, db *sql.DB, statement string, arguments []any) []string {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+statement, arguments...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return plan
}
