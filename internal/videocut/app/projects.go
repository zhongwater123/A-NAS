package app

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
	"github.com/zhongwater123/A-NAS/internal/videocut/db"
	"github.com/zhongwater123/A-NAS/internal/videocut/domain"
)

// A project holds only the videos a member handed to the montage center
// (docs/specs/montage-center.md): each batch adds its own, fixed to their
// content objects. The photo library keeps the videos and lets the montage
// center reach them through the project's grant (ADR 0038 decision 3).

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

const (
	AssetAvailable domain.AssetStatus = "available"
	AssetTrashed   domain.AssetStatus = "trashed"
	AssetMissing   domain.AssetStatus = "missing"
	// AssetUnknown: the photo library did not answer this time.
	AssetUnknown domain.AssetStatus = "unknown"
)

// previewCount is how many videos a project in the list shows.
const previewCount = 6

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
		return nil, domain.ErrTooManyAssets
	}
	out := slices.Clone(ids)
	for _, id := range out {
		if !printable(id, MaxIDLength) {
			return nil, domain.ErrInvalid
		}
	}
	slices.Sort(out)
	out = slices.Compact(out)
	// No videos is no project: never all of the library (#514).
	if len(out) == 0 {
		return nil, domain.ErrInvalid
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

// assetGone reports whether the photo library answered that a video a
// project holds can no longer be read: deleted, or in the trash. Where a
// video is read rather than added, that is not a video that cannot be added
// (GrantError), and the reader says what it means there.
func assetGone(err error) bool {
	code := photosapi.GrantErrorCode(err)
	return code == photosapi.GrantAssetNotFound || code == photosapi.GrantAssetsRefused
}

func (s *Store) project(ctx context.Context, q queryer, owner, id string) (db.ProjectRow, error) {
	row, err := q.Project(ctx, owner, id)
	if errors.Is(err, sql.ErrNoRows) {
		return db.ProjectRow{}, domain.ErrNotFound
	}
	return row, err
}

// queryer is the database or one transaction in it (db.DB, db.Tx).
type queryer interface {
	JobSettings(ctx context.Context, jobID string) (string, error)
	KeyedJob(ctx context.Context, owner, requestKey, column string) (id, kind string, subject sql.NullString, err error)
	PlanGrant(ctx context.Context, owner string) (username, at string, err error)
	Project(ctx context.Context, owner, id string) (db.ProjectRow, error)
	ProjectSong(ctx context.Context, owner, projectID string) (string, db.ProjectSong, error)
	QueueRevocation(ctx context.Context, grantID string, at time.Time) error
	RoundStanding(ctx context.Context, owner, jobID, token string) (db.RoundStanding, error)
	Song(ctx context.Context, id string) (db.SongRow, error)
}

// batch finds owner's batch for requestKey, or starts one: a create batch
// with its project in the registering state, named name, or an add batch on
// projectID. A request key repeats one request: with another kind of
// request, another project or other videos it is ErrConflict.
func (s *Store) batch(ctx context.Context, owner, requestKey, kind, projectID, name string, ids []string) (db.BatchRow, error) {
	var row db.BatchRow
	err := s.db.Update(ctx, func(tx *db.Tx) error {
		var err error
		row, err = tx.Batch(ctx, owner, requestKey)
		if err == nil {
			if row.Kind != kind || kind == kindAdd && row.ProjectID != projectID || !slices.Equal(row.Assets, ids) {
				return domain.ErrConflict
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		at := s.now()
		if kind == kindCreate {
			projectID = newID("project")
			if err := tx.AddRegisteringProject(ctx, projectID, owner, name, at); err != nil {
				return err
			}
		}
		row = db.BatchRow{ID: newID("batch"), ProjectID: projectID, Kind: kind, State: batchRegistering, Assets: ids}
		return tx.AddBatch(ctx, owner, requestKey, row, at)
	})
	return row, err
}

// register grants a registering batch's videos to its project, with the
// member's session, and records them. A refusal undoes the batch at once.
// A batch the photo library did not answer for stays registering: the
// member repeats it with its request key, or reconciling settles it. It
// runs under the member's lock.
func (s *Store) register(ctx context.Context, owner, session string, b db.BatchRow) error {
	grant, err := s.grants.Register(ctx, session, b.ProjectID, b.Assets)
	if err != nil {
		err = domain.GrantError(err)
		if !errors.Is(err, domain.ErrPhotosUnavailable) {
			// Refused: nothing was granted.
			s.undo(ctx, b, "")
		}
		return err
	}
	if b.Kind == kindCreate {
		// Settling a batch that does not finish revokes this grant.
		if err := s.db.SetRegisteringGrant(ctx, b.ProjectID, grant.ID); err != nil {
			return err
		}
	}
	looked, err := s.grants.Lookup(ctx, grant.ID, b.Assets)
	switch {
	case err != nil:
		err = domain.GrantError(err)
	case len(looked) != len(b.Assets):
		err = fmt.Errorf("%w: the grant answered %d of %d videos", domain.ErrPhotosUnavailable, len(looked), len(b.Assets))
	default:
		for _, asset := range looked {
			if asset.Status != photos.GrantAvailable {
				// It left the member's sight after the grant took it.
				err = domain.ErrAssetsRefused
			}
		}
	}
	if err != nil {
		if !errors.Is(err, domain.ErrPhotosUnavailable) {
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
	return s.db.Update(ctx, func(tx *db.Tx) error {
		at := s.now()
		for _, asset := range looked {
			if err := tx.AddProjectAsset(ctx, b.ProjectID, asset.AssetID, asset.ObjectID, b.ID, at); err != nil {
				return err
			}
		}
		// An addition to a project whose grant was lost registered a new one.
		if err := tx.ReadyProject(ctx, owner, b.ProjectID, grant.ID, defaultName(earliest, latest, s.now()), at); err != nil {
			return err
		}
		return tx.FinishBatch(ctx, b.ID)
	})
}

// undo settles a batch its request gave up on, and revokes the grant that
// queued. What it could not undo now, reconciling settles later.
func (s *Store) undo(ctx context.Context, b db.BatchRow, grantID string) {
	revoke, err := s.settle(ctx, b, grantID)
	if err != nil {
		s.logger.WarnContext(ctx, "undoing a refused batch failed; reconciling settles it", "batch", b.ID, "error", err)
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
func (s *Store) settle(ctx context.Context, b db.BatchRow, grantID string) (string, error) {
	if b.Kind == kindCreate {
		var revoke string
		err := s.db.Update(ctx, func(tx *db.Tx) error {
			recorded, err := tx.RegisteringGrant(ctx, b.ProjectID)
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
			return tx.DeleteProject(ctx, b.ProjectID)
		})
		return revoke, err
	}
	if grantID != "" {
		held, err := s.heldAssets(ctx, b.ProjectID, b.Assets)
		if err != nil {
			return "", err
		}
		drop := slices.DeleteFunc(slices.Clone(b.Assets), func(id string) bool { return held[id] })
		if len(drop) > 0 {
			if err := s.grants.Remove(ctx, grantID, drop); err != nil && photosapi.GrantErrorCode(err) != photosapi.GrantNotFound {
				return "", domain.GrantError(err)
			}
		}
	}
	return "", s.db.DeleteRegisteringBatch(ctx, b.ID)
}

// heldAssets reports which of ids the project holds.
func (s *Store) heldAssets(ctx context.Context, projectID string, ids []string) (map[string]bool, error) {
	return s.db.HeldAssets(ctx, projectID, ids)
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
func (s *Store) CreateProject(ctx context.Context, owner, session, requestKey, name string, assetIDs []string) (domain.ProjectDetail, bool, error) {
	ids, err := uniqueIDs(assetIDs)
	if err != nil {
		return domain.ProjectDetail{}, false, err
	}
	name = strings.TrimSpace(name)
	if !printable(requestKey, MaxRequestKey) || !validText(name, MaxNameRunes) {
		return domain.ProjectDetail{}, false, domain.ErrInvalid
	}
	unlock, err := s.members.Lock(ctx, owner)
	if err != nil {
		return domain.ProjectDetail{}, false, err
	}
	defer unlock()
	b, err := s.batch(ctx, owner, requestKey, kindCreate, "", name, ids)
	if err != nil {
		return domain.ProjectDetail{}, false, err
	}
	// A batch still registering is new, or one the photo library did not
	// answer for before: the member has not had its project yet.
	created := b.State == batchRegistering
	if created {
		if err := s.register(ctx, owner, session, b); err != nil {
			return domain.ProjectDetail{}, false, err
		}
	}
	detail, err := s.Project(ctx, owner, b.ProjectID)
	return detail, created, err
}

// AddAssets adds assetIDs to owner's project. The same requestKey again
// adds nothing more. Versions already made stay as they are, and nothing is
// made anew.
func (s *Store) AddAssets(ctx context.Context, owner, session, projectID, requestKey string, assetIDs []string) (domain.ProjectDetail, error) {
	ids, err := uniqueIDs(assetIDs)
	if err != nil {
		return domain.ProjectDetail{}, err
	}
	if !printable(requestKey, MaxRequestKey) {
		return domain.ProjectDetail{}, domain.ErrInvalid
	}
	unlock, err := s.members.Lock(ctx, owner)
	if err != nil {
		return domain.ProjectDetail{}, err
	}
	defer unlock()
	row, err := s.project(ctx, s.db, owner, projectID)
	if err != nil {
		return domain.ProjectDetail{}, err
	}
	if row.State != projectReady {
		return domain.ProjectDetail{}, domain.ErrNotFound
	}
	b, err := s.batch(ctx, owner, requestKey, kindAdd, projectID, "", ids)
	if err != nil {
		return domain.ProjectDetail{}, err
	}
	if b.State == batchRegistering {
		if err := s.register(ctx, owner, session, b); err != nil {
			return domain.ProjectDetail{}, err
		}
	}
	return s.Project(ctx, owner, projectID)
}

// Projects lists owner's projects, newest first, from cursor.
func (s *Store) Projects(ctx context.Context, owner, cursor string) (domain.ProjectPage, error) {
	var after *db.ProjectCursor
	if cursor != "" {
		at, id, ok := strings.Cut(cursor, "|")
		if !ok {
			return domain.ProjectPage{}, domain.ErrInvalid
		}
		after = &db.ProjectCursor{At: at, ID: id}
	}
	// The store has one connection: the page's rows are read whole before
	// the previews are.
	listed, err := s.db.ProjectList(ctx, owner, after, pageSize+1)
	if err != nil {
		return domain.ProjectPage{}, err
	}
	page := domain.ProjectPage{Items: []domain.Project{}}
	var lastAt string
	for _, row := range listed {
		project := domain.Project{ID: row.ID, Name: row.Name, AssetCount: row.AssetCount, ChosenVersionID: row.ChosenVersionID,
			ChosenFilmAssetID: row.ChosenFilmAssetID}
		created := row.Created
		if len(page.Items) == pageSize {
			page.Next = lastAt + "|" + page.Items[pageSize-1].ID
			break
		}
		if project.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
			return domain.ProjectPage{}, err
		}
		page.Items, lastAt = append(page.Items, project), created
	}
	for i := range page.Items {
		if page.Items[i].PreviewAssetIDs, err = s.previews(ctx, page.Items[i].ID); err != nil {
			return domain.ProjectPage{}, err
		}
	}
	return page, nil
}

// previews picks up to previewCount of a project's videos, evenly spread.
func (s *Store) previews(ctx context.Context, projectID string) ([]string, error) {
	all, err := s.db.ProjectAssetIDs(ctx, projectID)
	if err != nil {
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
func (s *Store) Project(ctx context.Context, owner, projectID string) (domain.ProjectDetail, error) {
	row, err := s.project(ctx, s.db, owner, projectID)
	if err != nil {
		return domain.ProjectDetail{}, err
	}
	if row.State != projectReady {
		return domain.ProjectDetail{}, domain.ErrNotFound
	}
	kept, err := s.db.ProjectAssets(ctx, projectID)
	if err != nil {
		return domain.ProjectDetail{}, err
	}
	detail := domain.ProjectDetail{ID: row.ID, Name: row.Name, CreatedAt: row.Created, Ask: row.Ask,
		Chronological: row.Chronological, Assets: []domain.ProjectAsset{}}
	objects := map[string]string{}
	var ids []string
	for _, k := range kept {
		asset := domain.ProjectAsset{AssetID: k.AssetID, AddedAt: k.AddedAt}
		objects[asset.AssetID], ids = k.ObjectID, append(ids, asset.AssetID)
		detail.Assets = append(detail.Assets, asset)
	}
	detail.AssetCount = len(detail.Assets)
	if detail.Versions, err = s.versions(ctx, projectID); err != nil {
		return domain.ProjectDetail{}, err
	}
	for _, version := range detail.Versions {
		if version.ID == row.Chosen {
			detail.ChosenVersionID, detail.ChosenFilmAssetID = version.ID, version.FilmAssetID
		}
	}
	switch latest, err := s.db.LatestJob(ctx, projectID); {
	case err == nil:
		job, err := s.Job(ctx, owner, latest)
		if err != nil {
			return domain.ProjectDetail{}, err
		}
		detail.Job = &job
	case !errors.Is(err, sql.ErrNoRows):
		return domain.ProjectDetail{}, err
	}
	// The films were added to the grant when saved: one lookup says where
	// the videos and the films stand.
	asked := slices.Clone(ids)
	for _, version := range detail.Versions {
		asked = append(asked, version.FilmAssetID)
	}
	status, err := s.lookup(ctx, row.GrantID.String, asked)
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
			detail.Assets[i].Status = domain.AssetStatus(looked.Status)
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
func (s *Store) UpdateProject(ctx context.Context, owner, projectID string, change domain.ProjectChange) (domain.ProjectDetail, error) {
	var fields db.ProjectChange
	given := 0
	if change.Name != nil {
		name := strings.TrimSpace(*change.Name)
		if name == "" || !validText(name, MaxNameRunes) {
			return domain.ProjectDetail{}, domain.ErrInvalid
		}
		fields.Name, given = &name, given+1
	}
	if change.Ask != nil {
		if !validText(*change.Ask, MaxAskRunes) {
			return domain.ProjectDetail{}, domain.ErrInvalid
		}
		ask := strings.TrimSpace(*change.Ask)
		fields.Ask, given = &ask, given+1
	}
	if change.Chronological != nil {
		chronological := *change.Chronological
		fields.Chronological, given = &chronological, given+1
	}
	if given == 0 && change.SongStart == nil {
		return domain.ProjectDetail{}, domain.ErrInvalid
	}
	if change.SongStart != nil {
		// The start is checked against the song first: a start that leaves
		// no usable excerpt changes nothing.
		if _, err := s.SongStart(ctx, owner, projectID, *change.SongStart); err != nil {
			return domain.ProjectDetail{}, err
		}
		if given == 0 {
			return s.Project(ctx, owner, projectID)
		}
	}
	changed, err := s.db.UpdateReadyProject(ctx, owner, projectID, fields, s.now())
	if err != nil {
		return domain.ProjectDetail{}, err
	}
	if changed == 0 {
		return domain.ProjectDetail{}, errors.Join(domain.ErrNotFound, err)
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
	err = s.db.Update(ctx, func(tx *db.Tx) error {
		row, err := s.project(ctx, tx, owner, projectID)
		if err != nil {
			return err
		}
		if row.State != projectReady {
			return domain.ErrNotFound
		}
		grantID = row.GrantID
		// The jobs go with the project; a running one is stopped below.
		if jobs, err = tx.ProjectJobIDs(ctx, projectID); err != nil {
			return err
		}
		if grantID.Valid {
			if err := s.queueRevocation(ctx, tx, grantID.String); err != nil {
				return err
			}
		}
		if err = tx.DeleteProject(ctx, projectID); err != nil {
			return err
		}
		return tx.ForgetUnusedSongs(ctx, owner)
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

// queueRevocation records that grantID is to be revoked, due now.
func (s *Store) queueRevocation(ctx context.Context, q queryer, grantID string) error {
	return q.QueueRevocation(ctx, grantID, s.now())
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
		_ = s.db.PostponeRevocation(ctx, grantID, s.now().Add(UpkeepInterval))
		return
	}
	_ = s.db.DeleteRevocation(ctx, grantID)
}

// dueRevocations lists the grants whose revocation is due.
func (s *Store) dueRevocations(ctx context.Context) ([]string, error) {
	return s.db.DueRevocations(ctx, s.now())
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
	found, err := s.db.AnyBatchRegistering(ctx)
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
		return domain.GrantError(err)
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
	return s.db.ProjectOwners(ctx)
}

func (s *Store) reconcileMember(ctx context.Context, owner string, grants []photos.Grant, seen map[string]bool, first bool) error {
	unlock, err := s.members.Lock(ctx, owner)
	if err != nil {
		return err
	}
	defer unlock()
	projects, err := s.db.MemberProjects(ctx, owner)
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
		case project.State == projectReady && project.GrantID != grant.ID:
			s.logger.InfoContext(ctx, "a project takes the grant registered for it again", "project", grant.ProjectID, "grant", grant.ID)
			if err := s.db.SetProjectGrant(ctx, grant.ProjectID, grant.ID); err != nil {
				return err
			}
			project.GrantID = grant.ID
			projects[grant.ProjectID] = project
		}
	}
	batches, err := s.db.RegisteringBatches(ctx, owner)
	if err != nil {
		return err
	}
	for _, b := range batches {
		if !s.seen[b.ID] {
			seen[b.ID] = true
			continue
		}
		grantID := projects[b.ProjectID].GrantID
		if grantID == "" {
			grantID = listed[b.ProjectID]
		}
		// A revocation it queues goes out with Upkeep's RetryRevocations.
		if _, err := s.settle(ctx, b, grantID); err != nil {
			s.logger.WarnContext(ctx, "settling an unfinished batch failed; trying again", "batch", b.ID, "error", err)
			seen[b.ID] = true
		}
	}
	if first {
		for id, project := range projects {
			if project.State == projectReady && listed[id] == "" {
				s.logger.WarnContext(ctx, "a project's photo grant is gone; its videos show as missing", "project", id)
			}
		}
	}
	return nil
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
	err = s.db.Update(ctx, func(tx *db.Tx) error {
		if jobs, err = tx.OwnerJobIDs(ctx, userID); err != nil {
			return err
		}
		if err := tx.DeleteOwnerProjects(ctx, userID); err != nil {
			return err
		}
		// Their planning grant goes too: the name may be another member's later.
		if err := tx.DeletePlanGrant(ctx, userID); err != nil {
			return err
		}
		return tx.DeleteOwnerSongs(ctx, userID)
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
func (s *Store) versions(ctx context.Context, projectID string) ([]domain.Version, error) {
	return s.versionsOf(ctx, "project_id", projectID, "number DESC")
}
