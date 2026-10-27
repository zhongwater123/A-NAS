package sqlitetest_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/sqlitetest"
)

func TestAMatchingReadEndsWithErrCut(t *testing.T) {
	db, err := sql.Open(sqlitetest.Register("ORDER BY n", 2), filepath.Join(t.TempDir(), "cut.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec("CREATE TABLE numbers (n INTEGER); INSERT INTO numbers VALUES (1), (2), (3), (4)"); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query("SELECT n FROM numbers ORDER BY n")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var read []int
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		read = append(read, n)
	}
	if len(read) != 2 || !errors.Is(rows.Err(), sqlitetest.ErrCut) {
		t.Fatalf("read %v, Err() = %v; want 2 rows and ErrCut", read, rows.Err())
	}
	// What made the loops of #269 wrong: Close does not report the failure.
	if err := rows.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}

	// A prepared statement of the same query is cut too.
	statement, err := db.Prepare("SELECT n FROM numbers ORDER BY n")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = statement.Close() }()
	prepared, err := statement.Query()
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	count := 0
	for prepared.Next() {
		count++
	}
	if count != 2 || !errors.Is(prepared.Err(), sqlitetest.ErrCut) {
		t.Fatalf("prepared statement read %d rows, Err() = %v; want 2 rows and ErrCut", count, prepared.Err())
	}

	// Other queries read everything.
	if err := db.QueryRow("SELECT count(*) FROM numbers").Scan(&count); err != nil || count != 4 {
		t.Fatalf("count = %d, %v", count, err)
	}
}
