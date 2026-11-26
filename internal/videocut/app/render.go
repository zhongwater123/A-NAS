package app

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/zhongwater123/A-NAS/internal/atomicfile"
	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
	"github.com/zhongwater123/A-NAS/internal/videocut/db"
	"github.com/zhongwater123/A-NAS/internal/videocut/domain"
	"github.com/zhongwater123/A-NAS/internal/videocutcompute"
)

const (
	// maxNeeds bounds the clear spans one fill asks for.
	maxNeeds = 2000
	// maxClips is the most shots a film has (assemble's limit).
	maxClips = 100
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
	saved, err := s.db.StepResult(ctx, run.id, planStep("fill", run.plan))
	if err != nil {
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
	request := videocutcompute.FillRequest{FPS: FilmRate, Rows: rows, Cards: map[string]videocutcompute.CatalogCard{}, Catalog: run.catalog.Catalog,
		Music: run.excerpt.Music, Planned: run.planned, Picked: run.picked, Known: known, Pins: run.pins()}
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
			return nil, domain.Failed("the fill asked again for %s", answer.Need.Name)
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
	return nil, domain.Failed("the fill asked for more than %d spans", maxNeeds)
}

// fillRows are the windows beneath the catalog's cards, with their vectors.
func (s *Store) fillRows(ctx context.Context, run *jobRun) ([]videocutcompute.FillRow, []float32, int, error) {
	spans := catalogSpans(run.catalog)
	rows := []videocutcompute.FillRow{}
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
		if run.kind == KindEdit && video.windowsChanged(analysis) {
			// The version was cut with windows the library has analysed
			// again with another model since: it is played or recut now,
			// not edited, and trying again changes nothing.
			return nil, nil, 0, domain.Failed("%s", AnalysisChangedReason)
		}
		if !video.sameWindows(analysis) {
			// Made again since the job's first step looked: that step
			// decides again (analysisKey, editSourceKey).
			return nil, nil, 0, errors.New("the photo library's analysis changed while the job ran")
		}
		if dimensions != 0 && dimensions != analysis.Dimensions {
			return nil, nil, 0, domain.Failed("window vectors have incompatible dimensions")
		}
		dimensions = analysis.Dimensions
		windows, beneath, err := windowVectors(analysis, raw, func(start, end float64) bool { return overlaps(within, start, end) })
		if err != nil {
			return nil, nil, 0, domain.Failed("%v", err)
		}
		if len(vectors)+len(beneath) > videocutcompute.MaxComputeArrayBytes/4 {
			return nil, nil, 0, domain.Failed("素材窗口向量超过计算服务的 64 MiB 上限")
		}
		for _, window := range windows {
			rows = append(rows, videocutcompute.FillRow{Video: video.AssetID, Start: float64(window.StartMs) / 1000, End: float64(window.EndMs) / 1000})
		}
		vectors = append(vectors, beneath...)
	}
	return rows, vectors, dimensions, nil
}

// windowVectors are those of an analysis's windows that keep wants, given
// each window's start and end in seconds, with their vectors one after
// another. raw is the analysis's vectors as the photo library keeps them:
// for each window in order, its dimensions as little-endian float32.
func windowVectors(analysis photos.GrantAnalysis, raw []byte, keep func(start, end float64) bool) ([]photos.Window, []float32, error) {
	dimensions := analysis.Dimensions
	if dimensions <= 0 || dimensions > photos.MaxWindowDimensions || len(raw) != len(analysis.List)*4*dimensions {
		return nil, nil, fmt.Errorf("%d bytes of vectors for %d windows of dimension %d", len(raw), len(analysis.List), dimensions)
	}
	var windows []photos.Window
	var vectors []float32
	for i, window := range analysis.List {
		if !keep(float64(window.StartMs)/1000, float64(window.EndMs)/1000) {
			continue
		}
		windows = append(windows, window)
		at := len(vectors)
		vectors = append(vectors, make([]float32, dimensions)...)
		if _, err := binary.Decode(raw[i*4*dimensions:(i+1)*4*dimensions], binary.LittleEndian, vectors[at:]); err != nil {
			return nil, nil, err
		}
	}
	return windows, vectors, nil
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
func (s *Store) clearSpan(ctx context.Context, run *jobRun, need videocutcompute.FillNeed) ([2]float64, error) {
	if _, ok := run.assetOf(need.Video); !ok {
		return [2]float64{}, domain.Failed("the fill needs a video not in the job: %s", need.Video)
	}
	grab := func(request ffmpegworker.FramesRequest) ([]byte, int, error) {
		file, err := s.openVideo(ctx, run, need.Video)
		if err != nil {
			return nil, 0, err
		}
		defer func() { _ = file.Close() }()
		request.Format, request.Width, request.Height, request.Turned = ffmpegworker.FormatGray, videocutcompute.FrameWidth, videocutcompute.FrameHeight, need.Turned
		var frames []byte
		count := 0
		_, err = s.editing.Frames(ctx, file, request, func(frame []byte) error {
			if len(frame) != videocutcompute.FrameWidth*videocutcompute.FrameHeight {
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
		if after, afterCount, err = grab(ffmpegworker.FramesRequest{Start: need.Key, End: need.Hi, Rate: videocutcompute.FrameRate}); err != nil {
			return [2]float64{}, err
		}
	}
	if need.Key-need.Lo > half {
		if before, beforeCount, err = grab(ffmpegworker.FramesRequest{Start: need.Lo, End: need.Key, Rate: videocutcompute.FrameRate}); err != nil {
			return [2]float64{}, err
		}
	}
	return s.jobCompute.Clear(ctx, videocutcompute.ClearRequest{Key: need.Key, Lo: need.Lo, Hi: need.Hi}, key, after, before, afterCount, beforeCount)
}

// openVideo opens a job's video through the grant, still the content the
// job started with.
func (s *Store) openVideo(ctx context.Context, run *jobRun, assetID string) (*os.File, error) {
	file, object, err := s.grants.Open(ctx, run.grantID, assetID)
	if err != nil {
		return nil, jobGrantError(err, []string{assetID})
	}
	want, known := "", false
	for _, asset := range run.assets {
		if asset.AssetID == assetID {
			want, known = asset.ObjectID, true
		}
	}
	if video, ok := run.assetOf(assetID); !known && ok {
		// An edit's fill may take a piece from a video no shot of the draft
		// leads: still the content the version was planned from.
		want, known = video.ObjectID, true
	}
	if known && want != object {
		_ = file.Close()
		return nil, &stepNeeds{reason: NeedMissing, missing: []string{assetID}}
	}
	return file, nil
}

// clipFrames is how many frames of the film a shot takes.
func clipFrames(clip domain.Clip) int { return int(math.Round(clip.Share * FilmRate)) }

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
func validateTimeline(run *jobRun, answer videocutcompute.FillAnswer) error {
	if len(answer.Short) > 0 {
		if pins := run.pins(); pins != nil {
			return shortOfPins(pins, answer.Short)
		}
		return domain.Failed("%d slots were left empty", len(answer.Short))
	}
	if len(answer.Clips) == 0 || len(answer.Clips) > maxClips {
		return domain.Failed("the timeline has %d shots", len(answer.Clips))
	}
	total := 0
	var slots [][2]int
	// beats are the beats of each slot's shots.
	var beats [][]string
	var slot [2]int
	for i, raw := range answer.Clips {
		var clip domain.Clip
		if err := json.Unmarshal(raw, &clip); err != nil {
			return domain.Failed("shot %d: %v", i+1, err)
		}
		video, ok := run.assetOf(clip.Video)
		if !ok {
			return domain.Failed("shot %d uses a video outside the project", i+1)
		}
		card, ok := run.catalog.Cards[clip.Card]
		if !ok || card.Video != clip.Video || !slices.Contains(run.catalog.Catalog, clip.Card) {
			return domain.Failed("shot %d uses a card outside the catalog", i+1)
		}
		if !finite(clip.Start, clip.End, clip.Share, clip.Slow, clip.Slot[0], clip.Slot[1]) || clip.Start < 0 || clip.Start >= clip.End {
			return domain.Failed("shot %d runs from %v to %v", i+1, clip.Start, clip.End)
		}
		if video.Duration > 0 && clip.End > video.Duration+0.05 {
			return domain.Failed("shot %d ends at %.3f, after its video's %.3f seconds", i+1, clip.End, video.Duration)
		}
		if clip.Slow != 0 && (clip.Slow < 1 || clip.Slow > 2.5) {
			return domain.Failed("shot %d is slowed %v times", i+1, clip.Slow)
		}
		frames := clipFrames(clip)
		if frames <= 0 || clip.Slot[0] >= clip.Slot[1] {
			return domain.Failed("shot %d is empty", i+1)
		}
		// FFmpeg consumes frames / slow from Start. The Worker rounds Slow
		// to three decimal places, so allow that rounding in the span.
		slow := max(1, clip.Slow)
		if clip.Start < card.Start-0.001 || clip.End > card.End+0.001 ||
			float64(frames)/FilmRate > (clip.End-clip.Start)*(slow+0.0005)+0.000001 {
			return domain.Failed("shot %d reads beyond its source span", i+1)
		}
		thisSlot := [2]int{filmFrame(clip.Slot[0]), filmFrame(clip.Slot[1])}
		if thisSlot != slot || len(slots) == 0 {
			if thisSlot[0] != total || thisSlot[0] != slot[1] || thisSlot[1] <= thisSlot[0] {
				return domain.Failed("slot %v does not follow frame %d", clip.Slot, total)
			}
			slots = append(slots, thisSlot)
			beats = append(beats, nil)
			slot = thisSlot
		}
		beats[len(beats)-1] = append(beats[len(beats)-1], clip.Beat)
		total += frames
		if total > slot[1] {
			return domain.Failed("shot %d overfills its slot", i+1)
		}
	}
	if total != slot[1] {
		return domain.Failed("the last slot ends at frame %d, filled to %d", slot[1], total)
	}
	if pins := run.pins(); pins != nil {
		// An edit keeps its draft's slots, in the draft's order.
		if err := checkPinnedSlots(pins, slots, beats); err != nil {
			return err
		}
	} else if err := checkPlannedSlots(run.planned, slots); err != nil {
		return err
	}
	if total != run.excerpt.Frames {
		return domain.Failed("the shots take %d frames, the excerpt %d", total, run.excerpt.Frames)
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
		return domain.Failed("invalid planned slots: %v", err)
	}
	at := 0
	for _, part := range layout.Slots {
		for _, picture := range part {
			if len(picture) == 0 || len(picture) > len(filled)-at {
				return domain.Failed("the picture has unfilled slots")
			}
			lengths := map[int]int{}
			for _, slot := range picture {
				lengths[filmFrame(slot[1])-filmFrame(slot[0])]++
			}
			for _, slot := range filled[at : at+len(picture)] {
				length := slot[1] - slot[0]
				if lengths[length] == 0 {
					return domain.Failed("unexpected %d-frame slot in picture", length)
				}
				lengths[length]--
			}
			at += len(picture)
		}
	}
	if at != len(filled) {
		return domain.Failed("the timeline has %d unplanned slots", len(filled)-at)
	}
	return nil
}

// checkPinnedSlots checks an edit's timeline fills its pins' slots, each
// in its place, and that each is cut with the beat the member chose for it:
// beats are the beats of each slot's shots. The fill passes over a pin whose
// card has no footage left around its keyframe, as when the shots beside it
// took it; a film that silently shows another beat there is not the draft,
// so the member is told which shots to change.
func checkPinnedSlots(pins []videocutcompute.FillPin, filled [][2]int, beats [][]string) error {
	if len(pins) != len(filled) {
		return domain.Failed("the timeline has %d slots, the draft %d", len(filled), len(pins))
	}
	var passed []string
	for i, pin := range pins {
		if want := [2]int{filmFrame(pin.Slot[0]), filmFrame(pin.Slot[1])}; filled[i] != want {
			return domain.Failed("slot %d is %v, the draft's %v", i+1, filled[i], want)
		}
		if !slices.Contains(beats[i], pin.Beat) {
			passed = append(passed, strconv.Itoa(i+1))
		}
	}
	if len(passed) > 0 {
		return domain.Failed("第 %s 镜选的节拍放不进它的位置：同一段录像里相邻的镜头已经用到了那段画面。换一个节拍后再存", strings.Join(passed, "、"))
	}
	return nil
}

// shortOfPins says which of an edit's shots the fill could not fill: short
// are the fill's unfilled slots, each [part, picture, start, end, frames].
func shortOfPins(pins []videocutcompute.FillPin, short []json.RawMessage) error {
	var shots []string
	for _, raw := range short {
		var slot []float64
		if err := json.Unmarshal(raw, &slot); err != nil || len(slot) < 4 {
			continue
		}
		for i, pin := range pins {
			if filmFrame(pin.Slot[0]) == filmFrame(slot[2]) {
				shots = append(shots, strconv.Itoa(i+1))
			}
		}
	}
	if len(shots) == 0 {
		return domain.Failed("%d slots were left empty", len(short))
	}
	return domain.Failed("第 %s 镜选的节拍不够长，也没有别的画面能补上。换一个节拍后再存", strings.Join(shots, "、"))
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
	clips := make([]domain.Clip, len(run.timeline.Clips))
	for i, raw := range run.timeline.Clips {
		if err := json.Unmarshal(raw, &clips[i]); err != nil {
			return nil, domain.Failed("shot %d: %v", i+1, err)
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
	segments, err := os.OpenFile(filepath.Join(dir, segmentsFile), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
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
	_ = segments.Close()
	// The worker gets a descriptor of its own that cannot write (ADR 0017).
	video, err := os.Open(segments.Name())
	if err != nil {
		return nil, err
	}
	defer func() { _ = video.Close() }()
	song, err := s.jobMusic.Audio(ctx, run.owner, run.settings.SongID)
	if errors.Is(err, domain.ErrSongUnusable) {
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
	film, err := s.editing.Assemble(ctx, video, song, ffmpegworker.AssembleRequest{SongStart: run.excerpt.Start, Frames: total, Created: result.Created})
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
func (s *Store) renderClip(ctx context.Context, run *jobRun, segments *os.File, clip domain.Clip, last bool, offset int) error {
	video, _ := run.assetOf(clip.Video)
	file, err := s.openVideo(ctx, run, clip.Video)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	frames := clipFrames(clip)
	request := ffmpegworker.SegmentRequest{Start: clip.Start, Frames: frames, Offset: offset,
		Blurred: narrow(video.Width, video.Height)}
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
		return domain.Failed("the film lasts %.3f seconds, the timeline %.3f", probe.Format.Duration, want)
	}
	var picture, sound bool
	for _, stream := range probe.Streams {
		switch stream.Type {
		case "video":
			if stream.Width != FilmWidth || stream.Height != FilmHeight || !finite(stream.FrameRate) || math.Abs(stream.FrameRate-FilmRate) > 0.01 {
				return domain.Failed("the film is %d×%d at %v frames a second", stream.Width, stream.Height, stream.FrameRate)
			}
			picture = true
		case "audio":
			sound = true
		}
	}
	if !picture || !sound {
		return domain.Failed("the film has picture %v and sound %v", picture, sound)
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
	step, last := planStep("save", run.plan), run.plan >= run.plans
	versionKey := run.keys[step]
	number, name, err := s.db.VersionNumber(ctx, versionKey, run.projectID)
	if err != nil {
		return nil, err
	}
	film, err := os.Open(filepath.Join(s.jobDir(run.id), run.render.Film))
	if err != nil {
		return nil, err
	}
	defer func() { _ = film.Close() }()
	// Once this update wins, cancellation cannot claim the job until the
	// outcome of this version key is known, also across a restart.
	started, err := s.db.StartSave(ctx, run.id)
	if err != nil {
		return nil, err
	}
	if !started {
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
			return nil, &saveRefusal{domain.Failed("相册授权已撤销，无法确认上次存回的结果，请在相册中核对成片")}
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
	at := s.now()
	// The version keeps its timeline, which editing it reads (#596). A save
	// replayed after a restart has not loaded the fill: its version has
	// its job's fill to read instead.
	var timeline any
	if len(run.timeline.Clips) > 0 {
		encoded, err := json.Marshal(timelineResult{Clips: run.timeline.Clips})
		if err != nil {
			return nil, err
		}
		timeline = string(encoded)
	}
	err = s.db.Update(ctx, func(tx *db.Tx) error {
		// An edit's version is an edit of the version it came from, at the
		// draft's revision; a generate job's was planned.
		if err := tx.AddVersion(ctx, newID("version"), run.projectID, number, run.id, max(run.plan, 1), versionKey, assetID, at, timeline); err != nil {
			return err
		}
		if err := tx.FinishStep(ctx, run.id, step, fmt.Sprintf(`{"filmAssetId":%q}`, assetID), at); err != nil {
			return err
		}
		if !last {
			// The next 方案 goes on: the fence is released, the job runs on.
			return tx.ReleaseSaveFence(ctx, run.id, at)
		}
		return tx.FinishJob(ctx, run.id, at)
	})
	if err != nil {
		return nil, err
	}
	s.logger.InfoContext(ctx, "montage film saved", "job", run.id, "project", run.projectID, "version", number, "plan", run.plan)
	if last {
		s.removeJobFiles(run.id)
		return nil, nil
	}
	// The film is in the library: its copy here does not wait for the 方案
	// still to be planned and made, however long that takes.
	_ = film.Close()
	if err := os.Remove(film.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.logger.WarnContext(ctx, "removing a saved film's copy failed", "job", run.id, "error", err)
	}
	return nil, nil
}
