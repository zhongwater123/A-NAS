package montage

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/zhongwater123/A-NAS/internal/atomicfile"
	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

const (
	// maxNeeds bounds the clear spans one fill asks for.
	maxNeeds = 2000
	// maxClips is the most shots a film has (assemble's limit).
	maxClips = 100
	// The compute Worker bounds the sum of its input arrays (worker.MAX_ARRAY_BYTES).
	MaxComputeArrayBytes = 64 << 20
	// filmBytesPerFrame is room enough for a frame of the segments and
	// the film both (about 8 Mbit/s each, with headroom).
	filmBytesPerFrame = 80 << 10
	// durationTolerance is how far the film may be from the timeline.
	durationTolerance = 0.1
	filmFile          = "film.mp4"
	segmentsFile      = "segments.ts"
)

// --- fill ---

// timelineResult is the filled timeline: the shots as the fill gave them
// and the clear spans it asked for.
type timelineResult struct {
	Clips []json.RawMessage     `json:"clips"`
	Known map[string][2]float64 `json:"known"`
}

// fill fills the music's slots. It is reentrant: each time the compute
// Worker needs the clear span around a keyframe, the span is found from
// grey frames the FFmpeg Worker takes, kept, and the fill asked again.
func (s *Store) fill(ctx context.Context, run *jobRun) (any, error) {
	known := map[string][2]float64{}
	var saved sql.NullString
	if err := s.db.QueryRowContext(ctx, "SELECT result_json FROM steps WHERE job_id = ? AND name = ?", run.id, planStep("fill", run.plan)).Scan(&saved); err != nil {
		return nil, err
	}
	if saved.Valid {
		var progress timelineResult
		if err := json.Unmarshal([]byte(saved.String), &progress); err == nil && progress.Known != nil {
			known = progress.Known
		}
	}
	rows, vectors, dimensions, err := s.fillRows(ctx, run)
	if err != nil {
		return nil, err
	}
	request := FillRequest{FPS: FilmRate, Rows: rows, Cards: map[string]Card{}, Catalog: run.catalog.Catalog,
		Music: run.excerpt.Music, Planned: run.planned, Picked: run.picked, Known: known}
	for _, id := range run.catalog.Catalog {
		request.Cards[id] = run.catalog.Cards[id]
	}
	for range maxNeeds {
		answer, err := s.jobCompute.Fill(ctx, request, vectors, dimensions)
		if err != nil {
			return nil, err
		}
		if answer.Need == nil {
			if err := validateTimeline(run, answer); err != nil {
				return nil, err
			}
			return timelineResult{Clips: answer.Clips, Known: known}, nil
		}
		if _, seen := known[answer.Need.Name]; seen {
			return nil, failed("the fill asked again for %s", answer.Need.Name)
		}
		span, err := s.clearSpan(ctx, run, *answer.Need)
		if err != nil {
			return nil, err
		}
		known[answer.Need.Name] = span
		if err := s.saveProgress(ctx, run.id, planStep("fill", run.plan), timelineResult{Known: known}); err != nil {
			return nil, err
		}
	}
	return nil, failed("the fill asked for more than %d spans", maxNeeds)
}

// fillRows are the windows beneath the catalog's cards, with their vectors.
func (s *Store) fillRows(ctx context.Context, run *jobRun) ([]FillRow, []float32, int, error) {
	spans := catalogSpans(run.catalog)
	rows := []FillRow{}
	var vectors []float32
	dimensions := 0
	for _, video := range run.analysis.Videos {
		within := spans[video.AssetID]
		if len(within) == 0 {
			continue
		}
		analysis, raw, err := s.grants.Analysis(ctx, run.grantID, video.AssetID)
		if err != nil {
			return nil, nil, 0, jobGrantError(err, []string{video.AssetID})
		}
		if analysis.ObjectID != video.ObjectID || analysis.Identity != video.Identity || analysis.Dimensions != video.Dimensions {
			return nil, nil, 0, errors.New("the photo library's analysis changed while the job ran")
		}
		if dimensions != 0 && dimensions != analysis.Dimensions {
			return nil, nil, 0, failed("window vectors have incompatible dimensions")
		}
		dimensions = analysis.Dimensions
		if dimensions <= 0 || dimensions > photos.MaxWindowDimensions || len(raw) != len(analysis.List)*4*dimensions {
			return nil, nil, 0, failed("%d bytes of vectors for %d windows of dimension %d", len(raw), len(analysis.List), dimensions)
		}
		for i, window := range analysis.List {
			start, end := float64(window.StartMs)/1000, float64(window.EndMs)/1000
			if !overlaps(within, start, end) {
				continue
			}
			if len(vectors)+dimensions > MaxComputeArrayBytes/4 {
				return nil, nil, 0, failed("素材窗口向量超过计算服务的 64 MiB 上限")
			}
			rows = append(rows, FillRow{Video: video.AssetID, Start: start, End: end})
			for d := range dimensions {
				at := (i*dimensions + d) * 4
				vectors = append(vectors, math.Float32frombits(binary.LittleEndian.Uint32(raw[at:])))
			}
		}
	}
	return rows, vectors, dimensions, nil
}

func catalogSpans(catalog catalogResult) map[string][][2]float64 {
	spans := map[string][][2]float64{}
	for _, id := range catalog.Catalog {
		card := catalog.Cards[id]
		spans[card.Video] = append(spans[card.Video], [2]float64{card.Start, card.End})
	}
	return spans
}

func overlaps(spans [][2]float64, start, end float64) bool {
	return slices.ContainsFunc(spans, func(span [2]float64) bool { return start < span[1] && end > span[0] })
}

// clearSpan finds the clear span the fill needs, from grey frames of the
// keyframe and every half second around it. The FFmpeg Worker's slot is
// let go before the compute Worker is asked.
func (s *Store) clearSpan(ctx context.Context, run *jobRun, need FillNeed) ([2]float64, error) {
	if _, ok := run.assetOf(need.Video); !ok {
		return [2]float64{}, failed("the fill needs a video not in the job: %s", need.Video)
	}
	grab := func(request ffmpegworker.FramesRequest) ([]byte, int, error) {
		file, err := s.openVideo(ctx, run, need.Video)
		if err != nil {
			return nil, 0, err
		}
		defer func() { _ = file.Close() }()
		request.Format, request.Width, request.Height, request.Turned = ffmpegworker.FormatGray, FrameWidth, FrameHeight, need.Turned
		var frames []byte
		count := 0
		_, err = s.editing.Frames(ctx, file, request, func(frame []byte) error {
			if len(frame) != FrameWidth*FrameHeight {
				return fmt.Errorf("a grey frame of %d bytes", len(frame))
			}
			frames, count = append(frames, frame...), count+1
			return nil
		})
		return frames, count, err
	}
	key, count, err := grab(ffmpegworker.FramesRequest{Times: []float64{need.Key}})
	if err != nil {
		return [2]float64{}, err
	}
	if count != 1 {
		return [2]float64{}, fmt.Errorf("%d frames at the keyframe", count)
	}
	const half = 0.5 / 2 // picture.STEP / 2
	var after, before []byte
	var afterCount, beforeCount int
	if need.Hi-need.Key > half {
		if after, afterCount, err = grab(ffmpegworker.FramesRequest{Start: need.Key, End: need.Hi, Rate: FrameRate}); err != nil {
			return [2]float64{}, err
		}
	}
	if need.Key-need.Lo > half {
		if before, beforeCount, err = grab(ffmpegworker.FramesRequest{Start: need.Lo, End: need.Key, Rate: FrameRate}); err != nil {
			return [2]float64{}, err
		}
	}
	return s.jobCompute.Clear(ctx, ClearRequest{Key: need.Key, Lo: need.Lo, Hi: need.Hi}, key, after, before, afterCount, beforeCount)
}

// openVideo opens a job's video through the grant, still the content the
// job started with.
func (s *Store) openVideo(ctx context.Context, run *jobRun, assetID string) (*os.File, error) {
	file, object, err := s.grants.Open(ctx, run.grantID, assetID)
	if err != nil {
		return nil, jobGrantError(err, []string{assetID})
	}
	for _, asset := range run.assets {
		if asset.assetID == assetID && asset.objectID != object {
			_ = file.Close()
			return nil, &stepNeeds{reason: NeedMissing, missing: []string{assetID}}
		}
	}
	return file, nil
}

// clipFrames is how many frames of the film a shot takes.
func clipFrames(clip Clip) int { return int(math.Round(clip.Share * FilmRate)) }

func finite(values ...float64) bool {
	for _, v := range values {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}

// validateTimeline checks the filled timeline before anything is rendered
// (docs/architecture/montage-center.md, 校验): a timeline that fails is the
// job's failure, never a film.
func validateTimeline(run *jobRun, answer FillAnswer) error {
	if len(answer.Short) > 0 {
		return failed("%d slots were left empty", len(answer.Short))
	}
	if len(answer.Clips) == 0 || len(answer.Clips) > maxClips {
		return failed("the timeline has %d shots", len(answer.Clips))
	}
	total := 0
	var slots [][2]int
	var slot [2]int
	for i, raw := range answer.Clips {
		var clip Clip
		if err := json.Unmarshal(raw, &clip); err != nil {
			return failed("shot %d: %v", i+1, err)
		}
		video, ok := run.assetOf(clip.Video)
		if !ok {
			return failed("shot %d uses a video outside the project", i+1)
		}
		card, ok := run.catalog.Cards[clip.Card]
		if !ok || card.Video != clip.Video || !slices.Contains(run.catalog.Catalog, clip.Card) {
			return failed("shot %d uses a card outside the catalog", i+1)
		}
		if !finite(clip.Start, clip.End, clip.Share, clip.Slow, clip.Slot[0], clip.Slot[1]) || clip.Start < 0 || clip.Start >= clip.End {
			return failed("shot %d runs from %v to %v", i+1, clip.Start, clip.End)
		}
		if video.Duration > 0 && clip.End > video.Duration+0.05 {
			return failed("shot %d ends at %.3f, after its video's %.3f seconds", i+1, clip.End, video.Duration)
		}
		if clip.Slow != 0 && (clip.Slow < 1 || clip.Slow > 2.5) {
			return failed("shot %d is slowed %v times", i+1, clip.Slow)
		}
		frames := clipFrames(clip)
		if frames <= 0 || clip.Slot[0] >= clip.Slot[1] {
			return failed("shot %d is empty", i+1)
		}
		// FFmpeg consumes frames / slow from Start. The Worker rounds Slow
		// to three decimal places, so allow that rounding in the span.
		slow := max(1, clip.Slow)
		if clip.Start < card.Start-0.001 || clip.End > card.End+0.001 ||
			float64(frames)/FilmRate > (clip.End-clip.Start)*(slow+0.0005)+0.000001 {
			return failed("shot %d reads beyond its source span", i+1)
		}
		thisSlot := [2]int{filmFrame(clip.Slot[0]), filmFrame(clip.Slot[1])}
		if thisSlot != slot || len(slots) == 0 {
			if thisSlot[0] != total || thisSlot[0] != slot[1] || thisSlot[1] <= thisSlot[0] {
				return failed("slot %v does not follow frame %d", clip.Slot, total)
			}
			slots = append(slots, thisSlot)
			slot = thisSlot
		}
		total += frames
		if total > slot[1] {
			return failed("shot %d overfills its slot", i+1)
		}
	}
	if total != slot[1] {
		return failed("the last slot ends at frame %d, filled to %d", slot[1], total)
	}
	if err := checkPlannedSlots(run.planned, slots); err != nil {
		return err
	}
	if total != run.excerpt.Frames {
		return failed("the shots take %d frames, the excerpt %d", total, run.excerpt.Frames)
	}
	return nil
}

// The Python fill rounds beat positions to the nearest even frame.
func filmFrame(seconds float64) int { return int(math.RoundToEven(seconds * FilmRate)) }

// Within a picture the Worker may reorder whole slots to follow shooting
// order. Each picture must still use exactly its planned slot lengths.
func checkPlannedSlots(plan json.RawMessage, filled [][2]int) error {
	var layout struct{ Slots [][][][2]float64 }
	if err := json.Unmarshal(plan, &layout); err != nil {
		return failed("invalid planned slots: %v", err)
	}
	at := 0
	for _, part := range layout.Slots {
		for _, picture := range part {
			if len(picture) == 0 || len(picture) > len(filled)-at {
				return failed("the picture has unfilled slots")
			}
			lengths := map[int]int{}
			for _, slot := range picture {
				lengths[filmFrame(slot[1])-filmFrame(slot[0])]++
			}
			for _, slot := range filled[at : at+len(picture)] {
				length := slot[1] - slot[0]
				if lengths[length] == 0 {
					return failed("unexpected %d-frame slot in picture", length)
				}
				lengths[length]--
			}
			at += len(picture)
		}
	}
	if at != len(filled) {
		return failed("the timeline has %d unplanned slots", len(filled)-at)
	}
	return nil
}

// --- render ---

type renderResult struct {
	Film    string `json:"film"`
	Frames  int    `json:"frames"`
	Created string `json:"created"`
}

func (s *Store) filmKept(run *jobRun) bool {
	if run.render.Film == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(s.jobDir(run.id), run.render.Film))
	return err == nil
}

// filmFileOf is the plan-th 方案's film in the job's directory.
func filmFileOf(plan int) string {
	if plan <= 1 {
		return filmFile
	}
	return fmt.Sprintf("film-%d.mp4", plan)
}

func noSpace(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT)
}

// renderFilm renders each shot, one after another into tmp/<job-id>/,
// assembles the film with the song and checks it.
func (s *Store) renderFilm(ctx context.Context, run *jobRun) (any, error) {
	clips := make([]Clip, len(run.timeline.Clips))
	for i, raw := range run.timeline.Clips {
		if err := json.Unmarshal(raw, &clips[i]); err != nil {
			return nil, failed("shot %d: %v", i+1, err)
		}
	}
	dir := s.jobDir(run.id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	total := 0
	for _, clip := range clips {
		total += clipFrames(clip)
	}
	if ok, err := s.admits(dir, int64(total)*filmBytesPerFrame); err != nil {
		return nil, err
	} else if !ok {
		return nil, &stepNeeds{reason: NeedStorageFull}
	}
	segments, err := os.OpenFile(filepath.Join(dir, segmentsFile), os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	defer func() { _ = segments.Close(); _ = os.Remove(segments.Name()) }()
	offset := 0
	for i, clip := range clips {
		if err := s.renderClip(ctx, run, segments, clip, i == len(clips)-1, offset); err != nil {
			return nil, storage(err)
		}
		offset += clipFrames(clip)
	}
	if err := segments.Sync(); err != nil {
		return nil, storage(err)
	}
	if _, err := segments.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	song, err := s.jobMusic.Audio(ctx, run.owner, run.settings.SongID)
	if errors.Is(err, ErrSongUnusable) {
		return nil, &stepNeeds{reason: NeedMusic, err: err}
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = song.Close() }()
	created := s.now()
	if latest := run.latestTakenAt(); latest != nil {
		created = *latest
	}
	result := renderResult{Film: filmFileOf(run.plan), Frames: total, Created: created.UTC().Format(time.RFC3339)}
	film, err := s.editing.Assemble(ctx, segments, song, ffmpegworker.AssembleRequest{SongStart: run.excerpt.Start, Frames: total, Created: result.Created})
	if err != nil {
		return nil, err
	}
	defer func() { _ = film.Close() }()
	path := filepath.Join(dir, result.Film)
	if err := writeFile(path, film); err != nil {
		return nil, storage(err)
	}
	if err := s.checkFilm(ctx, path, total); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	return result, nil
}

func storage(err error) error {
	if noSpace(err) {
		return &stepNeeds{reason: NeedStorageFull, err: err}
	}
	return err
}

// renderClip appends one shot to segments.
func (s *Store) renderClip(ctx context.Context, run *jobRun, segments *os.File, clip Clip, last bool, offset int) error {
	video, _ := run.assetOf(clip.Video)
	file, err := s.openVideo(ctx, run, clip.Video)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	frames := clipFrames(clip)
	request := ffmpegworker.SegmentRequest{Start: clip.Start, Frames: frames, Offset: offset,
		Blurred: video.Width > 0 && video.Height > 0 && float64(video.Width)/float64(video.Height) < 16.0/9.0-0.01}
	if clip.Slow > 1 {
		request.Slow = clip.Slow
	}
	if last {
		request.FadeFrames = min(endingFade, frames/2)
	}
	piece, err := s.editing.Segment(ctx, file, request)
	if err != nil {
		return err
	}
	defer func() { _ = piece.Close() }()
	_, err = io.Copy(segments, piece)
	return err
}

// writeFile writes content to path through a file beside it, on disk
// before it takes the name.
func writeFile(path string, content io.Reader) error {
	partial := path + ".part"
	out, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, content); err != nil {
		_ = out.Close()
		_ = os.Remove(partial)
		return err
	}
	if err := errors.Join(out.Sync(), out.Close()); err != nil {
		_ = os.Remove(partial)
		return err
	}
	if err := os.Rename(partial, path); err != nil {
		return err
	}
	return atomicfile.SyncDir(filepath.Dir(path))
}

// checkFilm checks the film against the timeline: its length within 0.1
// seconds, 1920 × 1080 at 30 frames a second, with sound.
func (s *Store) checkFilm(ctx context.Context, path string, frames int) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	probe, err := s.editing.Probe(ctx, file)
	if err != nil {
		return err
	}
	want := float64(frames) / FilmRate
	if !finite(probe.Format.Duration) || math.Abs(probe.Format.Duration-want) > durationTolerance {
		return failed("the film lasts %.3f seconds, the timeline %.3f", probe.Format.Duration, want)
	}
	var picture, sound bool
	for _, stream := range probe.Streams {
		switch stream.Type {
		case "video":
			if stream.Width != FilmWidth || stream.Height != FilmHeight || !finite(stream.FrameRate) || math.Abs(stream.FrameRate-FilmRate) > 0.01 {
				return failed("the film is %d×%d at %v frames a second", stream.Width, stream.Height, stream.FrameRate)
			}
			picture = true
		case "audio":
			sound = true
		}
	}
	if !picture || !sound {
		return failed("the film has picture %v and sound %v", picture, sound)
	}
	return nil
}

// --- save ---

// saveRefusal gives settleJob both the outcome and permission to release
// the save fence in the same transaction.
type saveRefusal struct{ error }

func (r *saveRefusal) Unwrap() error { return r.error }

// save saves a 方案's film into the owner's library, once per version key,
// and records the version. The last 方案's ends the job; after an earlier
// one the job goes on with the next. A cancelled job saves nothing.
func (s *Store) save(ctx context.Context, run *jobRun) (any, error) {
	step, last := planStep("save", run.plan), run.plan >= s.plans
	versionKey := run.keys[step]
	var number int
	var name string
	if err := s.db.QueryRowContext(ctx, `SELECT IFNULL((SELECT number FROM versions WHERE version_key = ?),
		(SELECT IFNULL(MAX(number), 0) + 1 FROM versions WHERE project_id = p.id)), p.name
		FROM projects p WHERE p.id = ?`, versionKey, run.projectID).Scan(&number, &name); err != nil {
		return nil, err
	}
	film, err := os.Open(filepath.Join(s.jobDir(run.id), run.render.Film))
	if err != nil {
		return nil, err
	}
	defer func() { _ = film.Close() }()
	// Once this update wins, cancellation cannot claim the job until the
	// outcome of this version key is known, also across a restart.
	started, err := s.db.ExecContext(ctx, "UPDATE jobs SET save_started = 1 WHERE id = ? AND cancel_requested = 0 AND state = 'running'", run.id)
	if err != nil {
		return nil, err
	}
	n, err := started.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, errCancelled
	}
	assetID, err := s.grants.SaveFilm(ctx, run.grantID, versionKey, filmName(name, number), film)
	if err != nil {
		code := photosapi.GrantErrorCode(err)
		if code == "" || code == photosapi.GrantUnavailable || run.replayingSave && code == photosapi.GrantOwnerInactive {
			return nil, fmt.Errorf("waiting for the photo library to confirm the saved film: %w", err)
		}
		if run.replayingSave && code == photosapi.GrantNotFound {
			// Revocation prevents the service from reading even the old receipt.
			return nil, &saveRefusal{failed("相册授权已撤销，无法确认上次存回的结果，请在相册中核对成片")}
		}
		switch code {
		case photosapi.GrantNoSpace:
			return nil, &saveRefusal{&stepNeeds{reason: NeedStorageFull, err: err}}
		case photosapi.GrantUnauthorized:
			return nil, &saveRefusal{&stepNeeds{reason: NeedMissing, err: err}}
		default:
			return nil, &saveRefusal{jobGrantError(err, nil)}
		}
	}
	now := sqlitedb.FormatTime(s.now())
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO versions(id, project_id, number, job_id, plan, version_key, film_asset_id, created_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (version_key) DO NOTHING`,
			newID("version"), run.projectID, number, run.id, max(run.plan, 1), versionKey, assetID, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE steps SET state = 'done', result_json = ?, finished_at = ? WHERE job_id = ? AND name = ?",
			fmt.Sprintf(`{"filmAssetId":%q}`, assetID), now, run.id, step); err != nil {
			return err
		}
		if !last {
			// The next 方案 goes on: the fence is released, the job runs on.
			_, err := tx.ExecContext(ctx, `UPDATE jobs SET save_started = 0, attempts = 0, error_class = '', error = '', updated_at = ? WHERE id = ?`, now, run.id)
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE jobs SET state = 'done', save_started = 0, missing_json = NULL, attempts = 0, error_class = '', error = '', wait_reason = '',
			lease_until = NULL, finished_at = ?, updated_at = ? WHERE id = ?`, now, now, run.id)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.logger.InfoContext(ctx, "montage film saved", "job", run.id, "project", run.projectID, "version", number, "plan", run.plan)
	if last {
		s.removeJobFiles(run.id)
	}
	return nil, nil
}
