package montage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// A project holds only the videos a member handed to the montage center
// (docs/specs/montage-center.md): each batch adds its own, fixed to their
// content objects. The photo library keeps the videos and lets the montage
// center reach them through the project's grant (ADR 0038 decision 3).

// Grants is what the montage center asks of the photo library's grants;
// photosapi.Grants provides it.
type Grants interface {
	Register(ctx context.Context, session, projectID string, assetIDs []string) (photos.Grant, error)
	Lookup(ctx context.Context, grantID string, assetIDs []string) ([]photos.GrantedAsset, error)
	Remove(ctx context.Context, grantID string, assetIDs []string) error
	List(ctx context.Context) ([]photos.Grant, error)
	Revoke(ctx context.Context, grantID string) error
}

// Limits of a request (docs/specs/montage-center.md, 本稿补充的默认规则).
const (
	MaxAssets     = photos.MaxGrantAssets
	MaxNameRunes  = 64
	MaxAskRunes   = 200
	maxRequestKey = 128
	pageSize      = 50
)

var (
	// ErrNotFound covers another member's project as well as a missing one.
	ErrNotFound = errors.New("montage project not found")
	// ErrInvalid is a request the montage center refuses as it stands.
	ErrInvalid = errors.New("invalid montage request")
	// ErrTooManyAssets names more than MaxAssets videos at once.
	ErrTooManyAssets = errors.New("too many videos in one request")
	// ErrAssetsRefused: the photo library would not grant a video. Missing,
	// hidden, trashed and non-video assets answer alike.
	ErrAssetsRefused = errors.New("a video cannot be added")
	// ErrConflict: a request key already used for another project.
	ErrConflict = errors.New("the request key belongs to another project")
	// ErrPhotosUnavailable: the photo library did not answer.
	ErrPhotosUnavailable = errors.New("the photo library is not available")
	// ErrSessionEnded: the member's session is no longer valid.
	ErrSessionEnded = errors.New("the member's session is not valid")
)

// AssetStatus is where a project's video stands in the photo library now.
type AssetStatus string

const (
	AssetAvailable AssetStatus = "available"
	AssetTrashed   AssetStatus = "trashed"
	AssetMissing   AssetStatus = "missing"
	// AssetUnknown: the photo library did not answer this time.
	AssetUnknown AssetStatus = "unknown"
)

// Project is a member's project in a list.
type Project struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	AssetCount int       `json:"assetCount"`
	CreatedAt  time.Time `json:"createdAt"`
}

// ProjectAsset is a video of a project.
type ProjectAsset struct {
	AssetID  string      `json:"assetId"`
	Status   AssetStatus `json:"status"`
	Duration float64     `json:"duration,omitempty"`
	TakenAt  *time.Time  `json:"takenAt,omitempty"`
	AddedAt  time.Time   `json:"addedAt"`
}

// ProjectDetail is one project with its videos.
type ProjectDetail struct {
	Project
	Ask           string         `json:"ask"`
	Chronological bool           `json:"chronological"`
	Assets        []ProjectAsset `json:"assets"`
}

// ProjectPage is a page of projects, newest first.
type ProjectPage struct {
	Items []Project `json:"items"`
	Next  string    `json:"next,omitempty"`
}

// ProjectChange changes a project's name and settings; nil leaves one.
// SongStart moves the song's excerpt to the downbeat nearest it.
type ProjectChange struct {
	Name          *string  `json:"name,omitempty"`
	Ask           *string  `json:"ask,omitempty"`
	Chronological *bool    `json:"chronological,omitempty"`
	SongStart     *float64 `json:"songStart,omitempty"`
}

func newID(prefix string) string {
	b := make([]byte, 15)
	_, _ = rand.Read(b)
	return prefix + ":" + base32.StdEncoding.EncodeToString(b)
}

func uniqueIDs(ids []string) ([]string, error) {
	if len(ids) > MaxAssets {
		return nil, ErrTooManyAssets
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || len(id) > 128 {
			return nil, ErrInvalid
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	// No videos is no project: never all of the library (#514).
	if len(out) == 0 {
		return nil, ErrInvalid
	}
	return out, nil
}

func validKey(key string) bool { return key != "" && len(key) <= maxRequestKey }

func validText(text string, most int) bool {
	return utf8.ValidString(text) && utf8.RuneCountInString(text) <= most && !strings.ContainsAny(text, "\x00")
}

// grantError turns what the photo library answered into the montage
// center's errors.
func grantError(err error) error {
	switch photosapi.GrantErrorCode(err) {
	case "":
		return fmt.Errorf("%w: %v", ErrPhotosUnavailable, err)
	case photosapi.GrantAssetsRefused, photosapi.GrantAssetNotFound:
		return ErrAssetsRefused
	case photosapi.GrantTooMany:
		return ErrTooManyAssets
	case photosapi.GrantUnauthorized:
		return ErrSessionEnded
	case photosapi.GrantNotFound:
		return ErrNotFound
	}
	return fmt.Errorf("%w: %v", ErrPhotosUnavailable, err)
}

type projectRow struct {
	id, owner, name, state string
	grantID                sql.NullString
	grantLost              bool
	ask                    string
	chronological          bool
	created                time.Time
}

func (s *Store) project(ctx context.Context, q queryer, owner, id string) (projectRow, error) {
	var row projectRow
	var created string
	err := q.QueryRowContext(ctx, `SELECT id, owner_user_id, name, state, grant_id, grant_lost, ask, chronological, created_at
		FROM projects WHERE id = ? AND owner_user_id = ?`, id, owner).
		Scan(&row.id, &row.owner, &row.name, &row.state, &row.grantID, &row.grantLost, &row.ask, &row.chronological, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return projectRow{}, ErrNotFound
	}
	if err != nil {
		return projectRow{}, err
	}
	row.created, err = time.Parse(time.RFC3339Nano, created)
	return row, err
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (s *Store) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

// batch finds or starts owner's batch for requestKey. A new project gets a
// row in the registering state first: registering the grant needs the
// member's session, so a restart in between cannot finish it, and Reconcile
// revokes the grant and drops the row.
func (s *Store) batch(ctx context.Context, owner, requestKey, projectID string) (batchID, forProject string, err error) {
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, "SELECT id, project_id FROM batches WHERE owner_user_id = ? AND request_key = ?", owner, requestKey).
			Scan(&batchID, &forProject)
		if err == nil || !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		now := sqlitedb.FormatTime(s.now())
		if projectID == "" {
			projectID = newID("project")
			if _, err := tx.ExecContext(ctx, `INSERT INTO projects(id, owner_user_id, name, state, created_at, updated_at)
				VALUES(?, ?, '', 'registering', ?, ?)`, projectID, owner, now, now); err != nil {
				return err
			}
		}
		batchID, forProject = newID("batch"), projectID
		_, err = tx.ExecContext(ctx, "INSERT INTO batches(id, owner_user_id, project_id, request_key, created_at) VALUES(?, ?, ?, ?, ?)",
			batchID, owner, projectID, requestKey, now)
		return err
	})
	return batchID, forProject, err
}

// abandon drops a project that never got its grant.
func (s *Store) abandon(ctx context.Context, projectID string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM projects WHERE id = ? AND state = 'registering'", projectID)
	return err
}

// register grants the batch's videos and records them in the project.
func (s *Store) register(ctx context.Context, owner, session, projectID, batchID string, ids []string) error {
	grant, err := s.grants.Register(ctx, session, projectID, ids)
	if err != nil {
		return grantError(err)
	}
	looked, err := s.grants.Lookup(ctx, grant.ID, ids)
	if err != nil {
		return grantError(err)
	}
	var earliest, latest *time.Time
	for _, asset := range looked {
		if asset.Status != photos.GrantAvailable {
			return ErrAssetsRefused
		}
		if asset.TakenAt != nil {
			if earliest == nil || asset.TakenAt.Before(*earliest) {
				earliest = asset.TakenAt
			}
			if latest == nil || asset.TakenAt.After(*latest) {
				latest = asset.TakenAt
			}
		}
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		now := sqlitedb.FormatTime(s.now())
		for _, asset := range looked {
			if _, err := tx.ExecContext(ctx, `INSERT INTO project_assets(project_id, asset_id, object_id, batch_id, added_at)
				VALUES(?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, projectID, asset.AssetID, asset.ObjectID, batchID, now); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE projects SET state = 'ready', grant_id = ?, updated_at = ?,
			name = CASE WHEN name = '' THEN ? ELSE name END WHERE id = ? AND owner_user_id = ?`,
			grant.ID, now, defaultName(earliest, latest, s.now()), projectID, owner)
		return err
	})
}

// defaultName names a new project by its videos' dates, as "2026-08-02 至
// 08-04" (docs/specs/montage-center.md).
func defaultName(earliest, latest *time.Time, now time.Time) string {
	if earliest == nil {
		return now.Local().Format("2006-01-02") + " 的剪辑"
	}
	first, last := earliest.Local(), latest.Local()
	switch {
	case first.Format("2006-01-02") == last.Format("2006-01-02"):
		return first.Format("2006-01-02")
	case first.Year() == last.Year():
		return first.Format("2006-01-02") + " 至 " + last.Format("01-02")
	}
	return first.Format("2006-01-02") + " 至 " + last.Format("2006-01-02")
}

// CreateProject makes owner's project of assetIDs, registering their grant
// with the member's session. The same requestKey again returns the same
// project, with one grant.
func (s *Store) CreateProject(ctx context.Context, owner, session, requestKey, name string, assetIDs []string) (ProjectDetail, bool, error) {
	ids, err := uniqueIDs(assetIDs)
	if err != nil {
		return ProjectDetail{}, false, err
	}
	name = strings.TrimSpace(name)
	if !validKey(requestKey) || !validText(name, MaxNameRunes) {
		return ProjectDetail{}, false, ErrInvalid
	}
	batchID, projectID, err := s.batch(ctx, owner, requestKey, "")
	if err != nil {
		return ProjectDetail{}, false, err
	}
	row, err := s.project(ctx, s.db, owner, projectID)
	if err != nil {
		return ProjectDetail{}, false, err
	}
	created := row.state == "registering"
	if created {
		if name != "" {
			if _, err := s.db.ExecContext(ctx, "UPDATE projects SET name = ? WHERE id = ? AND name = ''", name, projectID); err != nil {
				return ProjectDetail{}, false, err
			}
		}
		if err := s.register(ctx, owner, session, projectID, batchID, ids); err != nil {
			if errors.Is(err, ErrAssetsRefused) || errors.Is(err, ErrTooManyAssets) || errors.Is(err, ErrSessionEnded) {
				// Nothing was granted: the project never was.
				err = errors.Join(err, s.abandon(ctx, projectID))
			}
			return ProjectDetail{}, false, err
		}
	}
	detail, err := s.Project(ctx, owner, projectID)
	return detail, created, err
}

// AddAssets adds assetIDs to owner's project. The same requestKey again
// adds nothing more. Versions already made stay as they are, and nothing is
// made anew.
func (s *Store) AddAssets(ctx context.Context, owner, session, projectID, requestKey string, assetIDs []string) (ProjectDetail, error) {
	ids, err := uniqueIDs(assetIDs)
	if err != nil {
		return ProjectDetail{}, err
	}
	if !validKey(requestKey) {
		return ProjectDetail{}, ErrInvalid
	}
	row, err := s.project(ctx, s.db, owner, projectID)
	if err != nil {
		return ProjectDetail{}, err
	}
	if row.state != "ready" {
		return ProjectDetail{}, ErrNotFound
	}
	batchID, forProject, err := s.batch(ctx, owner, requestKey, projectID)
	if err != nil {
		return ProjectDetail{}, err
	}
	if forProject != projectID {
		return ProjectDetail{}, ErrConflict
	}
	if err := s.register(ctx, owner, session, projectID, batchID, ids); err != nil {
		return ProjectDetail{}, err
	}
	return s.Project(ctx, owner, projectID)
}

// Projects lists owner's projects, newest first, from cursor.
func (s *Store) Projects(ctx context.Context, owner, cursor string) (ProjectPage, error) {
	where, args := "p.owner_user_id = ? AND p.state = 'ready'", []any{owner}
	if cursor != "" {
		at, id, ok := strings.Cut(cursor, "|")
		if !ok {
			return ProjectPage{}, ErrInvalid
		}
		where += " AND (p.created_at < ? OR (p.created_at = ? AND p.id < ?))"
		args = append(args, at, at, id)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT p.id, p.name, p.created_at, (SELECT COUNT(*) FROM project_assets a WHERE a.project_id = p.id)
		FROM projects p WHERE `+where+` ORDER BY p.created_at DESC, p.id DESC LIMIT ?`, append(args, pageSize+1)...)
	if err != nil {
		return ProjectPage{}, err
	}
	defer rows.Close()
	page := ProjectPage{Items: []Project{}}
	var lastAt string
	for rows.Next() {
		var project Project
		var created string
		if err := rows.Scan(&project.ID, &project.Name, &created, &project.AssetCount); err != nil {
			return ProjectPage{}, err
		}
		if len(page.Items) == pageSize {
			page.Next = lastAt + "|" + page.Items[pageSize-1].ID
			break
		}
		if project.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
			return ProjectPage{}, err
		}
		page.Items, lastAt = append(page.Items, project), created
	}
	return page, rows.Err()
}

// Project returns owner's project with where each video stands now. A video
// whose content changed since it was added counts as missing.
func (s *Store) Project(ctx context.Context, owner, projectID string) (ProjectDetail, error) {
	row, err := s.project(ctx, s.db, owner, projectID)
	if err != nil {
		return ProjectDetail{}, err
	}
	if row.state != "ready" {
		return ProjectDetail{}, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, "SELECT asset_id, object_id, added_at FROM project_assets WHERE project_id = ? ORDER BY added_at, asset_id", projectID)
	if err != nil {
		return ProjectDetail{}, err
	}
	defer rows.Close()
	detail := ProjectDetail{ID: row.id, Name: row.name, CreatedAt: row.created, Ask: row.ask,
		Chronological: row.chronological, Assets: []ProjectAsset{}}
	objects := map[string]string{}
	var ids []string
	for rows.Next() {
		var asset ProjectAsset
		var object, added string
		if err := rows.Scan(&asset.AssetID, &object, &added); err != nil {
			return ProjectDetail{}, err
		}
		if asset.AddedAt, err = time.Parse(time.RFC3339Nano, added); err != nil {
			return ProjectDetail{}, err
		}
		objects[asset.AssetID], ids = object, append(ids, asset.AssetID)
		detail.Assets = append(detail.Assets, asset)
	}
	if err := rows.Err(); err != nil {
		return ProjectDetail{}, err
	}
	detail.AssetCount = len(detail.Assets)
	status := map[string]photos.GrantedAsset{}
	known := !row.grantLost && row.grantID.Valid
	if known && len(ids) > 0 {
		looked, err := s.grants.Lookup(ctx, row.grantID.String, ids)
		switch {
		case photosapi.GrantErrorCode(err) == photosapi.GrantNotFound:
			// The grant is gone, as after a restore: every video is missing.
		case err != nil:
			s.logger.WarnContext(ctx, "photo grant lookup failed", "project", projectID, "error", err)
			for i := range detail.Assets {
				detail.Assets[i].Status = AssetUnknown
			}
			return detail, nil
		default:
			for _, asset := range looked {
				status[asset.AssetID] = asset
			}
		}
	}
	for i, asset := range detail.Assets {
		looked, ok := status[asset.AssetID]
		switch {
		case !ok:
			detail.Assets[i].Status = AssetMissing
		case looked.Status == photos.GrantAvailable && looked.ObjectID != objects[asset.AssetID]:
			detail.Assets[i].Status = AssetMissing
		default:
			detail.Assets[i].Status = AssetStatus(looked.Status)
			detail.Assets[i].Duration, detail.Assets[i].TakenAt = looked.Duration, looked.TakenAt
		}
	}
	return detail, nil
}

// UpdateProject renames owner's project or changes its settings. A job in
// progress keeps the settings it started with.
func (s *Store) UpdateProject(ctx context.Context, owner, projectID string, change ProjectChange) (ProjectDetail, error) {
	sets, args := []string{}, []any{}
	if change.Name != nil {
		name := strings.TrimSpace(*change.Name)
		if name == "" || !validText(name, MaxNameRunes) {
			return ProjectDetail{}, ErrInvalid
		}
		sets, args = append(sets, "name = ?"), append(args, name)
	}
	if change.Ask != nil {
		if !validText(*change.Ask, MaxAskRunes) {
			return ProjectDetail{}, ErrInvalid
		}
		sets, args = append(sets, "ask = ?"), append(args, strings.TrimSpace(*change.Ask))
	}
	if change.Chronological != nil {
		sets, args = append(sets, "chronological = ?"), append(args, *change.Chronological)
	}
	if len(sets) == 0 && change.SongStart == nil {
		return ProjectDetail{}, ErrInvalid
	}
	if change.SongStart != nil {
		// The start is checked against the song first: a start that leaves
		// no usable excerpt changes nothing.
		if _, err := s.SongStart(ctx, owner, projectID, *change.SongStart); err != nil {
			return ProjectDetail{}, err
		}
		if len(sets) == 0 {
			return s.Project(ctx, owner, projectID)
		}
	}
	sets, args = append(sets, "updated_at = ?"), append(args, sqlitedb.FormatTime(s.now()), projectID, owner)
	result, err := s.db.ExecContext(ctx, "UPDATE projects SET "+strings.Join(sets, ", ")+
		" WHERE id = ? AND owner_user_id = ? AND state = 'ready'", args...)
	if err != nil {
		return ProjectDetail{}, err
	}
	if changed, err := result.RowsAffected(); err != nil || changed == 0 {
		return ProjectDetail{}, errors.Join(ErrNotFound, err)
	}
	return s.Project(ctx, owner, projectID)
}

// DeleteProject removes owner's project and revokes its grant. The videos
// and the films stay in the photo library. When the photo library does not
// answer, the grant is revoked later (RetryRevocations).
func (s *Store) DeleteProject(ctx context.Context, owner, projectID string) error {
	var grantID sql.NullString
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		row, err := s.project(ctx, tx, owner, projectID)
		if err != nil {
			return err
		}
		grantID = row.grantID
		if grantID.Valid {
			if _, err := tx.ExecContext(ctx, "INSERT INTO revocations(grant_id, next_try) VALUES(?, ?) ON CONFLICT DO NOTHING",
				grantID.String, sqlitedb.FormatTime(s.now())); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM projects WHERE id = ?", projectID); err != nil {
			return err
		}
		return forgetUnusedSongs(ctx, tx, owner)
	})
	if err != nil {
		return err
	}
	s.songs.Lock()
	s.removeUnusedCopies(ctx)
	s.songs.Unlock()
	if !grantID.Valid {
		return nil
	}
	s.revoke(ctx, grantID.String)
	return nil
}

// revoke revokes a grant waiting in revocations, and forgets it once the
// photo library has.
func (s *Store) revoke(ctx context.Context, grantID string) {
	if err := s.grants.Revoke(ctx, grantID); err != nil {
		s.logger.WarnContext(ctx, "revoking a photo grant failed; trying again later", "grant", grantID, "error", err)
		_, _ = s.db.ExecContext(ctx, `UPDATE revocations SET attempts = attempts + 1,
			next_try = ? WHERE grant_id = ?`, sqlitedb.FormatTime(s.now().Add(time.Minute)), grantID)
		return
	}
	_, _ = s.db.ExecContext(ctx, "DELETE FROM revocations WHERE grant_id = ?", grantID)
}

// dueRevocations lists the grants whose revocation is due again.
func (s *Store) dueRevocations(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT grant_id FROM revocations WHERE next_try <= ? ORDER BY next_try", sqlitedb.FormatTime(s.now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var due []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		due = append(due, id)
	}
	return due, rows.Err()
}

// RetryRevocations revokes the grants whose revocation the photo library
// missed.
func (s *Store) RetryRevocations(ctx context.Context) error {
	due, err := s.dueRevocations(ctx)
	if err != nil {
		return err
	}
	for _, id := range due {
		s.revoke(ctx, id)
	}
	return nil
}

// Reconcile brings the projects in line with the photo library's grants
// when the service starts: montage.db and the Catalog may have been restored
// to different times. A project that never got its grant goes, with any
// grant registered for it; a project whose grant is gone has its videos
// missing; a grant no project knows is revoked.
func (s *Store) Reconcile(ctx context.Context) error {
	grants, err := s.grants.List(ctx)
	if err != nil {
		return grantError(err)
	}
	projects, err := s.knownProjects(ctx)
	if err != nil {
		return err
	}
	live := map[string]bool{}
	for _, grant := range grants {
		project, ok := projects[grant.ProjectID]
		matches := ok && project.owner == grant.OwnerUserID && project.state == "ready" &&
			(!project.grantID.Valid || project.grantID.String == grant.ID)
		if matches && !grant.Revoked {
			live[grant.ID] = true
			continue
		}
		if !grant.Revoked {
			s.logger.InfoContext(ctx, "revoking a photo grant no project holds", "grant", grant.ID)
			if _, err := s.db.ExecContext(ctx, "INSERT INTO revocations(grant_id, next_try) VALUES(?, ?) ON CONFLICT DO NOTHING",
				grant.ID, sqlitedb.FormatTime(s.now())); err != nil {
				return err
			}
			s.revoke(ctx, grant.ID)
		}
	}
	for id, project := range projects {
		switch {
		case project.state == "registering":
			if _, err := s.db.ExecContext(ctx, "DELETE FROM projects WHERE id = ?", id); err != nil {
				return err
			}
		case project.grantID.Valid && !live[project.grantID.String]:
			s.logger.WarnContext(ctx, "a project's photo grant is gone; its videos are missing", "project", id)
			if _, err := s.db.ExecContext(ctx, "UPDATE projects SET grant_lost = 1 WHERE id = ?", id); err != nil {
				return err
			}
		}
	}
	return nil
}

// knownProject is what reconciling needs of a project.
type knownProject struct {
	owner, state string
	grantID      sql.NullString
}

func (s *Store) knownProjects(ctx context.Context) (map[string]knownProject, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, owner_user_id, state, grant_id FROM projects")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	projects := map[string]knownProject{}
	for rows.Next() {
		var id string
		var project knownProject
		if err := rows.Scan(&id, &project.owner, &project.state, &project.grantID); err != nil {
			return nil, err
		}
		projects[id] = project
	}
	return projects, rows.Err()
}

// RemoveAccount forgets a deleted account's projects. Its grants went with
// the photo library's own step, which runs first.
func (s *Store) RemoveAccount(ctx context.Context, userID string) error {
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM projects WHERE owner_user_id = ?", userID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM songs WHERE owner_user_id = ?", userID)
		return err
	})
	if err != nil {
		return err
	}
	s.songs.Lock()
	defer s.songs.Unlock()
	s.removeUnusedCopies(ctx)
	return nil
}
