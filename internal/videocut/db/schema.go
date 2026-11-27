package db

import (
	"context"
	"database/sql"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// DatabaseName is the store's database file.
const DatabaseName = "videocut.db"

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
		// project's song and the excerpt's start (docs/specs/videocut-center.md,
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
		// saved into the photo library (docs/architecture/videocut-center.md,
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
		// 5: planning by the member's VNAS (docs/architecture/videocut-center.md,
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
		// (docs/specs/videocut-center.md, 两个方案): each plan the job took
		// and each version says which of the job's 方案 it is, and a job how
		// many it makes: one for the jobs from before this step, which go on
		// as they started. The answers and briefs of step 5 need nothing: a
		// later 方案's input keys are its own. A project also keeps which
		// track of the music library its song was copied from, for the page
		// to mark that track and no other of the same title.
		{Up: `
ALTER TABLE projects ADD COLUMN song_track TEXT;
ALTER TABLE jobs ADD COLUMN plans INTEGER NOT NULL DEFAULT 1;
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
ALTER TABLE jobs DROP COLUMN plans;
ALTER TABLE projects DROP COLUMN song_track;
`},
		// 7: planning is a conversation of the member's planning instance
		// (docs/architecture/videocut-planning-session.md): why a round
		// stopped short of the model's planning, which says when the job
		// asks again (retryWait).
		{Up: "ALTER TABLE rounds ADD COLUMN failure TEXT NOT NULL DEFAULT ''", Down: "ALTER TABLE rounds DROP COLUMN failure"},
		// 8: editing a film after it was made (#596, ADR 0039). A project
		// keeps the version its member chose; a version its timeline, which
		// the save writes and a version from before is given from its job's
		// fill. A draft is a member's edit of one version: its shots over the
		// version's slots, which the version's timeline holds and a draft does
		// not keep; each step is a revision, the last hundred kept, and back is
		// the revision undoing it goes to.
		{Up: `
ALTER TABLE projects ADD COLUMN chosen_version_id TEXT;
ALTER TABLE versions ADD COLUMN timeline_json TEXT;
CREATE TABLE drafts (
    id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    base_version_id TEXT NOT NULL UNIQUE REFERENCES versions(id) ON DELETE CASCADE,
    revision INTEGER NOT NULL,
    shots_json TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE draft_revisions (
    draft_id TEXT NOT NULL REFERENCES drafts(id) ON DELETE CASCADE,
    revision INTEGER NOT NULL,
    op TEXT NOT NULL,
    seed INTEGER,
    back INTEGER,
    shots_json TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (draft_id, revision)
);
`, Down: `
DROP TABLE draft_revisions;
DROP TABLE drafts;
ALTER TABLE versions DROP COLUMN timeline_json;
ALTER TABLE projects DROP COLUMN chosen_version_id;
`},
		// 9: jobs of three kinds (#596): generate, as before; edit, which
		// renders a draft as a new version; recut. An edit or recut job and
		// the version it saves name the version they came from, and an edit
		// the draft's revision. A release from before knows neither: going
		// back forgets them, while their films stay in the photo library.
		// Foreign keys are off while a step goes back, so the Down takes
		// away itself what hangs on those jobs and versions.
		{Up: `
ALTER TABLE jobs ADD COLUMN kind TEXT NOT NULL DEFAULT 'generate' CHECK (kind IN ('generate', 'edit', 'recut'));
ALTER TABLE jobs ADD COLUMN source_version_id TEXT;
ALTER TABLE jobs ADD COLUMN draft_id TEXT;
ALTER TABLE jobs ADD COLUMN draft_revision INTEGER;
ALTER TABLE versions ADD COLUMN kind TEXT NOT NULL DEFAULT 'plan' CHECK (kind IN ('plan', 'edit', 'recut'));
ALTER TABLE versions ADD COLUMN source_version_id TEXT;
ALTER TABLE versions ADD COLUMN draft_revision INTEGER;
`, Down: `
UPDATE projects SET chosen_version_id = NULL WHERE chosen_version_id IN (SELECT id FROM versions WHERE kind <> 'plan');
DELETE FROM draft_revisions WHERE draft_id IN (SELECT id FROM drafts WHERE base_version_id IN (SELECT id FROM versions WHERE kind <> 'plan'));
DELETE FROM drafts WHERE base_version_id IN (SELECT id FROM versions WHERE kind <> 'plan');
DELETE FROM versions WHERE kind <> 'plan';
DELETE FROM job_assets WHERE job_id IN (SELECT id FROM jobs WHERE kind <> 'generate');
DELETE FROM steps WHERE job_id IN (SELECT id FROM jobs WHERE kind <> 'generate');
DELETE FROM rounds WHERE job_id IN (SELECT id FROM jobs WHERE kind <> 'generate');
DELETE FROM plans WHERE job_id IN (SELECT id FROM jobs WHERE kind <> 'generate');
DELETE FROM plan_answers WHERE job_id IN (SELECT id FROM jobs WHERE kind <> 'generate');
DELETE FROM plan_briefs WHERE job_id IN (SELECT id FROM jobs WHERE kind <> 'generate');
DELETE FROM jobs WHERE kind <> 'generate';
ALTER TABLE versions DROP COLUMN draft_revision;
ALTER TABLE versions DROP COLUMN source_version_id;
ALTER TABLE versions DROP COLUMN kind;
ALTER TABLE jobs DROP COLUMN draft_revision;
ALTER TABLE jobs DROP COLUMN draft_id;
ALTER TABLE jobs DROP COLUMN source_version_id;
ALTER TABLE jobs DROP COLUMN kind;
`},
	},
}
