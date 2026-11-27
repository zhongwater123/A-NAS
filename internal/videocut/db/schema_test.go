package db

import (
	"database/sql"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
	"github.com/zhongwater123/A-NAS/internal/sqlitedb/sqlitedbtest"
)

func TestDatabaseStepsHaveWorkingDowns(t *testing.T) {
	sqlitedbtest.CheckReversible(t, schema, func(path string) (*sql.DB, error) {
		return sqlitedb.Open(path, sqlitedb.Options{})
	})
}

// A build from before the kinds of job (step 9) opens a database in which a
// draft was saved as a version and a version recut: the edit and recut
// jobs go with everything that hangs on them, the versions they saved with
// their drafts, and the project forgets it chose one of them. Foreign keys
// are off while a step goes back, so nothing cascades; sqlitedb.Migrate
// refuses a Down that leaves rows referring to rows no longer there.
func TestGoingBackBeforeJobKindsForgetsEditsAndRecuts(t *testing.T) {
	ctx := t.Context()
	db, err := sqlitedb.Open(t.TempDir()+"/videocut.db", sqlitedb.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqlitedb.Migrate(ctx, db, schema); err != nil {
		t.Fatal(err)
	}
	at := "2026-11-20T08:00:00.000000000Z"
	for _, statement := range []string{
		`INSERT INTO projects(id, owner_user_id, name, grant_id, state, created_at, updated_at, chosen_version_id)
			VALUES('project:1', 'user:alice', '海边', 'grant:1', 'ready', '` + at + `', '` + at + `', 'version:3')`,
		`INSERT INTO jobs(id, owner_user_id, project_id, request_key, settings_json, state, not_before, created_at, updated_at, plans, kind, source_version_id)
			VALUES('job:1', 'user:alice', 'project:1', 'k1', '{}', 'done', '` + at + `', '` + at + `', '` + at + `', 1, 'generate', NULL),
				('job:2', 'user:alice', 'project:1', 'k2', '{}', 'done', '` + at + `', '` + at + `', '` + at + `', 1, 'edit', 'version:1'),
				('job:3', 'user:alice', 'project:1', 'k3', '{}', 'done', '` + at + `', '` + at + `', '` + at + `', 1, 'recut', 'version:1')`,
		`INSERT INTO job_assets(job_id, asset_id, object_id) VALUES('job:1', 'asset:a', 'object:a'), ('job:2', 'asset:a', 'object:a'), ('job:3', 'asset:a', 'object:a')`,
		`INSERT INTO steps(job_id, name, input_key, state, started_at) VALUES('job:1', 'fill', 'k', 'done', '` + at + `'),
			('job:2', 'fill', 'k', 'done', '` + at + `'), ('job:3', 'fill', 'k', 'done', '` + at + `')`,
		`INSERT INTO rounds(id, job_id, owner_user_id, attempt_token, state, claimed_at, epoch, template, input_key)
			VALUES('round:1', 'job:3', 'user:alice', 'token', 'ended', '` + at + `', 0, 'plan-v3', 'k')`,
		`INSERT INTO versions(id, project_id, number, job_id, plan, version_key, film_asset_id, created_at, kind, source_version_id, draft_revision)
			VALUES('version:1', 'project:1', 1, 'job:1', 1, 'v1', 'asset:film1', '` + at + `', 'plan', NULL, NULL),
				('version:2', 'project:1', 2, 'job:2', 1, 'v2', 'asset:film2', '` + at + `', 'edit', 'version:1', 1),
				('version:3', 'project:1', 3, 'job:3', 1, 'v3', 'asset:film3', '` + at + `', 'recut', 'version:1', NULL)`,
		`INSERT INTO drafts(id, owner_user_id, project_id, base_version_id, revision, shots_json, updated_at)
			VALUES('draft:1', 'user:alice', 'project:1', 'version:1', 1, '{}', '` + at + `'), ('draft:2', 'user:alice', 'project:1', 'version:2', 0, '{}', '` + at + `')`,
		`INSERT INTO draft_revisions(draft_id, revision, op, shots_json, created_at) VALUES('draft:1', 0, 'create', '{}', '` + at + `'),
			('draft:2', 0, 'create', '{}', '` + at + `')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	before := schema
	before.Steps = schema.Steps[:8]
	if err := sqlitedb.Migrate(ctx, db, before); err != nil {
		t.Fatalf("going back before the kinds of job: %v", err)
	}
	for query, want := range map[string]int{
		"SELECT COUNT(*) FROM jobs":                                     1,
		"SELECT COUNT(*) FROM versions":                                 1,
		"SELECT COUNT(*) FROM steps":                                    1,
		"SELECT COUNT(*) FROM job_assets":                               1,
		"SELECT COUNT(*) FROM rounds":                                   0,
		"SELECT COUNT(*) FROM drafts":                                   1,
		"SELECT COUNT(*) FROM draft_revisions":                          1,
		"SELECT COUNT(*) FROM projects WHERE chosen_version_id IS NULL": 1,
	} {
		var got int
		if err := db.QueryRowContext(ctx, query).Scan(&got); err != nil || got != want {
			t.Errorf("%s = %d, %v; want %d", query, got, err, want)
		}
	}
}
