// Package montage keeps the montage center's records (剪辑中心, ADR 0038):
// projects, plans, timelines and jobs, in montage.db on the montage store.
// The videos, their analysis and the films stay in the photo library.
package montage

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/zhongwater123/A-NAS/internal/capacity"
	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// DatabaseName is the store's database file.
const DatabaseName = "montage.db"

// schema grows a step per change (sqlitedb.Schema); every step has a Down,
// so that an older release takes the database back.
var schema = sqlitedb.Schema{
	Name:     DatabaseName,
	Baseline: func(context.Context, *sql.DB) error { return nil },
	Steps: []sqlitedb.Step{
		// 1: projects, the batches that brought their videos, the videos
		// fixed to their content, and grants waiting to be revoked.
		{Up: `
CREATE TABLE projects (
    id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL,
    name TEXT NOT NULL,
    grant_id TEXT,
    state TEXT NOT NULL CHECK (state IN ('registering', 'ready')),
    grant_lost INTEGER NOT NULL DEFAULT 0,
    ask TEXT NOT NULL DEFAULT '',
    chronological INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX projects_by_owner ON projects(owner_user_id, created_at, id);

CREATE TABLE batches (
    id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    request_key TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (owner_user_id, request_key)
);

CREATE TABLE project_assets (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    asset_id TEXT NOT NULL,
    object_id TEXT NOT NULL,
    batch_id TEXT NOT NULL REFERENCES batches(id) ON DELETE CASCADE,
    added_at TEXT NOT NULL,
    PRIMARY KEY (project_id, asset_id)
);

CREATE TABLE revocations (
    grant_id TEXT PRIMARY KEY,
    next_try TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0
);
`, Down: `
DROP TABLE revocations;
DROP TABLE project_assets;
DROP TABLE batches;
DROP TABLE projects;
`},
		// 2: jobs, the videos they started with, each step's input key and
		// result, the plans and the planning rounds (M9), and the versions
		// saved into the photo library (docs/architecture/montage-center.md,
		// 持久任务).
		{Up: `
CREATE TABLE jobs (
    id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    request_key TEXT NOT NULL,
    settings_json TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('queued', 'running', 'waiting', 'needs_input', 'done', 'failed', 'cancelled')),
    step TEXT NOT NULL DEFAULT '',
    attempts INTEGER NOT NULL DEFAULT 0,
    not_before TEXT NOT NULL,
    lease_until TEXT,
    heartbeat_at TEXT,
    cancel_requested INTEGER NOT NULL DEFAULT 0,
    wait_reason TEXT NOT NULL DEFAULT '',
    error_class TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    missing_json TEXT,
    plan_rejections INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    finished_at TEXT,
    UNIQUE (owner_user_id, request_key)
);
CREATE INDEX jobs_by_state ON jobs(state, not_before);
CREATE INDEX jobs_by_project ON jobs(project_id, created_at);

CREATE TABLE job_assets (
    job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    asset_id TEXT NOT NULL,
    object_id TEXT NOT NULL,
    PRIMARY KEY (job_id, asset_id)
);

CREATE TABLE steps (
    job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    input_key TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('running', 'done')),
    result_json TEXT,
    started_at TEXT NOT NULL,
    finished_at TEXT,
    PRIMARY KEY (job_id, name)
);

CREATE TABLE rounds (
    id TEXT PRIMARY KEY,
    job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL,
    attempt_token TEXT NOT NULL,
    run_ref TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL CHECK (state IN ('claimed', 'started', 'ended', 'abandoned')),
    claimed_at TEXT NOT NULL,
    started_at TEXT,
    ended_at TEXT,
    reported_at TEXT,
    outcome TEXT NOT NULL DEFAULT ''
);

CREATE TABLE plans (
    job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    step TEXT NOT NULL CHECK (step IN ('pick', 'shots')),
    round_id TEXT,
    catalog_key TEXT NOT NULL,
    answer_json TEXT NOT NULL,
    accepted_at TEXT NOT NULL,
    voided_at TEXT
);
CREATE INDEX plans_by_job ON plans(job_id, step, voided_at);

CREATE TABLE versions (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    number INTEGER NOT NULL,
    job_id TEXT NOT NULL UNIQUE,
    version_key TEXT NOT NULL UNIQUE,
    film_asset_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (project_id, number)
);
`, Down: `
DROP TABLE versions;
DROP TABLE plans;
DROP TABLE rounds;
DROP TABLE steps;
DROP TABLE job_assets;
DROP TABLE jobs;
`},
	},
}

// Store is the montage center's database. Every record names the member it
// belongs to, and every read is for one member.
type Store struct {
	db       *sql.DB
	root     string
	grants   Grants
	compute  Compute
	editing  Editing
	planner  Planner
	jobMusic Music
	now      func() time.Time
	logger   *slog.Logger
	// jobWake tells RunJobs a job is due; running holds the running job's
	// cancel.
	jobWake chan struct{}
	runMu   sync.Mutex
	running map[string]context.CancelCauseFunc
	// beat is how often a running job's lease is renewed (heartbeat).
	beat time.Duration
	// admits is capacity.Admits, which tests replace.
	admits func(path string, incoming int64) (bool, error)
}

// Options are what a store works with.
type Options struct {
	// Grants reaches the photo library's project grants.
	Grants Grants
	// Compute, Editing, Planner and Music are what jobs work with; a
	// store without them keeps projects but runs no job.
	Compute Compute
	Editing Editing
	Planner Planner
	Music   Music
	Now     func() time.Time
	Logger  *slog.Logger
}

// Open opens, creating when missing, the store's database in root. Commits
// wait until they are on disk: a job that was told it advanced must still
// have advanced after a power cut.
func Open(ctx context.Context, root string, options Options) (*Store, error) {
	if options.Grants == nil {
		return nil, errors.New("the montage store needs the photo library's grants")
	}
	db, err := sqlitedb.Open(filepath.Join(root, DatabaseName), sqlitedb.Options{Durable: true})
	if err != nil {
		return nil, err
	}
	if err := sqlitedb.Migrate(ctx, db, schema); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	store := &Store{db: db, root: root, grants: options.Grants, compute: options.Compute, editing: options.Editing,
		planner: options.Planner, jobMusic: options.Music, now: options.Now, logger: options.Logger,
		jobWake: make(chan struct{}, 1), running: map[string]context.CancelCauseFunc{}, beat: heartbeat,
		admits: capacity.Admits}
	if store.now == nil {
		store.now = time.Now
	}
	if store.logger == nil {
		store.logger = slog.Default()
	}
	return store, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }
