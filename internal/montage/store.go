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
	"sync/atomic"
	"time"

	"github.com/zhongwater123/A-NAS/internal/capacity"
	"github.com/zhongwater123/A-NAS/internal/keylock"
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
		// fixed to their content, and grants waiting to be revoked. A batch
		// keeps what its request asked for, so that its request key repeats
		// that request alone, and stays registering until its videos are in
		// the project.
		{Up: `
CREATE TABLE projects (
    id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL,
    name TEXT NOT NULL,
    grant_id TEXT,
    state TEXT NOT NULL CHECK (state IN ('registering', 'ready')),
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
    kind TEXT NOT NULL CHECK (kind IN ('create', 'add')),
    asset_ids TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('registering', 'done')),
    created_at TEXT NOT NULL,
    UNIQUE (owner_user_id, request_key)
);
CREATE INDEX batches_unfinished ON batches(owner_user_id) WHERE state = 'registering';

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
		// 2: the songs members chose, with their analysis, and each
		// project's song and the excerpt's start (docs/specs/montage-center.md,
		// 配乐). A song row is a member's; its copy, songs/<sha256>/audio,
		// is shared by content.
		{Up: `
CREATE TABLE songs (
    id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL,
    sha256 TEXT NOT NULL,
    title TEXT NOT NULL,
    duration REAL NOT NULL,
    content_type TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('analyzing', 'ready', 'unusable')),
    reason TEXT NOT NULL DEFAULT '',
    waiting TEXT NOT NULL DEFAULT '',
    attempts INTEGER NOT NULL DEFAULT 0,
    next_try TEXT,
    analysis_version TEXT NOT NULL DEFAULT '',
    analysis_json TEXT,
    loudness BLOB,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (owner_user_id, sha256)
);
CREATE INDEX songs_by_content ON songs(sha256, state);
ALTER TABLE projects ADD COLUMN song_id TEXT;
ALTER TABLE projects ADD COLUMN song_start REAL;
ALTER TABLE projects ADD COLUMN song_selection TEXT;
`, Down: `
ALTER TABLE projects DROP COLUMN song_selection;
ALTER TABLE projects DROP COLUMN song_start;
ALTER TABLE projects DROP COLUMN song_id;
DROP TABLE songs;
`},
		// 3: jobs, the videos they started with, each step's input key and
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
		{Up: "ALTER TABLE jobs ADD COLUMN save_started INTEGER NOT NULL DEFAULT 0", Down: "ALTER TABLE jobs DROP COLUMN save_started"},
		// 5: planning by the member's VNAS (docs/architecture/montage-center.md,
		// 规划). plan_grants: when each member last registered a planning
		// grant, and under which name the waker knows them. jobs: which want
		// a round and since when, and their epoch, which replanning raises.
		// rounds: the epoch and the pick step's input key a round plans for,
		// its template, and when its model first called a tool. plan_answers:
		// the first answer taken for a step's input key, which the job's step
		// then takes into plans. plan_briefs: the brief built for a step's
		// input key.
		{Up: `
CREATE TABLE plan_grants (
    owner_user_id TEXT PRIMARY KEY,
    username TEXT NOT NULL,
    registered_at TEXT NOT NULL
);
ALTER TABLE jobs ADD COLUMN plan_wanted INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN plan_wanted_at TEXT;
ALTER TABLE jobs ADD COLUMN plan_epoch INTEGER NOT NULL DEFAULT 0;
ALTER TABLE rounds ADD COLUMN epoch INTEGER NOT NULL DEFAULT 0;
ALTER TABLE rounds ADD COLUMN template TEXT NOT NULL DEFAULT '';
ALTER TABLE rounds ADD COLUMN input_key TEXT NOT NULL DEFAULT '';
ALTER TABLE rounds ADD COLUMN called_at TEXT;
CREATE INDEX rounds_by_job ON rounds(job_id, claimed_at);
CREATE INDEX rounds_by_owner ON rounds(owner_user_id, reported_at);

CREATE TABLE plan_answers (
    job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    step TEXT NOT NULL CHECK (step IN ('pick', 'shots')),
    input_key TEXT NOT NULL,
    round_id TEXT NOT NULL,
    answer_json TEXT NOT NULL,
    accepted_at TEXT NOT NULL,
    PRIMARY KEY (job_id, step, input_key)
);

CREATE TABLE plan_briefs (
    job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    step TEXT NOT NULL CHECK (step IN ('pick', 'shots')),
    input_key TEXT NOT NULL,
    brief_json TEXT NOT NULL,
    PRIMARY KEY (job_id, step, input_key)
);
`, Down: `
DROP TABLE plan_briefs;
DROP TABLE plan_answers;
DROP INDEX rounds_by_owner;
DROP INDEX rounds_by_job;
ALTER TABLE rounds DROP COLUMN called_at;
ALTER TABLE rounds DROP COLUMN input_key;
ALTER TABLE rounds DROP COLUMN template;
ALTER TABLE rounds DROP COLUMN epoch;
ALTER TABLE jobs DROP COLUMN plan_epoch;
ALTER TABLE jobs DROP COLUMN plan_wanted_at;
ALTER TABLE jobs DROP COLUMN plan_wanted;
DROP TABLE plan_grants;
`},
		// 6: a job makes two films to choose from, as the prototype's 方案
		// (docs/specs/montage-center.md, 两个方案): each plan the job took
		// and each version says which of the job's 方案 it is. The answers and
		// briefs of step 5 need nothing: a later 方案's input keys are its own.
		{Up: `
ALTER TABLE plans ADD COLUMN plan INTEGER NOT NULL DEFAULT 1;
DROP INDEX plans_by_job;
CREATE INDEX plans_by_job ON plans(job_id, plan, step, voided_at);
CREATE TABLE versions_new (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    number INTEGER NOT NULL,
    job_id TEXT NOT NULL,
    plan INTEGER NOT NULL DEFAULT 1,
    version_key TEXT NOT NULL UNIQUE,
    film_asset_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (project_id, number),
    UNIQUE (job_id, plan)
);
INSERT INTO versions_new(id, project_id, number, job_id, plan, version_key, film_asset_id, created_at)
    SELECT id, project_id, number, job_id, 1, version_key, film_asset_id, created_at FROM versions;
DROP TABLE versions;
ALTER TABLE versions_new RENAME TO versions;
`, Down: `
CREATE TABLE versions_old (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    number INTEGER NOT NULL,
    job_id TEXT NOT NULL UNIQUE,
    version_key TEXT NOT NULL UNIQUE,
    film_asset_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (project_id, number)
);
INSERT INTO versions_old(id, project_id, number, job_id, version_key, film_asset_id, created_at)
    SELECT id, project_id, number, job_id, version_key, film_asset_id, created_at FROM versions WHERE plan = 1;
DROP TABLE versions;
ALTER TABLE versions_old RENAME TO versions;
DROP INDEX plans_by_job;
DELETE FROM plans WHERE plan <> 1;
ALTER TABLE plans DROP COLUMN plan;
CREATE INDEX plans_by_job ON plans(job_id, step, voided_at);
`},
	},
}

// Store is the montage center's database. Every record names the member it
// belongs to, and every read is for one member.
type Store struct {
	db      *sql.DB
	root    string
	grants  Grants
	media   Media
	music   MusicAnalyzer
	compute MusicComputer
	now     func() time.Time
	logger  *slog.Logger
	// songs serialises the song copies: a copy arriving and the copies
	// no row names any more being removed.
	songs sync.Mutex
	// wake tells AnalyzeSongs a song arrived.
	wake chan struct{}
	// What jobs work with: the compute Worker's montage operations, the
	// FFmpeg worker's editing, the planner, and the songs (the store's
	// own, Chosen, Excerpt and Audio, unless a test sets another).
	jobCompute Compute
	editing    Editing
	planner    Planner
	jobMusic   Music
	// jobWake tells RunJobs a job is due; running holds the running job's
	// cancel.
	jobWake chan struct{}
	runMu   sync.Mutex
	running map[string]context.CancelCauseFunc
	// beat is how often a running job's lease is renewed (heartbeat).
	beat time.Duration
	// admits is capacity.Admits, which tests replace.
	admits func(path string, incoming int64) (bool, error)
	// planMarkers is where the planning markers go, one empty file per
	// member with a job wanting a round (/run/a-nas-montage/plans).
	planMarkers string
	// plans is how many films, 方案, a job makes to choose from.
	plans int
	// members holds a member's lock while a request or reconciling changes
	// that member's projects and grants.
	members keylock.Map[string]
	// reconciling runs one round of Reconcile at a time. reconciled is set
	// once a round went through; seen are the unfinished batches the last
	// round found, which the next one settles.
	reconciling sync.Mutex
	reconciled  atomic.Bool
	seen        map[string]bool

	// Revocations a request asked for run in the background until Close.
	mu         sync.Mutex
	closed     bool
	base       context.Context
	stop       context.CancelFunc
	background sync.WaitGroup
}

// DefaultPlans is how many 方案 a job makes unless Options says otherwise:
// two films to choose from (docs/specs/montage-center.md, 两个方案).
const DefaultPlans = 2

// Options are what a store works with.
type Options struct {
	// Grants reaches the photo library's project grants.
	Grants Grants
	// Media decodes songs (the FFmpeg worker); Music finds their structure
	// (the music analysis Worker); Compute picks their excerpts (the
	// compute Worker). Without them songs wait.
	Media   Media
	Music   MusicAnalyzer
	Compute MusicComputer
	// JobCompute, Editing and Planner are what jobs work with besides the
	// songs: the compute Worker's montage operations, the FFmpeg worker's
	// editing and the planner. A store without them keeps projects and
	// songs but makes no film.
	JobCompute Compute
	Editing    Editing
	Planner    Planner
	// PlanMarkers is the directory of planning markers the waker reads;
	// without it no member's VNAS is asked to plan.
	PlanMarkers string
	// Plans is how many films, 方案, each job makes; 0 is DefaultPlans.
	Plans  int
	Now    func() time.Time
	Logger *slog.Logger
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
	store := &Store{db: db, root: root, grants: options.Grants, media: options.Media, music: options.Music, compute: options.Compute,
		now: options.Now, logger: options.Logger, wake: make(chan struct{}, 1),
		jobCompute: options.JobCompute, editing: options.Editing, planner: options.Planner,
		jobWake: make(chan struct{}, 1), running: map[string]context.CancelCauseFunc{}, beat: heartbeat, admits: capacity.Admits,
		planMarkers: options.PlanMarkers, plans: options.Plans}
	if store.plans <= 0 {
		store.plans = DefaultPlans
	}
	store.jobMusic = store
	// Without a stand-in, the member's VNAS plans, where markers reach it.
	if store.planner == nil && store.planMarkers != "" {
		store.planner = store.VNASPlanner()
	}
	if store.now == nil {
		store.now = time.Now
	}
	if store.logger == nil {
		store.logger = slog.Default()
	}
	store.base, store.stop = context.WithCancel(context.Background())
	return store, nil
}

// StartWorkers recovers durable work before accepting requests. Close cancels
// and joins the workers before closing their database.
func (s *Store) StartWorkers(ctx context.Context) error {
	if err := s.RecoverJobs(ctx); err != nil {
		return err
	}
	if err := s.ReclaimSongs(ctx); err != nil {
		return err
	}
	s.soon(s.AnalyzeSongs)
	s.soon(s.RunJobs)
	return nil
}

// Close stops the revocations, the song analysis and the job still running,
// which stay queued, and closes the database.
func (s *Store) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.stop()
	s.background.Wait()
	return s.db.Close()
}

// soon runs work in the background unless the store is closing.
func (s *Store) soon(work func(context.Context)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.background.Go(func() { work(s.base) })
}

func (s *Store) withTx(ctx context.Context, apply func(*sql.Tx) error) error {
	return sqlitedb.WithTx(ctx, s.db, apply)
}
