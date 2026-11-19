package montage

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
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
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
	fillVersion    = "fill/v9"
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
	assets        []jobAsset
	keys          map[string]string
	// The steps' results.
	analysis analysisResult
	catalog  catalogResult
	excerpt  Excerpt
	picked   json.RawMessage
	planned  json.RawMessage
	timeline timelineResult
	render   renderResult
}

type jobAsset struct{ assetID, objectID string }

// step is one step: its key from the run so far, and doing it.
type step struct {
	name string
	key  func(ctx context.Context, run *jobRun) (string, error)
	do   func(ctx context.Context, run *jobRun) (any, error)
	// load takes a stored result back into the run.
	load func(run *jobRun, result []byte) error
	// reusable reports whether a stored result can still be used, beyond
	// its key: the render's file must still be there.
	reusable func(run *jobRun) bool
}

func (s *Store) steps() []step {
	return []step{
		{name: "analysis", key: s.analysisKey, do: s.analyse,
			load: func(run *jobRun, b []byte) error { return json.Unmarshal(b, &run.analysis) }},
		{name: "catalog", key: func(_ context.Context, run *jobRun) (string, error) {
			return digest(run.keys["analysis"], catalogVersion), nil
		}, do: s.makeCatalog, load: func(run *jobRun, b []byte) error { return json.Unmarshal(b, &run.catalog) }},
		{name: "song", key: s.songKey, do: s.excerptOf,
			load: func(run *jobRun, b []byte) error { return json.Unmarshal(b, &run.excerpt) }},
		{name: "pick", key: func(_ context.Context, run *jobRun) (string, error) {
			return digest(run.keys["catalog"], run.settings.Ask, fmt.Sprint(run.settings.Chronological), run.keys["song"], promptVersion), nil
		}, do: s.pick, load: func(run *jobRun, b []byte) error { run.picked = b; return nil }},
		{name: "shots", key: func(_ context.Context, run *jobRun) (string, error) {
			return shotsKey(run.keys["pick"], run.picked), nil
		}, do: s.shots, load: func(run *jobRun, b []byte) error { run.planned = b; return nil }},
		{name: "fill", key: func(_ context.Context, run *jobRun) (string, error) {
			return digest(run.keys["shots"], digest(string(run.planned)), fillVersion), nil
		}, do: s.fill, load: func(run *jobRun, b []byte) error { return json.Unmarshal(b, &run.timeline) }},
		{name: "render", key: func(_ context.Context, run *jobRun) (string, error) {
			return digest(digestJSON(run.timeline.Clips), renderVersion, run.excerpt.Key), nil
		}, do: s.renderFilm, load: func(run *jobRun, b []byte) error { return json.Unmarshal(b, &run.render) },
			reusable: s.filmKept},
		{name: "save", key: func(_ context.Context, run *jobRun) (string, error) {
			return run.id + "|" + run.keys["render"], nil
		}, do: s.save, load: func(*jobRun, []byte) error { return nil }},
	}
}

// runSteps runs job from its first step not done.
func (s *Store) runSteps(ctx context.Context, job claimed) error {
	run := &jobRun{claimed: job, keys: map[string]string{}}
	// An interrupted save may already have committed in the photo library.
	// Replay its exact version key before inspecting changeable upstream inputs.
	var saving bool
	if err := s.db.QueryRowContext(ctx, "SELECT save_started FROM jobs WHERE id = ?", job.id).Scan(&saving); err != nil {
		return err
	}
	if saving {
		var rendered string
		var key string
		err := s.db.QueryRowContext(ctx, `SELECT p.grant_id, r.result_json, w.input_key FROM jobs j
			JOIN projects p ON p.id=j.project_id JOIN steps r ON r.job_id=j.id AND r.name='render'
			JOIN steps w ON w.job_id=j.id AND w.name='save' WHERE j.id=?`, job.id).Scan(&run.grantID, &rendered, &key)
		if err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(rendered), &run.render); err != nil {
			return err
		}
		run.replayingSave = true
		run.keys["save"] = key
		_, err = s.save(ctx, run)
		return err
	}
	if err := s.loadRun(ctx, run); err != nil {
		return err
	}
	for _, st := range s.steps() {
		if s.cancelRequested(ctx, job.id) {
			return errCancelled
		}
		// Every step starts by looking whether the videos are still there.
		if err := s.checkAssets(ctx, job); err != nil {
			return err
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
		if err := s.startStep(ctx, job.id, st.name, key); err != nil {
			return err
		}
		result, err := st.do(ctx, run)
		if errors.Is(err, ErrWorkerRefused) {
			return failed("计算服务拒绝处理此素材或规划: %v", err)
		}
		if err != nil {
			return err
		}
		if st.name == "save" {
			return nil // save records its step with the version and the job's end
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
	rows, err := s.db.QueryContext(ctx, "SELECT asset_id, object_id FROM job_assets WHERE job_id = ? ORDER BY asset_id", run.id)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var asset jobAsset
		if err := rows.Scan(&asset.assetID, &asset.objectID); err != nil {
			return err
		}
		run.assets = append(run.assets, asset)
	}
	if len(run.assets) != len(assets) {
		return fmt.Errorf("job %s lists its videos twice", run.id)
	}
	return rows.Err()
}

// jobGrant is the job's project grant and the job's videos. A project whose
// grant is gone has all its videos missing.
func (s *Store) jobGrant(ctx context.Context, jobID string) (string, []string, error) {
	var grantID sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT p.grant_id FROM jobs j JOIN projects p ON p.id = j.project_id WHERE j.id = ?`, jobID).
		Scan(&grantID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	if err != nil {
		return "", nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT asset_id FROM job_assets WHERE job_id = ? ORDER BY asset_id", jobID)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	var assets []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", nil, err
		}
		assets = append(assets, id)
	}
	if err := rows.Err(); err != nil {
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
// starts over, and so does everything after it.
func (s *Store) startStep(ctx context.Context, jobID, name, key string) error {
	now := sqlitedb.FormatTime(s.now())
	return s.withTx(ctx, func(tx *sql.Tx) error {
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
			for _, affected := range StepNames[slices.Index(StepNames, name):] {
				if affected != name {
					if _, err := tx.ExecContext(ctx, "DELETE FROM steps WHERE job_id = ? AND name = ?", jobID, affected); err != nil {
						return err
					}
				}
				if stored != "" && (affected == "pick" || affected == "shots") {
					if _, err := tx.ExecContext(ctx, "UPDATE plans SET voided_at = ? WHERE job_id = ? AND step = ? AND voided_at IS NULL", now, jobID, affected); err != nil {
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
	return s.withTx(ctx, func(tx *sql.Tx) error {
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
	objects := map[string]string{}
	rows, err := s.db.QueryContext(ctx, "SELECT asset_id, object_id FROM job_assets WHERE job_id = ?", job.id)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var asset, object string
		if err := rows.Scan(&asset, &object); err != nil {
			return err
		}
		objects[asset] = object
	}
	if err := rows.Err(); err != nil {
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
	var missing []string
	trashed := false
	for _, id := range ids {
		asset, ok := status[id]
		switch {
		case !ok || asset.Status == photos.GrantMissing:
			missing = append(missing, id)
		case asset.Status == photos.GrantTrashed:
			trashed = true
		case asset.ObjectID != objects[id]:
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return &stepNeeds{reason: NeedMissing, missing: missing}
	}
	if trashed {
		return &stepWait{reason: WaitTrashed, delay: lifecycleCheck}
	}
	return nil
}

// --- analysis ---

// analysedVideo is one video's analysis as the job uses it.
type analysedVideo struct {
	AssetID              string                     `json:"assetId"`
	ObjectID             string                     `json:"objectId"`
	Dimensions           int                        `json:"dimensions"`
	CardsIdentity        string                     `json:"cardsIdentity"`
	QualityIdentity      string                     `json:"qualityIdentity"`
	DescriptionsIdentity string                     `json:"descriptionsIdentity"`
	Identity             string                     `json:"identity"`
	Duration             float64                    `json:"duration"`
	Width                int                        `json:"width"`
	Height               int                        `json:"height"`
	TakenAt              *time.Time                 `json:"takenAt,omitempty"`
	Windows              []photos.Window            `json:"windows"`
	Cards                []Card                     `json:"cards"`
	Measures             map[string]json.RawMessage `json:"measures"`
}

type analysisResult struct {
	Videos []analysedVideo `json:"videos"`
}

// analysisKey reads the photo library's analysis of every video: the key
// is their objects and what the analysis made of them. A video not
// analysed yet makes the job wait; one whose analysis failed needs the
// member.
func (s *Store) analysisKey(ctx context.Context, run *jobRun) (string, error) {
	ids := make([]string, len(run.assets))
	for i, asset := range run.assets {
		ids[i] = asset.assetID
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
	var pending, broken, long []string
	unavailable := false
	for _, asset := range run.assets {
		looked, ok := about[asset.assetID]
		if ok && looked.Status == photos.GrantTrashed {
			return "", &stepWait{reason: WaitTrashed, delay: lifecycleCheck}
		}
		if !ok || looked.Status != photos.GrantAvailable || looked.ObjectID != asset.objectID {
			return "", &stepNeeds{reason: NeedMissing, missing: []string{asset.assetID}}
		}
		states := []photos.AnalysisState{looked.Windows, looked.Cards, looked.Quality, looked.Descriptions}
		switch {
		case looked.Windows == photos.AnalysisFailed && photos.VideoTooLong(looked.Duration):
			long = append(long, asset.assetID)
		case slices.Contains(states, photos.AnalysisUnavailable):
			unavailable = true
		case slices.Contains(states, photos.AnalysisFailed):
			broken = append(broken, asset.assetID)
		case slices.ContainsFunc(states, func(state photos.AnalysisState) bool { return state != photos.AnalysisDone }):
			pending = append(pending, asset.assetID)
		}
	}
	// What the member has to take out comes first, every such video at
	// once; then what only an administrator can change.
	if len(long) > 0 {
		return "", &stepNeeds{reason: NeedTooLong, missing: long, err: errors.New("视频超过窗口分析时长上限，请移除此素材")}
	}
	if len(broken) > 0 {
		return "", &stepNeeds{reason: NeedAnalysis, missing: broken}
	}
	if unavailable {
		return "", &stepNeeds{reason: NeedModel, err: errors.New("素材分析所需的本地模型不可用")}
	}
	if len(pending) > 0 {
		// Asking again changes nothing for the videos already queued.
		if err := s.analyzeAssets(ctx, run.grantID, pending, false); err != nil {
			return "", jobGrantError(err, pending)
		}
		return "", &stepWait{reason: WaitAnalysis, delay: waitPoll}
	}
	result := analysisResult{}
	for n, asset := range run.assets {
		analysis, _, err := s.grants.Analysis(ctx, run.grantID, asset.assetID)
		if err != nil {
			return "", jobGrantError(err, []string{asset.assetID})
		}
		if analysis.ObjectID != asset.objectID {
			return "", &stepNeeds{reason: NeedMissing, missing: []string{asset.assetID}}
		}
		video := analysedVideo{AssetID: asset.assetID, ObjectID: asset.objectID, Identity: analysis.Identity, Dimensions: analysis.Dimensions,
			CardsIdentity: analysis.CardsIdentity, QualityIdentity: analysis.QualityIdentity, DescriptionsIdentity: analysis.DescriptionsIdentity,
			Duration: about[asset.assetID].Duration, Width: about[asset.assetID].Width, Height: about[asset.assetID].Height,
			TakenAt: about[asset.assetID].TakenAt, Windows: analysis.List, Measures: map[string]json.RawMessage{}}
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
					"asset", asset.assetID, "card", card.Index)
				continue
			}
			id := fmt.Sprintf("v%d-%d", n+1, card.Index)
			frames := make([]string, len(seen))
			for i := range seen {
				frames[i] = fmt.Sprintf("%s/%d", id, i+1)
			}
			video.Cards = append(video.Cards, Card{ID: id, Video: asset.assetID, Start: float64(card.StartMs) / 1000,
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
// frames by the beat's number (ai/anas_montage/domain/fill.py). A
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
	Cards   map[string]Card `json:"cards"`
	Catalog []string        `json:"catalog"`
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
	request := CatalogRequest{Cards: []Card{}, Measures: map[string]json.RawMessage{}}
	result := catalogResult{Cards: map[string]Card{}}
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
	if values > MaxComputeArrayBytes/4 {
		return nil, failed("目录中的素材窗口向量超过计算服务的 64 MiB 上限")
	}
	return result, nil
}

// --- song ---

func (s *Store) songKey(ctx context.Context, run *jobRun) (string, error) {
	if s.jobMusic == nil || run.settings.SongID == "" {
		return "", &stepNeeds{reason: NeedMusic, err: ErrSongUnusable}
	}
	key, err := s.jobMusic.ExcerptKey(ctx, run.owner, run.settings.SongID, run.settings.SongStart)
	return key, songStepError(err)
}

func songStepError(err error) error {
	switch {
	case errors.Is(err, ErrSongAnalyzing):
		return &stepWait{reason: WaitAnalysis, delay: waitPoll}
	case errors.Is(err, ErrSongUnusable):
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
		return nil, &stepNeeds{reason: NeedMusic, err: ErrSongUnusable}
	}
	if excerpt.Key != run.keys["song"] {
		return nil, errors.New("the song analysis changed while selecting the excerpt")
	}
	return excerpt, nil
}

// --- planning ---

func (s *Store) planInput(run *jobRun, step string) PlanInput {
	cards := make([]Card, 0, len(run.catalog.Catalog))
	for _, id := range run.catalog.Catalog {
		cards = append(cards, run.catalog.Cards[id])
	}
	return PlanInput{JobID: run.id, Cards: cards, Music: run.excerpt.Music, Settings: run.settings, Picked: run.picked,
		StepKey: run.keys[step], PickKey: run.keys["pick"]}
}

// accepted is the job's plan for a step taken for this catalog, not voided.
func (s *Store) accepted(ctx context.Context, jobID, step, catalogKey string) (json.RawMessage, bool, error) {
	var answer string
	err := s.db.QueryRowContext(ctx, `SELECT answer_json FROM plans WHERE job_id = ? AND step = ? AND catalog_key = ? AND voided_at IS NULL
		ORDER BY accepted_at DESC LIMIT 1`, jobID, step, catalogKey).Scan(&answer)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return json.RawMessage(answer), err == nil, err
}

func (s *Store) accept(ctx context.Context, jobID, step, catalogKey string, answer json.RawMessage) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO plans(job_id, step, catalog_key, answer_json, accepted_at) VALUES(?, ?, ?, ?, ?)",
		jobID, step, catalogKey, string(answer), sqlitedb.FormatTime(s.now()))
	return err
}

func (s *Store) plan(ctx context.Context, run *jobRun, step string, ask func(context.Context, PlanInput) (json.RawMessage, error)) (any, error) {
	if answer, ok, err := s.accepted(ctx, run.id, step, run.keys["catalog"]); err != nil || ok {
		return answer, err
	}
	answer, err := ask(ctx, s.planInput(run, step))
	switch {
	case errors.Is(err, ErrPlanPending):
		return nil, &stepWait{reason: WaitVNAS, delay: waitPoll, detail: planDetail(err)}
	case errors.Is(err, ErrPlanUnknown):
		return nil, &stepNeeds{reason: NeedVNASUnknown, err: err}
	case errors.Is(err, ErrPlanInvalid):
		return nil, &stepNeeds{reason: NeedPlanInvalid, err: err}
	case err != nil:
		return nil, err
	}
	if !json.Valid(answer) {
		return nil, &stepNeeds{reason: NeedPlanInvalid, err: errors.New("the plan is not JSON")}
	}
	return answer, s.accept(ctx, run.id, step, run.keys["catalog"], answer)
}

func (s *Store) pick(ctx context.Context, run *jobRun) (any, error) {
	return s.plan(ctx, run, "pick", s.planner.Pick)
}

func (s *Store) shots(ctx context.Context, run *jobRun) (any, error) {
	return s.plan(ctx, run, "shots", s.planner.Shots)
}

// voidPlans voids the job's plans: the catalog changed.
func voidPlans(ctx context.Context, tx *sql.Tx, jobID string, at time.Time) error {
	_, err := tx.ExecContext(ctx, "UPDATE plans SET voided_at = ? WHERE job_id = ? AND voided_at IS NULL", sqlitedb.FormatTime(at), jobID)
	return err
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
