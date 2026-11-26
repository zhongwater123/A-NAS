package db

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// The reads and writes of projects: each member's projects, their grant,
// settings, song and chosen film (docs/architecture/videocut-center.md).

// ProjectRow is a project as kept.
type ProjectRow struct {
	ID, Owner, Name, State string
	GrantID                sql.NullString
	Ask                    string
	Chronological          bool
	Created                time.Time
	Chosen                 string
}

// Project is owner's project id, in whatever state; sql.ErrNoRows when
// owner has none.
func (d *DB) Project(ctx context.Context, owner, id string) (ProjectRow, error) {
	return project(ctx, d.db, owner, id)
}

// Project is DB.Project inside the transaction.
func (tx *Tx) Project(ctx context.Context, owner, id string) (ProjectRow, error) {
	return project(ctx, tx.tx, owner, id)
}

func project(ctx context.Context, q queryer, owner, id string) (ProjectRow, error) {
	var row ProjectRow
	var created string
	err := q.QueryRowContext(ctx, `SELECT id, owner_user_id, name, state, grant_id, ask, chronological, created_at, IFNULL(chosen_version_id, '')
		FROM projects WHERE id = ? AND owner_user_id = ?`, id, owner).
		Scan(&row.ID, &row.Owner, &row.Name, &row.State, &row.GrantID, &row.Ask, &row.Chronological, &created, &row.Chosen)
	if err != nil {
		return ProjectRow{}, err
	}
	row.Created, err = time.Parse(time.RFC3339Nano, created)
	return row, err
}

// AddRegisteringProject adds owner's project id, named name, registering
// its videos, made at at.
func (tx *Tx) AddRegisteringProject(ctx context.Context, id, owner, name string, at time.Time) error {
	now := sqlitedb.FormatTime(at)
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO projects(id, owner_user_id, name, state, created_at, updated_at)
				VALUES(?, ?, ?, 'registering', ?, ?)`, id, owner, name, now, now)
	return err
}

// SetRegisteringGrant records grantID as projectID's grant while it is
// registering.
func (d *DB) SetRegisteringGrant(ctx context.Context, projectID, grantID string) error {
	_, err := d.db.ExecContext(ctx, "UPDATE projects SET grant_id = ? WHERE id = ? AND state = 'registering'", grantID, projectID)
	return err
}

// ReadyProject makes owner's projectID ready with grantID at at, named
// name when it has no name.
func (tx *Tx) ReadyProject(ctx context.Context, owner, projectID, grantID, name string, at time.Time) error {
	_, err := tx.tx.ExecContext(ctx, `UPDATE projects SET state = 'ready', grant_id = ?, updated_at = ?,
			name = CASE WHEN name = '' THEN ? ELSE name END WHERE id = ? AND owner_user_id = ?`,
		grantID, sqlitedb.FormatTime(at), name, projectID, owner)
	return err
}

// RegisteringGrant is the grant projectID recorded while registering (not
// valid when none); sql.ErrNoRows when it is not registering.
func (tx *Tx) RegisteringGrant(ctx context.Context, projectID string) (sql.NullString, error) {
	var recorded sql.NullString
	err := tx.tx.QueryRowContext(ctx, "SELECT grant_id FROM projects WHERE id = ? AND state = 'registering'", projectID).Scan(&recorded)
	return recorded, err
}

// DeleteProject forgets project id.
func (tx *Tx) DeleteProject(ctx context.Context, id string) error {
	_, err := tx.tx.ExecContext(ctx, "DELETE FROM projects WHERE id = ?", id)
	return err
}

// DeleteOwnerProjects forgets every project of owner.
func (tx *Tx) DeleteOwnerProjects(ctx context.Context, owner string) error {
	_, err := tx.tx.ExecContext(ctx, "DELETE FROM projects WHERE owner_user_id = ?", owner)
	return err
}

// ProjectCursor is where a page of projects goes on from: after the
// project made at At with id ID.
type ProjectCursor struct{ At, ID string }

// ProjectListRow is a ready project as the list shows it: its number of
// videos, its chosen version and that version's film ("" when none), and
// when it was made, as kept.
type ProjectListRow struct {
	ID, Name, Created                  string
	AssetCount                         int
	ChosenVersionID, ChosenFilmAssetID string
}

// ProjectList is up to limit of owner's ready projects, the latest made
// first, after cursor when it is not nil.
func (d *DB) ProjectList(ctx context.Context, owner string, cursor *ProjectCursor, limit int) ([]ProjectListRow, error) {
	where, args := "p.owner_user_id = ? AND p.state = 'ready'", []any{owner}
	if cursor != nil {
		where += " AND (p.created_at < ? OR (p.created_at = ? AND p.id < ?))"
		args = append(args, cursor.At, cursor.At, cursor.ID)
	}
	rows, err := d.db.QueryContext(ctx, `SELECT p.id, p.name, p.created_at, (SELECT COUNT(*) FROM project_assets a WHERE a.project_id = p.id),
		IFNULL(v.id, ''), IFNULL(v.film_asset_id, '')
		FROM projects p LEFT JOIN versions v ON v.id = p.chosen_version_id AND v.project_id = p.id
		WHERE `+where+` ORDER BY p.created_at DESC, p.id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var projects []ProjectListRow
	for rows.Next() {
		var p ProjectListRow
		if err := rows.Scan(&p.ID, &p.Name, &p.Created, &p.AssetCount, &p.ChosenVersionID, &p.ChosenFilmAssetID); err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

// ProjectChange is what UpdateReadyProject changes: each that is not nil.
type ProjectChange struct {
	Name, Ask     *string
	Chronological *bool
}

// UpdateReadyProject makes change to owner's ready projectID at at, and
// says how many projects it changed.
func (d *DB) UpdateReadyProject(ctx context.Context, owner, projectID string, change ProjectChange, at time.Time) (int64, error) {
	sets, args := []string{}, []any{}
	if change.Name != nil {
		sets, args = append(sets, "name = ?"), append(args, *change.Name)
	}
	if change.Ask != nil {
		sets, args = append(sets, "ask = ?"), append(args, *change.Ask)
	}
	if change.Chronological != nil {
		sets, args = append(sets, "chronological = ?"), append(args, *change.Chronological)
	}
	sets, args = append(sets, "updated_at = ?"), append(args, sqlitedb.FormatTime(at), projectID, owner)
	result, err := d.db.ExecContext(ctx, "UPDATE projects SET "+strings.Join(sets, ", ")+
		" WHERE id = ? AND owner_user_id = ? AND state = 'ready'", args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// ChooseVersion makes versionID projectID's film, at at.
func (tx *Tx) ChooseVersion(ctx context.Context, projectID, versionID string, at time.Time) error {
	_, err := tx.tx.ExecContext(ctx, "UPDATE projects SET chosen_version_id = ?, updated_at = ? WHERE id = ?",
		versionID, sqlitedb.FormatTime(at), projectID)
	return err
}

// ReadyProjectAsset is the grant of owner's ready projectID (not valid
// when none) and the content it holds of assetID; sql.ErrNoRows when it
// does not hold it.
func (d *DB) ReadyProjectAsset(ctx context.Context, owner, projectID, assetID string) (sql.NullString, string, error) {
	var grantID sql.NullString
	var object string
	err := d.db.QueryRowContext(ctx, `SELECT p.grant_id, a.object_id FROM projects p JOIN project_assets a ON a.project_id = p.id
		WHERE p.id = ? AND p.owner_user_id = ? AND p.state = 'ready' AND a.asset_id = ?`, projectID, owner, assetID).Scan(&grantID, &object)
	return grantID, object, err
}

// DraggedSelection is the excerpt one of owner's projects keeps for songID
// from start, encoded; sql.ErrNoRows when none does.
func (d *DB) DraggedSelection(ctx context.Context, owner, songID string, start float64) (string, error) {
	var text string
	err := d.db.QueryRowContext(ctx, `SELECT song_selection FROM projects
		WHERE owner_user_id = ? AND song_id = ? AND song_start = ? AND song_selection IS NOT NULL LIMIT 1`, owner, songID, start).Scan(&text)
	return text, err
}

// ProjectGrantAndJobAssets is projectID's grant (not valid when none) and
// how many videos jobID has.
func (d *DB) ProjectGrantAndJobAssets(ctx context.Context, projectID, jobID string) (sql.NullString, int, error) {
	var count int
	var grantID sql.NullString
	err := d.db.QueryRowContext(ctx, `SELECT grant_id, (SELECT COUNT(*) FROM job_assets WHERE job_id = ?)
		FROM projects WHERE id = ?`, jobID, projectID).Scan(&grantID, &count)
	return grantID, count, err
}

// ProjectOwners are the members who have projects.
func (d *DB) ProjectOwners(ctx context.Context) ([]string, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT DISTINCT owner_user_id FROM projects")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var owners []string
	for rows.Next() {
		var owner string
		if err := rows.Scan(&owner); err != nil {
			return nil, err
		}
		owners = append(owners, owner)
	}
	return owners, rows.Err()
}

// SetProjectGrant records grantID as projectID's grant.
func (d *DB) SetProjectGrant(ctx context.Context, projectID, grantID string) error {
	_, err := d.db.ExecContext(ctx, "UPDATE projects SET grant_id = ? WHERE id = ?", grantID, projectID)
	return err
}

// KnownProject is what reconciling needs of a project.
type KnownProject struct {
	State, GrantID string
}

// MemberProjects are owner's projects, by id.
func (d *DB) MemberProjects(ctx context.Context, owner string) (map[string]KnownProject, error) {
	rows, err := d.db.QueryContext(ctx, "SELECT id, state, COALESCE(grant_id, '') FROM projects WHERE owner_user_id = ?", owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	projects := map[string]KnownProject{}
	for rows.Next() {
		var id string
		var project KnownProject
		if err := rows.Scan(&id, &project.State, &project.GrantID); err != nil {
			return nil, err
		}
		projects[id] = project
	}
	return projects, rows.Err()
}

// VersionNumber is the number of the version saved under versionKey, or
// the next number of projectID's versions when none is; with the
// project's name.
func (d *DB) VersionNumber(ctx context.Context, versionKey, projectID string) (int, string, error) {
	var number int
	var name string
	err := d.db.QueryRowContext(ctx, `SELECT IFNULL((SELECT number FROM versions WHERE version_key = ?),
		(SELECT IFNULL(MAX(number), 0) + 1 FROM versions WHERE project_id = p.id)), p.name
		FROM projects p WHERE p.id = ?`, versionKey, projectID).Scan(&number, &name)
	return number, name, err
}

// SetProjectSong makes songID, from trackID ("" for none), owner's
// projectID's song at at, from its default start.
func (tx *Tx) SetProjectSong(ctx context.Context, owner, projectID, songID, trackID string, at time.Time) error {
	_, err := tx.tx.ExecContext(ctx, `UPDATE projects SET song_id = ?, song_track = NULLIF(?, ''), song_start = NULL, song_selection = NULL, updated_at = ?
			WHERE id = ? AND owner_user_id = ?`, songID, trackID, sqlitedb.FormatTime(at), projectID, owner)
	return err
}

// ProjectSong is a project's song and the start the member chose.
type ProjectSong struct {
	SongID    sql.NullString
	TrackID   sql.NullString
	Start     sql.NullFloat64
	Selection sql.NullString
}

// ProjectSong is owner's projectID's state and song; sql.ErrNoRows when
// owner has no such project.
func (d *DB) ProjectSong(ctx context.Context, owner, projectID string) (string, ProjectSong, error) {
	return projectSong(ctx, d.db, owner, projectID)
}

// ProjectSong is DB.ProjectSong inside the transaction.
func (tx *Tx) ProjectSong(ctx context.Context, owner, projectID string) (string, ProjectSong, error) {
	return projectSong(ctx, tx.tx, owner, projectID)
}

func projectSong(ctx context.Context, q queryer, owner, projectID string) (string, ProjectSong, error) {
	var song ProjectSong
	var state string
	err := q.QueryRowContext(ctx, `SELECT state, song_id, song_track, song_start, song_selection FROM projects WHERE id = ? AND owner_user_id = ?`,
		projectID, owner).Scan(&state, &song.SongID, &song.TrackID, &song.Start, &song.Selection)
	return state, song, err
}

// SetSongStart keeps start and selection (each nil for the default) as
// where owner's projectID's excerpt of songID starts, at at; it says how
// many projects it changed (none when the project has another song).
func (d *DB) SetSongStart(ctx context.Context, owner, projectID, songID string, start, selection any, at time.Time) (int64, error) {
	result, err := d.db.ExecContext(ctx, `UPDATE projects SET song_start = ?, song_selection = ?, updated_at = ?
		WHERE id = ? AND owner_user_id = ? AND song_id = ?`, start, selection, sqlitedb.FormatTime(at), projectID, owner, songID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
