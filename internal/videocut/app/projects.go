package app

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
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
	// What jobs ask: a video's original, its analysis, and saving a film.
	Open(ctx context.Context, grantID, assetID string) (*os.File, string, error)
	Analyze(ctx context.Context, grantID string, assetIDs []string, retry bool) error
	Analysis(ctx context.Context, grantID, assetID string) (photos.GrantAnalysis, []byte, error)
	SaveFilm(ctx context.Context, grantID, versionKey, name string, film *os.File) (string, error)
}

// Limits of a request (docs/specs/montage-center.md, 本稿补充的默认规则).
// IDs and request keys are printable ASCII, as the photo library's IDs are,
// so that the largest request the contract allows fits the API's body limit.
const (
	MaxAssets     = photos.MaxGrantAssets
	MaxIDLength   = 64
	MaxRequestKey = 128
	MaxNameRunes  = 64
	MaxAskRunes   = 200
	pageSize      = 50
)

// UpkeepInterval is how often the montage service runs a store's Upkeep.
const UpkeepInterval = time.Minute

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
	// ErrConflict: a request key the member used for another request, of
	// another kind, on another project or with other videos.
	ErrConflict = errors.New("the request key was used for another request")
	// ErrGrantRevoked: the photo library revoked the project's grant, as
	// when montage.db was restored to before the project's deletion; the
	// project can take no more videos.
	ErrGrantRevoked = errors.New("the project's grant was revoked")
	// ErrPhotosUnavailable: the photo library did not answer, or cannot now.
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
	// PreviewAssetIDs are up to six of its videos, spread over the order
	// they were added: the list shows their thumbnails.
	PreviewAssetIDs []string `json:"previewAssetIds,omitempty"`
	// ChosenVersionID is the version the member confirmed as the
	// project's film (#596), and ChosenFilmAssetID that film.
	ChosenVersionID   string `json:"chosenVersionId,omitempty"`
	ChosenFilmAssetID string `json:"chosenFilmAssetId,omitempty"`
}

// previewCount is how many videos a project in the list shows.
const previewCount = 6

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
	// Versions are the films saved, newest first.
	Versions []Version `json:"versions"`
	// Job is the project's latest job, for the page to pick up where it
	// was after a reload.
	Job *Job `json:"job,omitempty"`
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

const (
	projectRegistering = "registering"
	projectReady       = "ready"

	kindCreate = "create"
	kindAdd    = "add"

	batchRegistering = "registering"
	batchDone        = "done"
)

func newID(prefix string) string { return prefix + ":" + rand.Text() }

// uniqueIDs checks a request's videos and returns them sorted, once each.
func uniqueIDs(ids []string) ([]string, error) {
	if len(ids) > MaxAssets {
		return nil, ErrTooManyAssets
	}
	out := slices.Clone(ids)
	for _, id := range out {
		if !printable(id, MaxIDLength) {
			return nil, ErrInvalid
		}
	}
	slices.Sort(out)
	out = slices.Compact(out)
	// No videos is no project: never all of the library (#514).
	if len(out) == 0 {
		return nil, ErrInvalid
	}
	return out, nil
}

// printable reports whether value is 1 to most characters of printable
// ASCII without spaces.
func printable(value string, most int) bool {
	if value == "" || len(value) > most {
		return false
	}
	for i := range len(value) {
		if value[i] < '!' || value[i] > '~' {
			return false
		}
	}
	return true
}

func validText(text string, most int) bool {
	return utf8.ValidString(text) && utf8.RuneCountInString(text) <= most && !strings.ContainsAny(text, "\x00")
}

// grantError turns what the photo library answered into the montage
// center's errors. No answer, an owner whose account is not in use now
// (owner_inactive) and the photo library's own failure are for later.
func grantError(err error) error {
	switch photosapi.GrantErrorCode(err) {
	case photosapi.GrantAssetsRefused, photosapi.GrantAssetNotFound:
		return ErrAssetsRefused
	case photosapi.GrantTooMany:
		return ErrTooManyAssets
	case photosapi.GrantUnauthorized:
		return ErrSessionEnded
	case photosapi.GrantNotFound:
		return ErrGrantRevoked
	}
	return fmt.Errorf("%w: %v", ErrPhotosUnavailable, err)
}

// assetGone reports whether the photo library answered that a video a
// project holds can no longer be read: deleted, or in the trash. Where a
// video is read rather than added, that is not a video that cannot be added
// (grantError), and the reader says what it means there.
func assetGone(err error) bool {
	code := photosapi.GrantErrorCode(err)
	return code == photosapi.GrantAssetNotFound || code == photosapi.GrantAssetsRefused
}

type projectRow struct {
	id, owner, name, state string
	grantID                sql.NullString
	ask                    string
	chronological          bool
	created                time.Time
	chosen                 string
}

func (s *Store) project(ctx context.Context, q queryer, owner, id string) (projectRow, error) {
	var row projectRow
	var created string
	err := q.QueryRowContext(ctx, `SELECT id, owner_user_id, name, state, grant_id, ask, chronological, created_at, IFNULL(chosen_version_id, '')
		FROM projects WHERE id = ? AND owner_user_id = ?`, id, owner).
		Scan(&row.id, &row.owner, &row.name, &row.state, &row.grantID, &row.ask, &row.chronological, &created, &row.chosen)
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

// batchRow is a request that brings videos to a project: kind create makes
// the project, kind add adds to it.
type batchRow struct {
	id, projectID, kind, state string
	assets                     []string
}

// batch finds owner's batch for requestKey, or starts one: a create batch
// with its project in the registering state, named name, or an add batch on
// projectID. A request key repeats one request: with another kind of
// request, another project or other videos it is ErrConflict.
func (s *Store) batch(ctx context.Context, owner, requestKey, kind, projectID, name string, ids []string) (batchRow, error) {
	var row batchRow
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var assets string
		err := tx.QueryRowContext(ctx, "SELECT id, project_id, kind, asset_ids, state FROM batches WHERE owner_user_id = ? AND request_key = ?",
			owner, requestKey).Scan(&row.id, &row.projectID, &row.kind, &assets, &row.state)
		if err == nil {
			if err := json.Unmarshal([]byte(assets), &row.assets); err != nil {
				return err
			}
			if row.kind != kind || kind == kindAdd && row.projectID != projectID || !slices.Equal(row.assets, ids) {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		now := sqlitedb.FormatTime(s.now())
		if kind == kindCreate {
			projectID = newID("project")
			if _, err := tx.ExecContext(ctx, `INSERT INTO projects(id, owner_user_id, name, state, created_at, updated_at)
				VALUES(?, ?, ?, 'registering', ?, ?)`, projectID, owner, name, now, now); err != nil {
				return err
			}
		}
		encoded, err := json.Marshal(ids)
		if err != nil {
			return err
		}
		row = batchRow{id: newID("batch"), projectID: projectID, kind: kind, state: batchRegistering, assets: ids}
		_, err = tx.ExecContext(ctx, `INSERT INTO batches(id, owner_user_id, project_id, request_key, kind, asset_ids, state, created_at)
			VALUES(?, ?, ?, ?, ?, ?, 'registering', ?)`, row.id, owner, projectID, requestKey, kind, string(encoded), now)
		return err
	})
	return row, err
}

// register grants a registering batch's videos to its project, with the
// member's session, and records them. A refusal undoes the batch at once.
// A batch the photo library did not answer for stays registering: the
// member repeats it with its request key, or reconciling settles it. It
// runs under the member's lock.
func (s *Store) register(ctx context.Context, owner, session string, b batchRow) error {
	grant, err := s.grants.Register(ctx, session, b.projectID, b.assets)
	if err != nil {
		err = grantError(err)
		if !errors.Is(err, ErrPhotosUnavailable) {
			// Refused: nothing was granted.
			s.undo(ctx, b, "")
		}
		return err
	}
	if b.kind == kindCreate {
		// Settling a batch that does not finish revokes this grant.
		if _, err := s.db.ExecContext(ctx, "UPDATE projects SET grant_id = ? WHERE id = ? AND state = 'registering'", grant.ID, b.projectID); err != nil {
			return err
		}
	}
	looked, err := s.grants.Lookup(ctx, grant.ID, b.assets)
	switch {
	case err != nil:
		err = grantError(err)
	case len(looked) != len(b.assets):
		err = fmt.Errorf("%w: the grant answered %d of %d videos", ErrPhotosUnavailable, len(looked), len(b.assets))
	default:
		for _, asset := range looked {
			if asset.Status != photos.GrantAvailable {
				// It left the member's sight after the grant took it.
				err = ErrAssetsRefused
			}
		}
	}
	if err != nil {
		if !errors.Is(err, ErrPhotosUnavailable) {
			s.undo(ctx, b, grant.ID)
		}
		return err
	}
	var earliest, latest *time.Time
	for _, asset := range looked {
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
				VALUES(?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, b.projectID, asset.AssetID, asset.ObjectID, b.id, now); err != nil {
				return err
			}
		}
		// An addition to a project whose grant was lost registered a new one.
		if _, err := tx.ExecContext(ctx, `UPDATE projects SET state = 'ready', grant_id = ?, updated_at = ?,
			name = CASE WHEN name = '' THEN ? ELSE name END WHERE id = ? AND owner_user_id = ?`,
			grant.ID, now, defaultName(earliest, latest, s.now()), b.projectID, owner); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE batches SET state = 'done' WHERE id = ?", b.id)
		return err
	})
}

// undo settles a batch its request gave up on, and revokes the grant that
// queued. What it could not undo now, reconciling settles later.
func (s *Store) undo(ctx context.Context, b batchRow, grantID string) {
	revoke, err := s.settle(ctx, b, grantID)
	if err != nil {
		s.logger.WarnContext(ctx, "undoing a refused batch failed; reconciling settles it", "batch", b.id, "error", err)
		return
	}
	if revoke != "" {
		s.revokeSoon(revoke)
	}
}

// settle undoes an unfinished batch. A new project goes, with its grant
// queued for revocation, which settle returns; an addition's videos leave
// the grant unless the project holds them from another batch. grantID is
// the project's grant, "" when none is known but the one a new project
// recorded. It runs under the member's lock.
func (s *Store) settle(ctx context.Context, b batchRow, grantID string) (string, error) {
	if b.kind == kindCreate {
		var revoke string
		err := s.withTx(ctx, func(tx *sql.Tx) error {
			var recorded sql.NullString
			err := tx.QueryRowContext(ctx, "SELECT grant_id FROM projects WHERE id = ? AND state = 'registering'", b.projectID).Scan(&recorded)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			if revoke = grantID; revoke == "" {
				revoke = recorded.String
			}
			if revoke != "" {
				if err := s.queueRevocation(ctx, tx, revoke); err != nil {
					return err
				}
			}
			_, err = tx.ExecContext(ctx, "DELETE FROM projects WHERE id = ?", b.projectID)
			return err
		})
		return revoke, err
	}
	if grantID != "" {
		held, err := s.heldAssets(ctx, b.projectID, b.assets)
		if err != nil {
			return "", err
		}
		drop := slices.DeleteFunc(slices.Clone(b.assets), func(id string) bool { return held[id] })
		if len(drop) > 0 {
			if err := s.grants.Remove(ctx, grantID, drop); err != nil && photosapi.GrantErrorCode(err) != photosapi.GrantNotFound {
				return "", grantError(err)
			}
		}
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM batches WHERE id = ? AND state = 'registering'", b.id)
	return "", err
}

// heldAssets reports which of ids the project holds.
func (s *Store) heldAssets(ctx context.Context, projectID string, ids []string) (map[string]bool, error) {
	args := []any{projectID}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, "SELECT asset_id FROM project_assets WHERE project_id = ? AND asset_id IN ("+
		sqlitedb.Placeholders(len(ids))+")", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	held := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		held[id] = true
	}
	return held, rows.Err()
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
// with the member's session; created is false when requestKey made it
// before. The same requestKey again returns the same project, with one
// grant, and waits for that request while it is still running.
func (s *Store) CreateProject(ctx context.Context, owner, session, requestKey, name string, assetIDs []string) (ProjectDetail, bool, error) {
	ids, err := uniqueIDs(assetIDs)
	if err != nil {
		return ProjectDetail{}, false, err
	}
	name = strings.TrimSpace(name)
	if !printable(requestKey, MaxRequestKey) || !validText(name, MaxNameRunes) {
		return ProjectDetail{}, false, ErrInvalid
	}
	unlock, err := s.members.Lock(ctx, owner)
	if err != nil {
		return ProjectDetail{}, false, err
	}
	defer unlock()
	b, err := s.batch(ctx, owner, requestKey, kindCreate, "", name, ids)
	if err != nil {
		return ProjectDetail{}, false, err
	}
	// A batch still registering is new, or one the photo library did not
	// answer for before: the member has not had its project yet.
	created := b.state == batchRegistering
	if created {
		if err := s.register(ctx, owner, session, b); err != nil {
			return ProjectDetail{}, false, err
		}
	}
	detail, err := s.Project(ctx, owner, b.projectID)
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
	if !printable(requestKey, MaxRequestKey) {
		return ProjectDetail{}, ErrInvalid
	}
	unlock, err := s.members.Lock(ctx, owner)
	if err != nil {
		return ProjectDetail{}, err
	}
	defer unlock()
	row, err := s.project(ctx, s.db, owner, projectID)
	if err != nil {
		return ProjectDetail{}, err
	}
	if row.state != projectReady {
		return ProjectDetail{}, ErrNotFound
	}
	b, err := s.batch(ctx, owner, requestKey, kindAdd, projectID, "", ids)
	if err != nil {
		return ProjectDetail{}, err
	}
	if b.state == batchRegistering {
		if err := s.register(ctx, owner, session, b); err != nil {
			return ProjectDetail{}, err
		}
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
	rows, err := s.db.QueryContext(ctx, `SELECT p.id, p.name, p.created_at, (SELECT COUNT(*) FROM project_assets a WHERE a.project_id = p.id),
		IFNULL(v.id, ''), IFNULL(v.film_asset_id, '')
		FROM projects p LEFT JOIN versions v ON v.id = p.chosen_version_id AND v.project_id = p.id
		WHERE `+where+` ORDER BY p.created_at DESC, p.id DESC LIMIT ?`, append(args, pageSize+1)...)
	if err != nil {
		return ProjectPage{}, err
	}
	defer rows.Close()
	page := ProjectPage{Items: []Project{}}
	var lastAt string
	for rows.Next() {
		var project Project
		var created string
		if err := rows.Scan(&project.ID, &project.Name, &created, &project.AssetCount, &project.ChosenVersionID, &project.ChosenFilmAssetID); err != nil {
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
	if err := rows.Err(); err != nil {
		return ProjectPage{}, err
	}
	// The store has one connection: the page's rows go before the previews are read.
	if err := rows.Close(); err != nil {
		return ProjectPage{}, err
	}
	for i := range page.Items {
		if page.Items[i].PreviewAssetIDs, err = s.previews(ctx, page.Items[i].ID); err != nil {
			return ProjectPage{}, err
		}
	}
	return page, nil
}

// previews picks up to previewCount of a project's videos, evenly spread.
func (s *Store) previews(ctx context.Context, projectID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT asset_id FROM project_assets WHERE project_id = ? ORDER BY added_at, asset_id", projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		all = append(all, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	n := min(len(all), previewCount)
	picked := make([]string, 0, n)
	for k := range n {
		picked = append(picked, all[k*len(all)/n])
	}
	return picked, nil
}

// Project returns owner's project with where each video stands now. A video
// whose content changed since it was added counts as missing, and so does
// every video of a project whose grant is gone.
func (s *Store) Project(ctx context.Context, owner, projectID string) (ProjectDetail, error) {
	row, err := s.project(ctx, s.db, owner, projectID)
	if err != nil {
		return ProjectDetail{}, err
	}
	if row.state != projectReady {
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
	if detail.Versions, err = s.versions(ctx, projectID); err != nil {
		return ProjectDetail{}, err
	}
	for _, version := range detail.Versions {
		if version.ID == row.chosen {
			detail.ChosenVersionID, detail.ChosenFilmAssetID = version.ID, version.FilmAssetID
		}
	}
	var latest string
	switch err := s.db.QueryRowContext(ctx, "SELECT id FROM jobs WHERE project_id = ? ORDER BY created_at DESC, id DESC LIMIT 1", projectID).Scan(&latest); {
	case err == nil:
		job, err := s.Job(ctx, owner, latest)
		if err != nil {
			return ProjectDetail{}, err
		}
		detail.Job = &job
	case !errors.Is(err, sql.ErrNoRows):
		return ProjectDetail{}, err
	}
	// The films were added to the grant when saved: one lookup says where
	// the videos and the films stand.
	asked := slices.Clone(ids)
	for _, version := range detail.Versions {
		asked = append(asked, version.FilmAssetID)
	}
	status, err := s.lookup(ctx, row.grantID.String, asked)
	if err != nil {
		s.logger.WarnContext(ctx, "photo grant lookup failed", "project", projectID, "error", err)
		for i := range detail.Assets {
			detail.Assets[i].Status = AssetUnknown
		}
		return detail, nil
	}
	for i, version := range detail.Versions {
		looked, ok := status[version.FilmAssetID]
		detail.Versions[i].Gone = !ok || looked.Status != photos.GrantAvailable
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

// lookup asks where each of ids stands in the grant, as many at a time as
// the photo library takes. A grant that is gone, as after the Catalog was
// restored to before it, leaves every video missing.
func (s *Store) lookup(ctx context.Context, grantID string, ids []string) (map[string]photos.GrantedAsset, error) {
	status := map[string]photos.GrantedAsset{}
	if grantID == "" {
		return status, nil
	}
	looked, err := s.lookupAssets(ctx, grantID, ids)
	if photosapi.GrantErrorCode(err) == photosapi.GrantNotFound {
		return status, nil
	}
	if err != nil {
		return nil, err
	}
	for _, asset := range looked {
		status[asset.AssetID] = asset
	}
	return status, nil
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

// DeleteProject removes owner's project and revokes its grant, without
// waiting for the photo library: the revocation is queued with the
// deletion and tried at once in the background, then every UpkeepInterval
// until it goes through. The videos and the films stay in the photo
// library.
func (s *Store) DeleteProject(ctx context.Context, owner, projectID string) error {
	unlock, err := s.members.Lock(ctx, owner)
	if err != nil {
		return err
	}
	defer unlock()
	var grantID sql.NullString
	var jobs []string
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		row, err := s.project(ctx, tx, owner, projectID)
		if err != nil {
			return err
		}
		if row.state != projectReady {
			return ErrNotFound
		}
		grantID = row.grantID
		// The jobs go with the project; a running one is stopped below.
		if jobs, err = jobIDs(ctx, tx, "SELECT id FROM jobs WHERE project_id = ?", projectID); err != nil {
			return err
		}
		if grantID.Valid {
			if err := s.queueRevocation(ctx, tx, grantID.String); err != nil {
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
	for _, id := range jobs {
		s.cancelRunning(id)
		s.removeJobFiles(id)
	}
	// A job that wanted a planning round went with the project.
	s.syncMarkers(ctx)
	s.songs.Lock()
	s.removeUnusedCopies(ctx)
	s.songs.Unlock()
	if grantID.Valid {
		s.revokeSoon(grantID.String)
	}
	return nil
}

func jobIDs(ctx context.Context, q queryer, query, id string) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// queueRevocation records that grantID is to be revoked, due now.
func (s *Store) queueRevocation(ctx context.Context, q queryer, grantID string) error {
	_, err := q.ExecContext(ctx, "INSERT INTO revocations(grant_id, next_try) VALUES(?, ?) ON CONFLICT DO NOTHING",
		grantID, sqlitedb.FormatTime(s.now()))
	return err
}

// revokeSoon revokes a queued grant in the background.
func (s *Store) revokeSoon(grantID string) {
	s.soon(func(ctx context.Context) { s.revoke(ctx, grantID) })
}

// revoke revokes a grant waiting in revocations, and forgets it once the
// photo library has.
func (s *Store) revoke(ctx context.Context, grantID string) {
	if err := s.grants.Revoke(ctx, grantID); err != nil {
		s.logger.WarnContext(ctx, "revoking a photo grant failed; trying again later", "grant", grantID, "error", err)
		_, _ = s.db.ExecContext(ctx, `UPDATE revocations SET attempts = attempts + 1,
			next_try = ? WHERE grant_id = ?`, sqlitedb.FormatTime(s.now().Add(UpkeepInterval)), grantID)
		return
	}
	_, _ = s.db.ExecContext(ctx, "DELETE FROM revocations WHERE grant_id = ?", grantID)
}

// dueRevocations lists the grants whose revocation is due.
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

// RetryRevocations revokes the grants whose revocation is due: those the
// photo library missed, and those reconciling queued.
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

// Upkeep keeps the store in line with the photo library: it reconciles
// while the store has not been reconciled yet or holds unfinished batches,
// then revokes the grants whose revocation is due. The montage service runs
// it every UpkeepInterval beside the requests; a failure is tried again the
// next time.
func (s *Store) Upkeep(ctx context.Context) {
	if !s.reconciled.Load() || s.unfinished(ctx) {
		if err := s.reconcile(ctx); err != nil {
			s.logger.WarnContext(ctx, "reconciling projects with the photo library failed; trying again", "error", err)
		}
	}
	if err := s.RetryRevocations(ctx); err != nil {
		s.logger.WarnContext(ctx, "retrying grant revocations failed", "error", err)
	}
	// A planning grant that ran out, or a marker a failed sync left, does
	// not keep the waker starting a member's unit.
	s.syncMarkers(ctx)
}

// unfinished reports whether a batch is still registering, or the store
// could not say.
func (s *Store) unfinished(ctx context.Context) bool {
	var found bool
	err := s.db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM batches WHERE state = 'registering')").Scan(&found)
	return err != nil || found
}

// reconcile brings the projects in line with the photo library's grants:
// montage.db and the Catalog may have been restored to different times, and
// a request may have stopped half way. Member by member, under the member's
// lock, so that no registration is half done while it looks:
//   - a grant no project holds is queued for revocation;
//   - a project whose grant was registered again, by an addition after the
//     Catalog lost the first, takes that grant;
//   - an unfinished batch is settled by the round after the one that first
//     found it, whose list of grants was taken after its request ended;
//   - a project whose grant is gone keeps its videos, which show as missing
//     (Project).
func (s *Store) reconcile(ctx context.Context) error {
	s.reconciling.Lock()
	defer s.reconciling.Unlock()
	grants, err := s.grants.List(ctx)
	if err != nil {
		return grantError(err)
	}
	held := map[string][]photos.Grant{}
	for _, grant := range grants {
		held[grant.OwnerUserID] = append(held[grant.OwnerUserID], grant)
	}
	owners, err := s.owners(ctx)
	if err != nil {
		return err
	}
	for owner := range held {
		owners = append(owners, owner)
	}
	slices.Sort(owners)
	first, seen := !s.reconciled.Load(), map[string]bool{}
	for _, owner := range slices.Compact(owners) {
		if err := s.reconcileMember(ctx, owner, held[owner], seen, first); err != nil {
			return err
		}
	}
	s.seen = seen
	s.reconciled.Store(true)
	return nil
}

func (s *Store) owners(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT DISTINCT owner_user_id FROM projects")
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

func (s *Store) reconcileMember(ctx context.Context, owner string, grants []photos.Grant, seen map[string]bool, first bool) error {
	unlock, err := s.members.Lock(ctx, owner)
	if err != nil {
		return err
	}
	defer unlock()
	projects, err := s.memberProjects(ctx, owner)
	if err != nil {
		return err
	}
	listed := map[string]string{}
	for _, grant := range grants {
		listed[grant.ProjectID] = grant.ID
		project, known := projects[grant.ProjectID]
		switch {
		case !known:
			s.logger.InfoContext(ctx, "revoking a photo grant no project holds", "grant", grant.ID)
			if err := s.queueRevocation(ctx, s.db, grant.ID); err != nil {
				return err
			}
		case project.state == projectReady && project.grantID != grant.ID:
			s.logger.InfoContext(ctx, "a project takes the grant registered for it again", "project", grant.ProjectID, "grant", grant.ID)
			if _, err := s.db.ExecContext(ctx, "UPDATE projects SET grant_id = ? WHERE id = ?", grant.ID, grant.ProjectID); err != nil {
				return err
			}
			project.grantID = grant.ID
			projects[grant.ProjectID] = project
		}
	}
	batches, err := s.unfinishedBatches(ctx, owner)
	if err != nil {
		return err
	}
	for _, b := range batches {
		if !s.seen[b.id] {
			seen[b.id] = true
			continue
		}
		grantID := projects[b.projectID].grantID
		if grantID == "" {
			grantID = listed[b.projectID]
		}
		// A revocation it queues goes out with Upkeep's RetryRevocations.
		if _, err := s.settle(ctx, b, grantID); err != nil {
			s.logger.WarnContext(ctx, "settling an unfinished batch failed; trying again", "batch", b.id, "error", err)
			seen[b.id] = true
		}
	}
	if first {
		for id, project := range projects {
			if project.state == projectReady && listed[id] == "" {
				s.logger.WarnContext(ctx, "a project's photo grant is gone; its videos show as missing", "project", id)
			}
		}
	}
	return nil
}

// knownProject is what reconciling needs of a project.
type knownProject struct {
	state, grantID string
}

func (s *Store) memberProjects(ctx context.Context, owner string) (map[string]knownProject, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, state, COALESCE(grant_id, '') FROM projects WHERE owner_user_id = ?", owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	projects := map[string]knownProject{}
	for rows.Next() {
		var id string
		var project knownProject
		if err := rows.Scan(&id, &project.state, &project.grantID); err != nil {
			return nil, err
		}
		projects[id] = project
	}
	return projects, rows.Err()
}

func (s *Store) unfinishedBatches(ctx context.Context, owner string) ([]batchRow, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, project_id, kind, asset_ids FROM batches WHERE owner_user_id = ? AND state = 'registering'", owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var batches []batchRow
	for rows.Next() {
		b := batchRow{state: batchRegistering}
		var assets string
		if err := rows.Scan(&b.id, &b.projectID, &b.kind, &assets); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(assets), &b.assets); err != nil {
			return nil, err
		}
		batches = append(batches, b)
	}
	return batches, rows.Err()
}

// RemoveAccount forgets a deleted account's projects. Its grants went with
// the photo library's own step, which runs first.
func (s *Store) RemoveAccount(ctx context.Context, userID string) error {
	unlock, err := s.members.Lock(ctx, userID)
	if err != nil {
		return err
	}
	defer unlock()
	var jobs []string
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if jobs, err = jobIDs(ctx, tx, "SELECT id FROM jobs WHERE owner_user_id = ?", userID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM projects WHERE owner_user_id = ?", userID); err != nil {
			return err
		}
		// Their planning grant goes too: the name may be another member's later.
		if _, err := tx.ExecContext(ctx, "DELETE FROM plan_grants WHERE owner_user_id = ?", userID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM songs WHERE owner_user_id = ?", userID)
		return err
	})
	if err != nil {
		return err
	}
	for _, id := range jobs {
		s.cancelRunning(id)
		s.removeJobFiles(id)
	}
	s.syncMarkers(ctx)
	s.songs.Lock()
	defer s.songs.Unlock()
	s.removeUnusedCopies(ctx)
	return nil
}

// versions lists a project's films, newest first.
func (s *Store) versions(ctx context.Context, projectID string) ([]Version, error) {
	return s.versionsOf(ctx, "project_id", projectID, "number DESC")
}
