package app

import (
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
	"github.com/zhongwater123/A-NAS/internal/videocut/db"
	"github.com/zhongwater123/A-NAS/internal/videocut/domain"
	"github.com/zhongwater123/A-NAS/internal/videocutcompute"
)

// Each step first works out its input key (docs/architecture/montage-
// center.md, 步骤与输入摘要): a step of the job already done with the same
// key is not done again, and every key includes the keys upstream, so a
// change upstream redoes what follows. The keys come only from the job's
// snapshot, never from the project as it is now.

// Versions of what the steps compute; a new version redoes the step.
const (
	catalogVersion = "catalog/v1"
	promptVersion  = "plan/v8"
	fillVersion    = "fill/v10"
	renderVersion  = "render/v1"
)

func digest(parts ...string) string {
	sum := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprintf(sum, "%d:%s\n", len(part), part)
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func digestJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return digest(string(encoded))
}

// jobRun is what a run of a job knows: its snapshot and each step's key and
// result as the steps go.
type jobRun struct {
	claimed
	grantID       string
	replayingSave bool
	assets        []db.JobAsset
	keys          map[string]string
	// The steps' results.
	analysis analysisResult
	catalog  catalogResult
	excerpt  domain.Excerpt
	// plan is the 方案 whose steps run, from 1 (0 before the first):
	// picked to render are its own.
	plan     int
	picked   json.RawMessage
	planned  json.RawMessage
	timeline timelineResult
	render   renderResult
}

// stepKey names a step's input for a run: a step whose key is the one its
// stored result was made with is not run again.
type stepKey = func(ctx context.Context, run *jobRun) (string, error)

// step is one step: its key from the run so far, and doing it.
type step struct {
	name string
	// plan is the 方案 the step belongs to, 0 for the steps all share.
	plan int
	key  stepKey
	do   func(ctx context.Context, run *jobRun) (any, error)
	// load takes a stored result back into the run.
	load func(run *jobRun, result []byte) error
	// reusable reports whether a stored result can still be used, beyond
	// its key: the render's file must still be there.
	reusable func(run *jobRun) bool
}

// planStep names a step of the plan-th 方案: the first keeps the plain
// names, the others add their number ("pick-2").
func planStep(name string, plan int) string {
	if plan <= 1 {
		return name
	}
	return fmt.Sprintf("%s-%d", name, plan)
}

// planStepSQL is planStep as SQL, for a step's plain name and the column
// that holds the 方案 (TestPlanStepsAreNamedAlikeInSQL).
func planStepSQL(name, column string) string {
	return "CASE WHEN " + column + " <= 1 THEN '" + name + "' ELSE '" + name + "-' || " + column + " END"
}

// planOf is a step's plain name and the 方案 it belongs to.
func planOf(name string) (string, int) {
	base, number, found := strings.Cut(name, "-")
	if !found {
		return name, 1
	}
	plan, err := strconv.Atoi(number)
	if err != nil || plan < 2 {
		return name, 1
	}
	return base, plan
}

// planKey is the first step's input key of the plan-th 方案. The first
// 方案's is pickKey itself, which a round plans for; a later one's adds its
// number, so that it is planned anew and its answers are kept apart. The
// tools work it out as the job's steps do (briefs.go).
func planKey(pickKey string, plan int) string {
	if plan <= 1 {
		return pickKey
	}
	return digest(pickKey, fmt.Sprintf("plan %d", plan))
}

// steps are a job's steps, in order (docs/architecture/montage-center.md,
// 两个方案): analysis, catalog and song, which every 方案 shares, then each
// 方案's own two steps of planning, fill, render and save, for as many 方案
// as the job started with. The first 方案 saved, the job goes on with the
// next; the last one's save ends the job. The list is the one place that
// says the order: what a job shows (Job) and which steps a new input makes
// stale (startStep) read it.
func (s *Store) steps(kind string, plans int) []step {
	if kind == KindEdit {
		return s.editSteps()
	}
	steps := []step{
		{name: "analysis", key: s.analysisKey, do: s.analyse,
			load: func(run *jobRun, b []byte) error { return json.Unmarshal(b, &run.analysis) }},
		{name: "catalog", key: func(_ context.Context, run *jobRun) (string, error) {
			return digest(run.keys["analysis"], catalogVersion), nil
		}, do: s.makeCatalog, load: func(run *jobRun, b []byte) error { return json.Unmarshal(b, &run.catalog) }},
		{name: "song", key: s.songKey, do: s.excerptOf,
			load: func(run *jobRun, b []byte) error { return json.Unmarshal(b, &run.excerpt) }},
	}
	for plan := 1; plan <= plans; plan++ {
		pick, shots, render := planStep("pick", plan), planStep("shots", plan), planStep("render", plan)
		steps = append(steps,
			step{name: pick, plan: plan, key: func(_ context.Context, run *jobRun) (string, error) {
				if plan > 1 {
					return planKey(run.keys["pick"], plan), nil
				}
				parts := []string{run.keys["catalog"], run.settings.Ask, fmt.Sprint(run.settings.Chronological), run.keys["song"], promptVersion}
				if recut := run.settings.Recut; recut != nil {
					// A recut plans anew from its version's plan and sentence.
					parts = append(parts, RecutTemplate, recut.Change, string(recut.Base))
				}
				return digest(parts...), nil
			}, do: s.pick, load: func(run *jobRun, b []byte) error { run.picked = b; return nil }},
			step{name: shots, plan: plan, key: func(_ context.Context, run *jobRun) (string, error) {
				return shotsKey(run.keys[pick], run.picked), nil
			}, do: s.shots, load: func(run *jobRun, b []byte) error { run.planned = b; return nil }},
		)
		steps = append(steps, s.filmSteps(plan, func(_ context.Context, run *jobRun) (string, error) {
			return digest(run.keys[shots], digest(string(run.planned)), fillVersion), nil
		}, func(_ context.Context, run *jobRun) (string, error) {
			return run.id + "|" + run.keys[render], nil
		})...)
	}
	return steps
}

// filmSteps are the steps that make a 方案's film of what was planned: the
// fill, the render and the save, the same whatever kind of job planned it.
// fillKey names what the fill cuts from, and saveKey what saves the film
// once.
func (s *Store) filmSteps(plan int, fillKey, saveKey stepKey) []step {
	return []step{
		{name: planStep("fill", plan), plan: plan, key: fillKey, do: s.fill,
			load: func(run *jobRun, b []byte) error { return json.Unmarshal(b, &run.timeline) }},
		{name: planStep("render", plan), plan: plan, key: func(_ context.Context, run *jobRun) (string, error) {
			parts := []string{digestJSON(run.timeline.Clips), renderVersion, run.excerpt.Key}
			if plan > 1 {
				// Two 方案 with the same timeline are still two films.
				parts = append(parts, fmt.Sprintf("plan %d", plan))
			}
			return digest(parts...), nil
		}, do: s.renderFilm, load: func(run *jobRun, b []byte) error { return json.Unmarshal(b, &run.render) },
			reusable: s.filmKept},
		{name: planStep("save", plan), plan: plan, key: saveKey, do: s.save, load: func(*jobRun, []byte) error { return nil }},
	}
}

// stepNames are the names of the steps of a job that makes plans 方案, in order.
func (s *Store) stepNames(kind string, plans int) []string {
	steps := s.steps(kind, plans)
	names := make([]string, len(steps))
	for i, st := range steps {
		names[i] = st.name
	}
	return names
}

// savedPlans are the job's 方案 whose films are saved.
func (s *Store) savedPlans(ctx context.Context, q queryer, jobID string) (map[int]bool, error) {
	rows, err := q.QueryContext(ctx, "SELECT plan FROM versions WHERE job_id = ?", jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	saved := map[int]bool{}
	for rows.Next() {
		var plan int
		if err := rows.Scan(&plan); err != nil {
			return nil, err
		}
		saved[plan] = true
	}
	return saved, rows.Err()
}

// runSteps runs job from its first step not done. A 方案 whose film is saved
// is settled: the film is in the member's library whatever became of the
// job's videos since, so its steps are neither done again nor made stale by
// a new input, and the job makes only the 方案 not saved yet
// (docs/specs/montage-center.md, 素材进回收站或被删除: 已存进相册的成片不受影响).
func (s *Store) runSteps(ctx context.Context, job claimed) error {
	run := &jobRun{claimed: job, keys: map[string]string{}}
	// An interrupted save may already have committed in the photo library.
	// Replay its exact version key before inspecting changeable upstream inputs.
	var saving bool
	if err := s.db.QueryRowContext(ctx, "SELECT save_started FROM jobs WHERE id = ?", job.id).Scan(&saving); err != nil {
		return err
	}
	if saving {
		// The 方案 being saved is the first without a version: a version is
		// recorded in the transaction that releases the fence.
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) + 1 FROM versions WHERE job_id = ?", job.id).Scan(&run.plan); err != nil {
			return err
		}
		var rendered string
		var key string
		save := planStep("save", run.plan)
		err := s.db.QueryRowContext(ctx, `SELECT p.grant_id, r.result_json, w.input_key FROM jobs j
			JOIN projects p ON p.id=j.project_id JOIN steps r ON r.job_id=j.id AND r.name=?
			JOIN steps w ON w.job_id=j.id AND w.name=? WHERE j.id=?`, planStep("render", run.plan), save, job.id).Scan(&run.grantID, &rendered, &key)
		if err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(rendered), &run.render); err != nil {
			return err
		}
		run.replayingSave = true
		run.keys[save] = key
		if _, err = s.save(ctx, run); err != nil || s.finished(ctx, job.id) {
			return err
		}
		// A 方案 before the last: the job goes on with the next.
		run = &jobRun{claimed: job, keys: map[string]string{}}
	}
	if err := s.loadRun(ctx, run); err != nil {
		return err
	}
	saved, err := s.savedPlans(ctx, s.db, job.id)
	if err != nil {
		return err
	}
	steps := s.steps(job.kind, job.plans)
	for i, st := range steps {
		if s.cancelRequested(ctx, job.id) {
			return errCancelled
		}
		if st.plan != run.plan {
			// A 方案's steps begin: nothing of the one before is theirs.
			run.plan = st.plan
			run.picked, run.planned, run.timeline, run.render = nil, nil, timelineResult{}, renderResult{}
		}
		if saved[st.plan] && st.name != StepOutline {
			continue
		}
		// Every step starts by looking whether the videos are still there;
		// those of a 方案 already saved were passed over above.
		if err := s.checkAssets(ctx, job); err != nil {
			return err
		}
		if saved[st.plan] {
			// The first 方案's pick key is also what a round plans for
			// (ClaimRound, standing): its row follows the job's inputs though
			// the 方案 itself is settled.
			key, err := st.key(ctx, run)
			if err != nil {
				return err
			}
			run.keys[st.name] = key
			if _, err := s.db.ExecContext(ctx, "UPDATE steps SET input_key = ? WHERE job_id = ? AND name = ? AND input_key <> ?",
				key, job.id, st.name, key); err != nil {
				return err
			}
			continue
		}
		key, err := st.key(ctx, run)
		if err != nil {
			return err
		}
		run.keys[st.name] = key
		stored, done, err := s.storedStep(ctx, job.id, st.name, key)
		if err != nil {
			return err
		}
		if done {
			if err := st.load(run, stored); err != nil {
				return err
			}
			if st.reusable == nil || st.reusable(run) {
				continue
			}
		}
		if err := s.startStep(ctx, job.id, st, key, steps[i+1:], saved); err != nil {
			return err
		}
		result, err := st.do(ctx, run)
		if errors.Is(err, videocutcompute.ErrWorkerRefused) {
			return failed("计算服务拒绝处理此素材或规划: %v", err)
		}
		if err != nil {
			return err
		}
		if base, _ := planOf(st.name); base == "save" {
			saved[st.plan] = true
			continue // save records its step with the version, and the last 方案's the job's end
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		if err := s.finishStep(ctx, job.id, st.name, encoded); err != nil {
			return err
		}
		if err := st.load(run, encoded); err != nil {
			return err
		}
	}
	return nil
}

// loadRun reads the job's grant and videos.
func (s *Store) loadRun(ctx context.Context, run *jobRun) error {
	grantID, assets, err := s.jobGrant(ctx, run.id)
	if err != nil {
		return err
	}
	run.grantID = grantID
	kept, err := s.db.JobAssets(ctx, run.id)
	if err != nil {
		return err
	}
	run.assets = append(run.assets, kept...)
	if len(run.assets) != len(assets) {
		return fmt.Errorf("job %s lists its videos twice", run.id)
	}
	return nil
}

// jobGrant is the job's project grant and the job's videos. A project whose
// grant is gone has all its videos missing.
func (s *Store) jobGrant(ctx context.Context, jobID string) (string, []string, error) {
	var grantID sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT p.grant_id FROM jobs j JOIN projects p ON p.id = j.project_id WHERE j.id = ?`, jobID).
		Scan(&grantID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, domain.ErrNotFound
	}
	if err != nil {
		return "", nil, err
	}
	assets, err := s.db.JobAssetIDs(ctx, jobID)
	if err != nil {
		return "", nil, err
	}
	if !grantID.Valid {
		return "", assets, &stepNeeds{reason: NeedMissing, missing: assets}
	}
	return grantID.String, assets, nil
}

func (s *Store) storedStep(ctx context.Context, jobID, name, key string) ([]byte, bool, error) {
	var stored, state string
	var result sql.NullString
	err := s.db.QueryRowContext(ctx, "SELECT input_key, state, result_json FROM steps WHERE job_id = ? AND name = ?", jobID, name).
		Scan(&stored, &state, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return []byte(result.String), stored == key && state == "done" && result.Valid, nil
}

// startStep records the step running with its key; a step with another key
// starts over, and so does everything after it (later), but for the steps of
// a 方案 already saved.
func (s *Store) startStep(ctx context.Context, jobID string, st step, key string, later []step, saved map[int]bool) error {
	name := st.name
	at := s.now()
	now := sqlitedb.FormatTime(at)
	return s.db.Update(ctx, func(tx *db.Tx) error {
		var stored string
		err := tx.QueryRowContext(ctx, "SELECT input_key FROM steps WHERE job_id = ? AND name = ?", jobID, name).Scan(&stored)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			_, err = tx.ExecContext(ctx, "INSERT INTO steps(job_id, name, input_key, state, started_at) VALUES(?, ?, ?, 'running', ?)",
				jobID, name, key, now)
		case err != nil:
		case stored == key:
			// The same input: keep what the step saved as it went (the
			// fill's clear spans).
			_, err = tx.ExecContext(ctx, "UPDATE steps SET state = 'running', finished_at = NULL WHERE job_id = ? AND name = ?", jobID, name)
		default:
			_, err = tx.ExecContext(ctx, `UPDATE steps SET input_key = ?, state = 'running', result_json = NULL, started_at = ?, finished_at = NULL
				WHERE job_id = ? AND name = ?`, key, now, jobID, name)
		}
		if err != nil {
			return err
		}
		if stored != key {
			// A new input: what the steps after it kept is stale.
			for _, affected := range append([]step{st}, later...) {
				if saved[affected.plan] {
					continue
				}
				if affected.name != name {
					if _, err := tx.ExecContext(ctx, "DELETE FROM steps WHERE job_id = ? AND name = ?", jobID, affected.name); err != nil {
						return err
					}
				}
				if base, plan := planOf(affected.name); stored != "" && (base == StepOutline || base == StepShots) {
					if err := tx.VoidPlan(ctx, jobID, plan, base, at); err != nil {
						return err
					}
				}
			}
		}
		_, err = tx.ExecContext(ctx, "UPDATE jobs SET step = ?, wait_reason = '', updated_at = ? WHERE id = ?", name, now, jobID)
		return err
	})
}

// finishStep stores the step's result; the next step's attempts start
// from none.
func (s *Store) finishStep(ctx context.Context, jobID, name string, result []byte) error {
	now := sqlitedb.FormatTime(s.now())
	return s.db.Update(ctx, func(tx *db.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE steps SET state = 'done', result_json = ?, finished_at = ? WHERE job_id = ? AND name = ?",
			string(result), now, jobID, name); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE jobs SET attempts = 0, error_class = '', error = '', updated_at = ? WHERE id = ?", now, jobID)
		return err
	})
}

// saveProgress keeps what a step found so far, for a restart to go on from.
func (s *Store) saveProgress(ctx context.Context, jobID, name string, progress any) error {
	encoded, err := json.Marshal(progress)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "UPDATE steps SET result_json = ? WHERE job_id = ? AND name = ? AND state = 'running'", string(encoded), jobID, name)
	return err
}

// The photo library bounds each call, while a project may span several batches.
func (s *Store) lookupAssets(ctx context.Context, grantID string, ids []string) ([]photos.GrantedAsset, error) {
	var result []photos.GrantedAsset
	for batch := range slices.Chunk(ids, photos.MaxGrantAssets) {
		found, err := s.grants.Lookup(ctx, grantID, batch)
		if err != nil {
			return nil, err
		}
		result = append(result, found...)
	}
	return result, nil
}

func (s *Store) analyzeAssets(ctx context.Context, grantID string, ids []string, retry bool) error {
	for batch := range slices.Chunk(ids, photos.MaxGrantAssets) {
		if err := s.grants.Analyze(ctx, grantID, batch, retry); err != nil {
			return err
		}
	}
	return nil
}

const WaitPhotos = "photos_unavailable"

// NeedTooLong: a video is longer than the photo library analyses; trying
// again fails again, so the member takes it out. NeedModel: the photo
// library has no local model to describe the cards with; the member tries
// again once an administrator installed it.
const (
	NeedTooLong = "video_too_long"
	NeedModel   = "model_unavailable"
)

func jobGrantError(err error, ids []string) error {
	switch photosapi.GrantErrorCode(err) {
	case photosapi.GrantNotFound, photosapi.GrantAssetNotFound:
		return &stepNeeds{reason: NeedMissing, missing: ids, err: err}
	case photosapi.GrantOwnerInactive, photosapi.GrantUnavailable:
		return &stepWait{reason: WaitPhotos, delay: waitPoll}
	case photosapi.GrantInvalid, photosapi.GrantTooMany:
		return failed("photo library refused the job's inputs: %v", err)
	}
	return err
}

// checkAssets looks up the job's videos: a video in the trash makes the job
// wait, one gone or changed makes it need the member.
func (s *Store) checkAssets(ctx context.Context, job claimed) error {
	grantID, ids, err := s.jobGrant(ctx, job.id)
	if err != nil {
		return err
	}
	objects, err := s.db.JobAssetObjects(ctx, job.id)
	if err != nil {
		return err
	}
	looked, err := s.lookupAssets(ctx, grantID, ids)
	if err != nil {
		return jobGrantError(err, ids)
	}
	status := map[string]photos.GrantedAsset{}
	for _, asset := range looked {
		status[asset.AssetID] = asset
	}
	var missing, trashed []string
	for _, id := range ids {
		asset, ok := status[id]
		switch {
		case !ok || asset.Status == photos.GrantMissing:
			missing = append(missing, id)
		case asset.Status == photos.GrantTrashed:
			trashed = append(trashed, id)
		case asset.ObjectID != objects[id]:
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return &stepNeeds{reason: NeedMissing, missing: missing}
	}
	if len(trashed) > 0 {
		wait := &stepWait{reason: WaitTrashed, delay: lifecycleCheck}
		if job.kind == KindEdit {
			// Named, so that the shots of the videos in the trash can be
			// marked for the member.
			wait.missing = trashed
		}
		return wait
	}
	return nil
}

// --- analysis ---

// analysedVideo is one video's analysis as the job uses it.
type analysedVideo struct {
	videoHead
	Cards    []videocutcompute.CatalogCard `json:"cards"`
	Measures map[string]json.RawMessage    `json:"measures"`
}

// videoHead is a video's analysis without its cards and their measures:
// what reading a version needs, its catalog holding the cards as they were
// planned from (loadPlan).
type videoHead struct {
	AssetID              string          `json:"assetId"`
	ObjectID             string          `json:"objectId"`
	Dimensions           int             `json:"dimensions"`
	CardsIdentity        string          `json:"cardsIdentity"`
	QualityIdentity      string          `json:"qualityIdentity"`
	DescriptionsIdentity string          `json:"descriptionsIdentity"`
	Identity             string          `json:"identity"`
	Duration             float64         `json:"duration"`
	Width                int             `json:"width"`
	Height               int             `json:"height"`
	TakenAt              *time.Time      `json:"takenAt,omitempty"`
	Windows              []photos.Window `json:"windows"`
}

// sameWindows reports whether now holds the windows the video was analysed
// with: of its content, done, by the same model and of the same dimensions.
func (video analysedVideo) sameWindows(now photos.GrantAnalysis) bool {
	return now.ObjectID == video.ObjectID && now.Windows == photos.AnalysisDone &&
		now.Identity == video.Identity && now.Dimensions == video.Dimensions
}

// windowsChanged reports whether the photo library has made the video's
// windows again with another model or dimensions since: windows not done
// yet are not another model, and other content is not the video's.
func (video analysedVideo) windowsChanged(now photos.GrantAnalysis) bool {
	return now.ObjectID == video.ObjectID && now.Windows == photos.AnalysisDone && !video.sameWindows(now)
}

type analysisResult struct {
	Videos []analysedVideo `json:"videos"`
}

// allDone reports whether every stage of a video's analysis is done: the
// one rule for the lookup's states and for the analysis read after it.
func allDone(states ...photos.AnalysisState) bool {
	return !slices.ContainsFunc(states, func(state photos.AnalysisState) bool { return state != photos.AnalysisDone })
}

// allStages are every stage of a video's analysis: what planning reads.
func allStages(video photos.GrantedAsset) []photos.AnalysisState {
	return []photos.AnalysisState{video.Windows, video.Cards, video.Quality, video.Descriptions}
}

// analysisReady answers what keeps a step from reading the analysis of
// videos, by the stages of it the step reads: the videos too long to
// analyse and those whose analysis failed need the member, every such video
// at once, then a missing local model an administrator; a stage not done
// yet is asked for, again changing nothing for one already queued, and
// waited for. Nil when every stage is done.
func (s *Store) analysisReady(ctx context.Context, grantID string, videos []photos.GrantedAsset, stages func(photos.GrantedAsset) []photos.AnalysisState) error {
	var pending, broken, long []string
	unavailable := false
	for _, video := range videos {
		states := stages(video)
		switch {
		case video.Windows == photos.AnalysisFailed && photos.VideoTooLong(video.Duration):
			long = append(long, video.AssetID)
		case slices.Contains(states, photos.AnalysisUnavailable):
			unavailable = true
		case slices.Contains(states, photos.AnalysisFailed):
			broken = append(broken, video.AssetID)
		case !allDone(states...):
			pending = append(pending, video.AssetID)
		}
	}
	switch {
	case len(long) > 0:
		return &stepNeeds{reason: NeedTooLong, missing: long, err: errors.New("视频超过窗口分析时长上限，请移除此素材")}
	case len(broken) > 0:
		return &stepNeeds{reason: NeedAnalysis, missing: broken}
	case unavailable:
		return &stepNeeds{reason: NeedModel, err: errors.New("素材分析所需的本地模型不可用")}
	case len(pending) > 0:
		if err := s.analyzeAssets(ctx, grantID, pending, false); err != nil {
			return jobGrantError(err, pending)
		}
		return &stepWait{reason: WaitAnalysis, delay: waitPoll}
	}
	return nil
}

// analysisKey reads the photo library's analysis of every video: the key
// is their objects and what the analysis made of them. A video not
// analysed yet makes the job wait; one whose analysis failed needs the
// member.
func (s *Store) analysisKey(ctx context.Context, run *jobRun) (string, error) {
	ids := make([]string, len(run.assets))
	for i, asset := range run.assets {
		ids[i] = asset.AssetID
	}
	looked, err := s.lookupAssets(ctx, run.grantID, ids)
	if err != nil {
		return "", jobGrantError(err, ids)
	}
	about := map[string]photos.GrantedAsset{}
	for _, asset := range looked {
		about[asset.AssetID] = asset
	}
	// The states first: reading the analysis itself carries every
	// window's vector.
	var available []photos.GrantedAsset
	for _, asset := range run.assets {
		looked, ok := about[asset.AssetID]
		if ok && looked.Status == photos.GrantTrashed {
			return "", &stepWait{reason: WaitTrashed, delay: lifecycleCheck}
		}
		if !ok || looked.Status != photos.GrantAvailable || looked.ObjectID != asset.ObjectID {
			return "", &stepNeeds{reason: NeedMissing, missing: []string{asset.AssetID}}
		}
		available = append(available, looked)
	}
	if err := s.analysisReady(ctx, run.grantID, available, allStages); err != nil {
		return "", err
	}
	result := analysisResult{}
	for n, asset := range run.assets {
		analysis, _, err := s.grants.Analysis(ctx, run.grantID, asset.AssetID)
		if err != nil {
			return "", jobGrantError(err, []string{asset.AssetID})
		}
		if analysis.ObjectID != asset.ObjectID {
			return "", &stepNeeds{reason: NeedMissing, missing: []string{asset.AssetID}}
		}
		// The analysis may have gone stale since the lookup, as when the
		// windows are made again for a new model: the library then answers
		// with its state and leaves out what is no longer current. Planning
		// from that would leave the video's cards out, or find no footage
		// at all; the next round's lookup decides again.
		if !allDone(analysis.Windows, analysis.Cards, analysis.Quality, analysis.Described) {
			s.logger.InfoContext(ctx, "a video's analysis changed after it was looked up; waiting for it again", "asset", asset.AssetID,
				"windows", analysis.Windows, "cards", analysis.Cards, "quality", analysis.Quality, "descriptions", analysis.Described)
			return "", &stepWait{reason: WaitAnalysis, delay: waitPoll}
		}
		video := analysedVideo{AssetID: asset.AssetID, ObjectID: asset.ObjectID, Identity: analysis.Identity, Dimensions: analysis.Dimensions,
			CardsIdentity: analysis.CardsIdentity, QualityIdentity: analysis.QualityIdentity, DescriptionsIdentity: analysis.DescriptionsIdentity,
			Duration: about[asset.AssetID].Duration, Width: about[asset.AssetID].Width, Height: about[asset.AssetID].Height,
			TakenAt: about[asset.AssetID].TakenAt, Windows: analysis.List, Measures: map[string]json.RawMessage{}}
		described := map[int]json.RawMessage{}
		for _, d := range analysis.Descriptions {
			if d.Error == "" && len(d.Description) > 0 {
				described[d.Card] = d.Description
			}
		}
		for _, card := range analysis.CardList {
			answer, ok := described[card.Index]
			if !ok {
				continue // a card the reader could not describe cannot be planned
			}
			seen, ok := beatTimes(answer, card.Times)
			if !ok {
				s.logger.WarnContext(ctx, "a card's description does not place its beats among the key frames",
					"asset", asset.AssetID, "card", card.Index)
				continue
			}
			id := fmt.Sprintf("v%d-%d", n+1, card.Index)
			frames := make([]string, len(seen))
			for i := range seen {
				frames[i] = beatID(id, i+1)
			}
			video.Cards = append(video.Cards, videocutcompute.CatalogCard{ID: id, Video: asset.AssetID, Start: float64(card.StartMs) / 1000,
				End: float64(card.EndMs) / 1000, Times: seen, Frames: frames, Answer: answer})
			if card.Quality != nil {
				measured, err := json.Marshal(card.Quality)
				if err != nil {
					return "", err
				}
				video.Measures[id] = measured
			}
		}
		result.Videos = append(result.Videos, video)
	}
	run.analysis = result
	return digestJSON(result), nil
}

// beatTimes are the instants a description's beats were seen at, one a beat:
// its first key frame, which the beat's "frames" count from 1 among the
// card's (photos.CardDescriber). The photo library describes a card with
// more key frames than its reader holds from some of them, so a description
// may have fewer beats than the card has key frames, and the nth beat is not
// the nth key frame. The compute Worker's domain reads a card's times and
// frames by the beat's number (ai/anas_videocut/algo/fill.py). A
// description that does not place every beat cannot be cut from.
func beatTimes(description json.RawMessage, times []float64) ([]float64, bool) {
	var answer struct {
		Beats []struct {
			Frames []int `json:"frames"`
		} `json:"beats"`
	}
	if err := json.Unmarshal(description, &answer); err != nil || len(answer.Beats) == 0 {
		return nil, false
	}
	seen := make([]float64, len(answer.Beats))
	for n, beat := range answer.Beats {
		if len(beat.Frames) == 0 || beat.Frames[0] < 1 || beat.Frames[0] > len(times) {
			return nil, false
		}
		seen[n] = times[beat.Frames[0]-1]
	}
	return seen, true
}

func (s *Store) analyse(_ context.Context, run *jobRun) (any, error) { return run.analysis, nil }

// --- catalog ---

type catalogResult struct {
	Cards   map[string]videocutcompute.CatalogCard `json:"cards"`
	Catalog []string                               `json:"catalog"`
}

// makeCatalog lists the cards that may go into the film, in shooting order.
func (s *Store) makeCatalog(ctx context.Context, run *jobRun) (any, error) {
	videos := slices.Clone(run.analysis.Videos)
	slices.SortStableFunc(videos, func(a, b analysedVideo) int {
		switch {
		case a.TakenAt == nil && b.TakenAt == nil:
			return cmp.Compare(a.AssetID, b.AssetID)
		case a.TakenAt == nil:
			return 1
		case b.TakenAt == nil:
			return -1
		}
		return a.TakenAt.Compare(*b.TakenAt)
	})
	request := videocutcompute.CatalogRequest{Cards: []videocutcompute.CatalogCard{}, Measures: map[string]json.RawMessage{}}
	result := catalogResult{Cards: map[string]videocutcompute.CatalogCard{}}
	for _, video := range videos {
		for _, card := range video.Cards {
			request.Cards = append(request.Cards, card)
			result.Cards[card.ID] = card
		}
		maps.Copy(request.Measures, video.Measures)
	}
	if len(request.Cards) == 0 {
		return nil, &stepNeeds{reason: NeedNoFootage}
	}
	usable, err := s.jobCompute.Catalog(ctx, request)
	if err != nil {
		return nil, err
	}
	if len(usable) == 0 {
		return nil, &stepNeeds{reason: NeedNoFootage}
	}
	for _, id := range usable {
		if _, ok := result.Cards[id]; !ok {
			return nil, failed("the catalog names card %q it was not given", id)
		}
	}
	result.Catalog = usable
	spans := catalogSpans(result)
	var values int64
	for _, video := range videos {
		for _, window := range video.Windows {
			if overlaps(spans[video.AssetID], float64(window.StartMs)/1000, float64(window.EndMs)/1000) {
				values += int64(video.Dimensions)
			}
		}
	}
	if values > videocutcompute.MaxComputeArrayBytes/4 {
		return nil, failed("目录中的素材窗口向量超过计算服务的 64 MiB 上限")
	}
	return result, nil
}

// --- song ---

func (s *Store) songKey(ctx context.Context, run *jobRun) (string, error) {
	if s.jobMusic == nil || run.settings.SongID == "" {
		return "", &stepNeeds{reason: NeedMusic, err: domain.ErrSongUnusable}
	}
	key, err := s.jobMusic.ExcerptKey(ctx, run.owner, run.settings.SongID, run.settings.SongStart)
	return key, songStepError(err)
}

func songStepError(err error) error {
	switch {
	case errors.Is(err, domain.ErrSongAnalyzing):
		return &stepWait{reason: WaitAnalysis, delay: waitPoll}
	case errors.Is(err, domain.ErrSongUnusable):
		return &stepNeeds{reason: NeedMusic, err: err}
	}
	return err
}

func (s *Store) excerptOf(ctx context.Context, run *jobRun) (any, error) {
	excerpt, err := s.jobMusic.Excerpt(ctx, run.owner, run.settings.SongID, run.settings.SongStart)
	if err != nil {
		return nil, songStepError(err)
	}
	if excerpt.Frames <= 0 {
		return nil, &stepNeeds{reason: NeedMusic, err: domain.ErrSongUnusable}
	}
	if excerpt.Key != run.keys["song"] {
		return nil, errors.New("the song analysis changed while selecting the excerpt")
	}
	return excerpt, nil
}

// --- planning ---

func (s *Store) planInput(run *jobRun, step string) domain.PlanInput {
	cards := make([]videocutcompute.CatalogCard, 0, len(run.catalog.Catalog))
	for _, id := range run.catalog.Catalog {
		cards = append(cards, run.catalog.Cards[id])
	}
	return domain.PlanInput{JobID: run.id, Plan: run.plan, Cards: cards, Music: run.excerpt.Music, Settings: run.settings, Picked: run.picked,
		StepKey: run.keys[planStep(step, run.plan)], PickKey: run.keys["pick"]}
}

// accepted is a 方案's plan for a step taken for this catalog, not voided.
func (s *Store) accepted(ctx context.Context, jobID string, plan int, step, catalogKey string) (json.RawMessage, bool, error) {
	answer, err := s.db.AcceptedPlan(ctx, jobID, plan, step, catalogKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return json.RawMessage(answer), err == nil, err
}

func (s *Store) accept(ctx context.Context, jobID string, plan int, step, catalogKey string, answer json.RawMessage) error {
	return s.db.AcceptPlan(ctx, jobID, plan, step, catalogKey, string(answer), s.now())
}

func (s *Store) plan(ctx context.Context, run *jobRun, step string, ask func(context.Context, domain.PlanInput) (json.RawMessage, error)) (any, error) {
	if answer, ok, err := s.accepted(ctx, run.id, run.plan, step, run.keys["catalog"]); err != nil || ok {
		return answer, err
	}
	answer, err := ask(ctx, s.planInput(run, step))
	switch {
	case errors.Is(err, domain.ErrPlanPending):
		return nil, &stepWait{reason: WaitVNAS, delay: waitPoll, detail: planDetail(err)}
	case errors.Is(err, domain.ErrPlanUnknown):
		return nil, &stepNeeds{reason: NeedVNASUnknown, err: err}
	case errors.Is(err, domain.ErrPlanInvalid):
		return nil, &stepNeeds{reason: NeedPlanInvalid, err: err}
	case errors.Is(err, domain.ErrPlanFailed):
		// Planned as many rounds as a job is, none finished: the job fails
		// and says why, and the member plans it again with one click (Replan).
		return nil, &stepFailed{err: err, reason: FailPlanning}
	case err != nil:
		return nil, err
	}
	if !json.Valid(answer) {
		return nil, &stepNeeds{reason: NeedPlanInvalid, err: errors.New("the plan is not JSON")}
	}
	return answer, s.accept(ctx, run.id, run.plan, step, run.keys["catalog"], answer)
}

func (s *Store) pick(ctx context.Context, run *jobRun) (any, error) {
	return s.plan(ctx, run, StepOutline, s.planner.Pick)
}

func (s *Store) shots(ctx context.Context, run *jobRun) (any, error) {
	return s.plan(ctx, run, StepShots, s.planner.Shots)
}

// pins are an edit's slots with their beats (#596), nil for a job that
// plans.
func (run *jobRun) pins() []videocutcompute.FillPin {
	if run.settings.Edit == nil {
		return nil
	}
	return run.settings.Edit.Pins
}

// assetOf is the job's video named in a card.
func (run *jobRun) assetOf(video string) (analysedVideo, bool) {
	for _, v := range run.analysis.Videos {
		if v.AssetID == video {
			return v, true
		}
	}
	return analysedVideo{}, false
}

// latestTakenAt is when the latest of the job's videos was taken, the
// film's own capture time (docs/specs/montage-center.md).
func (run *jobRun) latestTakenAt() *time.Time {
	var latest *time.Time
	for _, v := range run.analysis.Videos {
		if v.TakenAt != nil && (latest == nil || v.TakenAt.After(*latest)) {
			latest = v.TakenAt
		}
	}
	return latest
}

// filmName is the film's name in the photo library.
func filmName(project string, number int) string {
	suffix := fmt.Sprintf(" v%d.mp4", number)
	name := strings.Map(func(ch rune) rune {
		if ch == '/' || ch == '\\' || unicode.IsControl(ch) {
			return '_'
		}
		return ch
	}, project)
	name = strings.TrimLeftFunc(name, func(ch rune) bool { return ch == '.' || unicode.IsSpace(ch) })
	name = strings.TrimSpace(name)
	for len(name)+len(suffix) > 255 {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	if name == "" {
		name = "剪辑"
	}
	return name + suffix
}
