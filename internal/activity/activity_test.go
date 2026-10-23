package activity_test

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/zhongwater123/A-NAS/internal/activity"
)

type row struct {
	id, actor, action, resource, at, detail, space string
}

func openTable(t *testing.T, name string, space bool, rows ...row) activity.Table {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	schema := `CREATE TABLE audit_events (id TEXT PRIMARY KEY, actor_user_id TEXT, action TEXT NOT NULL, resource_type TEXT NOT NULL,
resource_id TEXT NOT NULL, occurred_at TEXT NOT NULL, detail TEXT NOT NULL DEFAULT ''`
	if space {
		schema += ", space_id TEXT NOT NULL DEFAULT ''"
	}
	if _, err := db.Exec(schema + ")"); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		var actor any = r.actor
		if r.actor == "" {
			actor = nil
		}
		if space {
			_, err = db.Exec("INSERT INTO audit_events VALUES(?,?,?,?,?,?,?,?)", r.id, actor, r.action, "x", r.resource, r.at, r.detail, r.space)
		} else {
			_, err = db.Exec("INSERT INTO audit_events VALUES(?,?,?,?,?,?,?)", r.id, actor, r.action, "x", r.resource, r.at, r.detail)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	return activity.Table{DB: db, SpaceColumn: space}
}

func ids(events []activity.Event) []string {
	var result []string
	for _, event := range events {
		result = append(result, event.ID)
	}
	return result
}

func equal(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// RFC 3339 strings drop trailing zeros, so "…:00Z" sorts after "…:00.5Z" as
// text; the log must still be in time order across sources and pages.
func TestMergedPagesFollowTimeAcrossSourcesWithoutGapsOrRepeats(t *testing.T) {
	ctx := context.Background()
	accounts := openTable(t, "control.db", false,
		row{id: "audit:a1", actor: "u1", action: "session.signed_in", at: "2026-10-12T10:00:00Z"},
		row{id: "audit:a2", actor: "u1", action: "user.password_changed", at: "2026-10-12T10:00:00.5Z"},
		row{id: "audit:a3", actor: "u2", action: "session.signed_in", at: "2026-10-12T09:59:59.9Z"},
		row{id: "audit:a4", action: "session.sign_in_failed", at: "2026-10-12T10:00:02Z"},
	)
	files := openTable(t, "files.db", true,
		row{id: "audit:f1", actor: "u1", action: "file.uploaded", at: "2026-10-12T10:00:00.25Z", detail: "a.txt"},
		row{id: "audit:f2", actor: "u1", action: "file.uploaded", at: "2026-10-12T10:00:00Z", detail: "b.txt"},
		row{id: "audit:f3", actor: "u2", action: "file.trashed", at: "2026-10-12T10:00:01Z", detail: "c.txt"},
	)
	want := []string{"audit:a4", "audit:f3", "audit:a2", "audit:f1", "audit:f2", "audit:a1", "audit:a3"}
	var got []string
	query := activity.Query{Limit: 3}
	for range 5 {
		fromAccounts, err := accounts.List(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		fromFiles, err := files.List(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		events, more := activity.Merge(query.Limit, fromAccounts, fromFiles)
		got = append(got, ids(events)...)
		if !more {
			break
		}
		last := events[len(events)-1].Key
		query.Before = &last
	}
	if !equal(got, want) {
		t.Fatalf("paged log = %v, want %v", got, want)
	}
}

func TestQueryFiltersByCategorySubjectTimeAndText(t *testing.T) {
	ctx := context.Background()
	table := openTable(t, "control.db", false,
		row{id: "audit:1", actor: "alice", action: "session.signed_in", at: "2026-10-12T08:00:00Z", detail: `{"via":"lan","address":"172.18.45.12"}`},
		row{id: "audit:2", actor: "admin", action: "user.credential_reset", resource: "alice", at: "2026-10-12T09:00:00Z"},
		row{id: "audit:3", actor: "admin", action: "photos.viewing_started", resource: "bob", at: "2026-10-12T10:00:00Z"},
		row{id: "audit:4", actor: "bob", action: "session.signed_out", at: "2026-10-11T10:00:00Z"},
	)
	cases := []struct {
		name  string
		query activity.Query
		want  []string
	}{
		{"sessions", activity.Query{Categories: []activity.Category{activity.CategorySession}}, []string{"audit:1", "audit:4"}},
		// photos.viewing is an account action, not a photo one.
		{"photos are not viewing", activity.Query{Categories: []activity.Category{activity.CategoryPhoto}}, nil},
		{"accounts", activity.Query{Categories: []activity.Category{activity.CategoryAccount}}, []string{"audit:3", "audit:2"}},
		{"done by or to alice", activity.Query{Subject: &activity.Subject{ActorIDs: []string{"alice"}, ResourceIDs: []string{"alice"}}}, []string{"audit:2", "audit:1"}},
		{"since today", activity.Query{Since: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)}, []string{"audit:3", "audit:2", "audit:1"}},
		{"until nine", activity.Query{Until: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}, []string{"audit:1", "audit:4"}},
		{"address", activity.Query{Text: "172.18"}, []string{"audit:1"}},
		{"action code, any case", activity.Query{Text: "RESET"}, []string{"audit:2"}},
		{"account name", activity.Query{Text: "bob", TextActors: []string{"bob"}}, []string{"audit:3", "audit:4"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.query.Limit = 10
			events, err := table.List(ctx, tc.query)
			if err != nil {
				t.Fatal(err)
			}
			if got := ids(events); !equal(got, tc.want) {
				t.Fatalf("events = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTextSearchSkipsNamesTheViewerMayNotSee(t *testing.T) {
	ctx := context.Background()
	table := openTable(t, "files.db", true,
		row{id: "audit:1", actor: "alice", action: "file.uploaded", at: "2026-10-12T08:00:00Z", detail: "体检报告.pdf", space: "space:alice"},
		row{id: "audit:2", actor: "alice", action: "file.uploaded", at: "2026-10-12T09:00:00Z", detail: "体检须知.pdf", space: "space:shared"},
		row{id: "audit:3", actor: "admin", action: "file.uploaded", at: "2026-10-12T10:00:00Z", detail: "体检.xlsx", space: "space:admin"},
	)
	reveal := &activity.Reveal{ViewerID: "admin", SpaceIDs: []string{"space:shared", "space:admin"}}
	events, err := table.List(ctx, activity.Query{Text: "体检", Reveal: reveal, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(events), []string{"audit:3", "audit:2"}; !equal(got, want) {
		t.Fatalf("search = %v, want %v", got, want)
	}
	all, err := table.List(ctx, activity.Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range all {
		if revealed, want := reveal.Revealed(event), event.ID != "audit:1"; revealed != want {
			t.Fatalf("Revealed(%s) = %v, want %v", event.ID, revealed, want)
		}
	}
}

func TestCursorRoundTripsAndRejectsForgedValues(t *testing.T) {
	cursor := activity.Cursor{At: "2026-10-12T10:00:00.500", ID: "audit:1"}
	parsed, err := activity.ParseCursor(cursor.String())
	if err != nil || parsed != cursor {
		t.Fatalf("ParseCursor() = %+v, %v; want %+v", parsed, err, cursor)
	}
	for _, forged := range []string{"", "!!", activity.Cursor{At: "2026-10-12 10:00", ID: "x"}.String(), activity.Cursor{At: cursor.At}.String()} {
		if _, err := activity.ParseCursor(forged); err == nil {
			t.Fatalf("ParseCursor(%q) accepted a forged cursor", forged)
		}
	}
}

func TestCategoryOf(t *testing.T) {
	for action, want := range map[string]activity.Category{
		"session.sign_in_failed": activity.CategorySession,
		"user.created":           activity.CategoryAccount,
		"space.viewing_started":  activity.CategoryAccount,
		"photos.viewing_ended":   activity.CategoryAccount,
		"snapshot.created":       activity.CategoryFile,
		"file.trashed.smb":       activity.CategoryFile,
		"storage.plan.running":   activity.CategoryStorage,
		"photo.imported":         activity.CategoryPhoto,
		"mystery":                activity.CategoryOther,
	} {
		if got := activity.CategoryOf(action); got != want {
			t.Errorf("CategoryOf(%q) = %q, want %q", action, got, want)
		}
	}
}

func TestParseQueryReadsClientFiltersAndRejectsBadOnes(t *testing.T) {
	cursor := activity.Cursor{At: "2026-10-12T10:00:00.000", ID: "audit:1"}
	q, err := activity.ParseQuery(url.Values{
		"before": {cursor.String()}, "since": {"2026-10-11T00:00:00+08:00"}, "category": {"file"}, "q": {" 报告 "}, "limit": {"20"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if *q.Before != cursor || !q.Since.Equal(time.Date(2026, 10, 10, 16, 0, 0, 0, time.UTC)) || q.Text != "报告" || q.Limit != 20 ||
		len(q.Categories) != 1 || q.Categories[0] != activity.CategoryFile {
		t.Fatalf("ParseQuery() = %+v", q)
	}
	again, err := activity.ParseQuery(q.Values())
	if err != nil || *again.Before != cursor || !again.Since.Equal(q.Since) || again.Text != q.Text || again.Limit != q.Limit {
		t.Fatalf("round trip = %+v, %v", again, err)
	}
	if q, _ := activity.ParseQuery(url.Values{}); q.Limit != activity.DefaultLimit {
		t.Fatalf("default limit = %d", q.Limit)
	}
	for _, bad := range []url.Values{
		{"limit": {"0"}}, {"limit": {"201"}}, {"before": {"nope"}}, {"since": {"yesterday"}},
		{"category": {"kernel"}}, {"q": {strings.Repeat("字", 101)}},
	} {
		if _, err := activity.ParseQuery(bad); err == nil {
			t.Fatalf("ParseQuery(%v) accepted", bad)
		}
	}
}
