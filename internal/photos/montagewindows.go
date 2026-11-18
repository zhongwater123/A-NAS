package photos

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"io/fs"
	"maps"
	"math"
	"os"
	"slices"
	"time"

	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// The montage center's analysis of a video (ADR 0038 decision 4) runs here,
// in the AI runner, only for the videos a grant asked for. Videos still take
// no part in the library's own AI (inLocalAI): their results never reach
// search, clusters, similar photos or the assistant. The first analysis is
// the window vectors, montage-windows/v1: a window every 2 seconds, the frame
// at its middle rendered as the thumbnail pipeline renders (longer side 512,
// JPEG quality 82, upright) and encoded by the image model.

const (
	windowsDerivation = "montage-windows/v1"
	// windowLength and windowChunk: windows of 2 seconds, 30 a chunk. Each
	// chunk is stored, the lease renewed and the gate asked before the next.
	windowLength = 2000
	windowChunk  = 30
	// windowFlat is the standard deviation of a frame's luma below which it
	// is black or one colour, and takes no part in cards.
	windowFlat = 8.0
	// windowsIdentityFormat is what the analysis is besides the model and
	// the size of its vectors: any change makes another analysis.
	windowsIdentityFormat = "%s|win=2s1f,frame=edge512,jpeg82,rot=display,dim=%d,l2"
	// montageRetention is how long an analysis no grant refers to stays.
	montageRetention = 7 * 24 * time.Hour
	// leaseRenewal is how often a long call renews its job's lease.
	leaseRenewal = 30 * time.Second
	// windowFrameEdge is the width of the frame FFmpeg hands over, as for a
	// video's poster; the thumbnail pipeline then renders it to 512.
	windowFrameEdge = posterEdge
	// jobErrorTooLong marks a video with more windows than MaxVideoWindows.
	jobErrorTooLong = "too_long"
)

// MaxVideoWindows is the most windows an analysis has: 6 hours of video.
// A longer video's analysis fails as too long, rather than make what the
// montage center cannot take. MaxWindowDimensions bounds the size of a
// window's vector in an answer.
const (
	MaxVideoWindows     = 10800
	MaxWindowDimensions = 4096
)

// FrameTaker takes several frames of a video in one request; the FFmpeg
// Worker's client provides it (ffmpegworker.Editing).
type FrameTaker interface {
	Frames(ctx context.Context, file *os.File, request ffmpegworker.FramesRequest, each func([]byte) error) (ffmpegworker.FramesResult, error)
}

// Window is one window of a video's analysis.
type Window struct {
	Index   int  `json:"idx"`
	StartMs int  `json:"startMs"`
	EndMs   int  `json:"endMs"`
	Flat    bool `json:"flat"`
}

// AnalysisState is where one analysis of a video stands.
type AnalysisState string

const (
	AnalysisNone    AnalysisState = ""
	AnalysisQueued  AnalysisState = "queued"
	AnalysisWorking AnalysisState = "working"
	AnalysisDone    AnalysisState = "done"
	AnalysisFailed  AnalysisState = "failed"
)

// windowsIdentity is the identity of window vectors of dimensions made with
// model.
func windowsIdentity(model string, dimensions int) string {
	return fmt.Sprintf(windowsIdentityFormat, model, dimensions)
}

// videoWindows lays out a video's windows, as the prototype does: one every
// windowLength from the start, the last up to the end, each with the
// instant of its middle frame.
func videoWindows(durationMs int) (windows []Window, middles []int) {
	for start := 0; start < durationMs; start += windowLength {
		end := min(start+windowLength, durationMs)
		windows = append(windows, Window{Index: len(windows), StartMs: start, EndMs: end})
		middles = append(middles, start+(end-start)/2)
	}
	return windows, middles
}

// VideoTooLong reports whether the analysis would exceed the window limit.
func VideoTooLong(duration float64) bool { return windowCount(duration) > MaxVideoWindows }

func windowCount(duration float64) int {
	durationMs := int(math.Round(duration * 1000))
	if durationMs <= 0 {
		return 0
	}
	return (durationMs + windowLength - 1) / windowLength
}

// analysisProgress is where the montage analysis of a video stands; for
// done windows, also their identity and how many there are.
type analysisProgress struct {
	windows         AnalysisState
	windowsIdentity string
	windowCount     int
}

// progressOf reads, for many videos in one go, where their montage
// analyses stand; videos maps each content object to its duration in
// seconds. A video's windows are done when no job is queued for them and
// every window has a vector of the current identity. Before the runner has
// asked the Worker for its model, windows of one identity count; the
// runner's first round queues them again if that model is another
// (requeueStaleWindows).
func (s *Service) progressOf(ctx context.Context, videos map[string]float64) (map[string]analysisProgress, error) {
	progress := make(map[string]analysisProgress, len(videos))
	if len(videos) == 0 {
		return progress, nil
	}
	objects := slices.Sorted(maps.Keys(videos))
	in := sqlitedb.Placeholders(len(objects))
	args := []any{windowsDerivation}
	for _, object := range objects {
		args = append(args, object)
	}
	jobs := map[string]string{}
	err := s.eachRow(ctx, "SELECT object_id, state FROM jobs WHERE derivation = ? AND object_id IN ("+in+")", args, func(rows *sql.Rows) error {
		var object, state string
		err := rows.Scan(&object, &state)
		jobs[object] = state
		return err
	})
	if err != nil {
		return nil, err
	}
	counts := map[string]map[string]int{}
	err = s.eachRow(ctx, "SELECT object_id, identity, COUNT(*) FROM video_windows WHERE object_id IN ("+in+") GROUP BY object_id, identity", args[1:], func(rows *sql.Rows) error {
		var object, identity string
		var count int
		if err := rows.Scan(&object, &identity, &count); err != nil {
			return err
		}
		if counts[object] == nil {
			counts[object] = map[string]int{}
		}
		counts[object][identity] = count
		return nil
	})
	if err != nil {
		return nil, err
	}
	current := s.ai.knownWindows()
	for _, object := range objects {
		var p analysisProgress
		switch state, queued := jobs[object]; {
		case !queued:
			identity := current
			if identity == "" && len(counts[object]) == 1 {
				for only := range counts[object] {
					identity = only
				}
			}
			if want := windowCount(videos[object]); want > 0 && identity != "" && counts[object][identity] >= want {
				p = analysisProgress{windows: AnalysisDone, windowsIdentity: identity, windowCount: want}
			}
		case state == "failed":
			p.windows = AnalysisFailed
		case state == "running":
			p.windows = AnalysisWorking
		default:
			p.windows = AnalysisQueued
		}
		progress[object] = p
	}
	return progress, nil
}

// grantedVideos are the content objects of assetIDs that the grant holds and
// its owner sees as available videos, with their durations, and where their
// analyses stand.
func (s *Service) grantedVideos(ctx context.Context, owner Principal, grantID string, assetIDs []string) (map[string]float64, map[string]analysisProgress, error) {
	looked, progress, err := s.lookupGrant(ctx, owner, grantID, assetIDs)
	if err != nil {
		return nil, nil, err
	}
	videos := map[string]float64{}
	for _, asset := range looked {
		if asset.Status == GrantAvailable {
			videos[asset.ObjectID] = asset.Duration
		}
	}
	return videos, progress, nil
}

// AnalyzeGrant queues the montage analysis of the grant's assetIDs that
// lack it; an object two grants ask for is analysed once, and windows done
// with the current model are not done again. With retry, a failed analysis
// starts over.
func (s *Service) AnalyzeGrant(ctx context.Context, owner Principal, grantID string, assetIDs []string, retry bool) error {
	videos, progress, err := s.grantedVideos(ctx, owner, grantID, assetIDs)
	if err != nil || len(videos) == 0 {
		return err
	}
	reader, _ := s.ai.descriptionReader()
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		now := formatTime(s.now())
		for _, object := range slices.Sorted(maps.Keys(videos)) {
			if _, err := tx.ExecContext(ctx, `INSERT INTO montage_objects(object_id, requested_at) VALUES(?, ?)
				ON CONFLICT(object_id) DO UPDATE SET unreferenced_since = NULL`, object, now); err != nil {
				return err
			}
			if retry {
				// Retain successful descriptions when a member retries the failed cards.
				if _, err := tx.ExecContext(ctx, "DELETE FROM video_card_descriptions WHERE object_id=? AND result IS NULL", object); err != nil {
					return err
				}
				// Whichever step of the analysis failed starts over.
				if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state = 'pending', attempts = 0, lease_until = NULL, not_before = ?, error_class = ''
					WHERE object_id = ? AND derivation LIKE 'montage-%' AND state = 'failed'`, now, object); err != nil {
					return err
				}
			}
			if progress[object].windows == AnalysisDone {
				continue
			}
			// A job already queued, or failed, stays as it is.
			if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(object_id, derivation, state, not_before, created_at)
				VALUES(?, ?, 'pending', ?, ?) ON CONFLICT DO NOTHING`, object, windowsDerivation, now, now); err != nil {
				return err
			}
			if err := s.queueMissingDescriptions(ctx, tx, reader, false, "?", object); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		s.ai.wakeUp()
	}
	return err
}

// GrantAnalysis is the montage analysis of one granted video so far.
//
// Once its windows are done, the answer carries them in a sealed memory
// file: WindowCount × Dimensions little-endian float32, the vectors in
// window order, then a header for each window, three little-endian uint32:
// its start and end in milliseconds and its flags, bit 0 set for a flat
// frame. ReadWindows reads it, filling List; the JSON answer stays small
// however long the video.
type GrantAnalysis struct {
	CardsIdentity string `json:"cardsIdentity,omitempty"`
	QualityIdentity string `json:"qualityIdentity,omitempty"`
	ObjectID    string        `json:"objectId"`
	Windows     AnalysisState `json:"windowsState"`
	Identity    string        `json:"windowsIdentity,omitempty"`
	WindowCount int           `json:"windowCount,omitempty"`
	Dimensions  int           `json:"dimensions,omitempty"`
	List        []Window      `json:"-"`
	// Cards and Quality say where the cards and their quality stand;
	// CardList holds the cards cut so far, with the quality measured.
	Cards    AnalysisState  `json:"cardsState"`
	Quality  AnalysisState  `json:"qualityState"`
	CardList []CardAnalysis `json:"cards,omitempty"`
	// Described is where the card descriptions stand; Descriptions holds
	// them once done, a card that could not be described with its error.
	Described            AnalysisState     `json:"descriptionsState,omitempty"`
	DescriptionsIdentity string            `json:"descriptionsIdentity,omitempty"`
	Descriptions         []CardDescription `json:"descriptions,omitempty"`
}

// windowHeader is a window's header in an analysis answer.
type windowHeader struct {
	StartMs, EndMs, Flags uint32
}

const windowFlatFlag = 1

// AnalysisOfGrant returns the analysis of a granted video and, once its
// windows are done, a sealed memory file with them (GrantAnalysis).
func (s *Service) AnalysisOfGrant(ctx context.Context, owner Principal, grantID, assetID string) (GrantAnalysis, *os.File, error) {
	looked, progress, err := s.lookupGrant(ctx, owner, grantID, []string{assetID})
	if err != nil {
		return GrantAnalysis{}, nil, err
	}
	if len(looked) != 1 || looked[0].Status != GrantAvailable {
		return GrantAnalysis{}, nil, ErrNotFound
	}
	done := progress[looked[0].ObjectID]
	analysis := GrantAnalysis{ObjectID: looked[0].ObjectID, Windows: done.windows}
	analysis.CardsIdentity, analysis.QualityIdentity = cardsIdentity, qualityIdentity
	analysis.Described = looked[0].Descriptions
	analysis.DescriptionsIdentity = looked[0].descriptionIdentity
	if analysis.Described == AnalysisDone {
		if analysis.Descriptions, err = s.cardDescriptions(ctx, analysis.ObjectID, analysis.DescriptionsIdentity); err != nil {
			return GrantAnalysis{}, nil, err
		}
	}
	if analysis.Cards, err = s.cardsState(ctx, analysis.ObjectID); err != nil {
		return GrantAnalysis{}, nil, err
	}
	if analysis.Quality, err = s.qualityState(ctx, analysis.ObjectID); err != nil {
		return GrantAnalysis{}, nil, err
	}
	if analysis.CardList, err = s.cardsOf(ctx, analysis.ObjectID); err != nil {
		return GrantAnalysis{}, nil, err
	}
	if done.windows != AnalysisDone {
		return analysis, nil, nil
	}
	analysis.Identity, analysis.WindowCount = done.windowsIdentity, done.windowCount
	file, err := sealedFile("anas-window-vectors", func(w io.Writer) error {
		headers := make([]windowHeader, 0, analysis.WindowCount)
		err := s.eachRow(ctx, "SELECT start_ms, end_ms, flat, vector FROM video_windows WHERE object_id = ? AND identity = ? AND idx < ? ORDER BY idx",
			[]any{analysis.ObjectID, analysis.Identity, analysis.WindowCount}, func(rows *sql.Rows) error {
				var header windowHeader
				var flat bool
				var vector []byte
				if err := rows.Scan(&header.StartMs, &header.EndMs, &flat, &vector); err != nil {
					return err
				}
				if analysis.Dimensions == 0 {
					analysis.Dimensions = len(vector) / 4
				}
				if len(vector) != 4*analysis.Dimensions {
					return fmt.Errorf("window vector of %d bytes among those of %d dimensions", len(vector), analysis.Dimensions)
				}
				if flat {
					header.Flags |= windowFlatFlag
				}
				headers = append(headers, header)
				_, err := w.Write(vector)
				return err
			})
		if err != nil {
			return err
		}
		if len(headers) != analysis.WindowCount {
			return errWindowsChanged
		}
		return binary.Write(w, binary.LittleEndian, headers)
	})
	if errors.Is(err, errWindowsChanged) {
		// A new model started over meanwhile.
		return GrantAnalysis{ObjectID: analysis.ObjectID, Windows: AnalysisQueued}, nil, nil
	}
	if err != nil {
		return GrantAnalysis{}, nil, err
	}
	return analysis, file, nil
}

var errWindowsChanged = errors.New("the windows changed while being read")

// ReadWindows reads the windows of a done analysis from the memory file of
// its answer (GrantAnalysis) into analysis.List, and returns their vectors:
// WindowCount × Dimensions little-endian float32 in window order.
func ReadWindows(analysis *GrantAnalysis, file io.Reader) ([]byte, error) {
	count, dimensions := analysis.WindowCount, analysis.Dimensions
	if count < 1 || count > MaxVideoWindows || dimensions < 1 || dimensions > MaxWindowDimensions {
		return nil, fmt.Errorf("an analysis of %d windows of %d dimensions", count, dimensions)
	}
	vectors := make([]byte, count*dimensions*4)
	if _, err := io.ReadFull(file, vectors); err != nil {
		return nil, fmt.Errorf("window vectors: %w", err)
	}
	headers := make([]windowHeader, count)
	if err := binary.Read(file, binary.LittleEndian, headers); err != nil {
		return nil, fmt.Errorf("window headers: %w", err)
	}
	if n, _ := io.CopyN(io.Discard, file, 1); n != 0 {
		return nil, errors.New("window analysis: more than its windows")
	}
	analysis.List = make([]Window, count)
	for i, header := range headers {
		if header.EndMs <= header.StartMs {
			return nil, fmt.Errorf("window %d ends at %d ms before it starts at %d ms", i, header.EndMs, header.StartMs)
		}
		analysis.List[i] = Window{Index: i, StartMs: int(header.StartMs), EndMs: int(header.EndMs), Flat: header.Flags&windowFlatFlag != 0}
	}
	return vectors, nil
}

// montageWorkWaiting reports whether a montage analysis is due: the
// descriptions only while the Worker has a text reader.
func (s *Service) montageWorkWaiting(ctx context.Context, reading bool) (bool, error) {
	now := formatTime(s.now())
	var due bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM jobs WHERE (derivation IN (?, ?, ?) OR (? AND derivation = ?))
		AND ((state = 'pending' AND not_before <= ?) OR (state = 'running' AND lease_until <= ?)))`,
		windowsDerivation, cardsDerivation, qualityDerivation, reading, describeDerivation, now, now).Scan(&due)
	return due, err
}

// dropUnreferencedWindows removes the queued window analyses of videos no
// grant refers to any more, as after a project was deleted: they would hold
// up the photos' own work for nothing. What they did goes with
// PruneMontageAnalysis; a grant asking again queues them again.
func (s *Service) dropUnreferencedWindows(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM jobs WHERE derivation = ? AND state = 'pending'
		AND object_id NOT IN (`+referencedObjects+`)`, windowsDerivation)
	return err
}

// renewLease keeps a long call's job claimed.
func (s *Service) renewLease(ctx context.Context, job claimedJob) error {
	_, err := s.db.ExecContext(ctx, "UPDATE jobs SET lease_until = ? WHERE object_id = ? AND derivation = ? AND state = 'running'",
		formatTime(s.now().Add(jobLease)), job.objectID, job.derivation)
	return err
}

// keepLease renews the job's lease every leaseRenewal until the returned
// stop is called.
func (s *Service) keepLease(ctx context.Context, job claimedJob) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(leaseRenewal)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = s.renewLease(ctx, job)
			}
		}
	}()
	return func() { cancel(); <-done }
}

// lumaDeviation is the standard deviation of a JPEG's luma, from 0 to 255.
func lumaDeviation(data []byte) (float64, error) {
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	bounds := img.Bounds()
	var sum, squares float64
	n := float64(bounds.Dx() * bounds.Dy())
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			var luma float64
			switch pixel := img.(type) {
			case *image.YCbCr:
				luma = float64(pixel.Y[pixel.YOffset(x, y)])
			case *image.Gray:
				luma = float64(pixel.GrayAt(x, y).Y)
			default:
				r, g, b, _ := img.At(x, y).RGBA()
				luma = (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)) / 257
			}
			sum += luma
			squares += luma * luma
		}
	}
	if n == 0 {
		return 0, errors.New("empty frame")
	}
	mean := sum / n
	return math.Sqrt(max(0, squares/n-mean*mean)), nil
}

// RunningGate is a Gate that also tells work already running whether it may
// go on: it stops at once while someone uses the photo library or memory is
// short, but not while the device's other work waits on the processor or
// the disk, which only holds the next item back. Unlike Open, the answer
// does not depend on whether the processor is busy at the same time.
type RunningGate interface {
	Gate
	Continue(ctx context.Context, need uint64) (open bool, reason string)
}

// continuing is how a running montage chunk asks gate whether to go on.
func continuing(gate Gate) func(context.Context, uint64) (bool, string) {
	if running, ok := gate.(RunningGate); ok {
		return running.Continue
	}
	return func(ctx context.Context, need uint64) (bool, string) {
		open, reason := gate.Open(ctx, need)
		return open || !stopsRunningWork(reason), reason
	}
}

// stopsRunningWork reports whether a reason to stop is someone using the
// library or short memory.
func stopsRunningWork(reason string) bool {
	return reason == "foreground" || reason == "memory_low" || reason == "memory_for_models" || reason == "memory_pressure"
}

// aiFailure is an error of the AI Worker's encoding a window's frame, apart
// from those of taking and rendering the frame.
type aiFailure struct{ err error }

func (f aiFailure) Error() string { return f.err.Error() }
func (f aiFailure) Unwrap() error { return f.err }

// errInvalidWindowVector is a vector of the wrong size or with values that
// are not finite numbers.
var errInvalidWindowVector = errors.New("invalid window vector")

// embedFrame encodes a window's rendered frame.
func embedFrame(ctx context.Context, embedder Embedder, data []byte, dimensions int) ([]float32, error) {
	input, err := memoryFile(data)
	if err != nil {
		return nil, aiFailure{err}
	}
	vector, err := embedder.EmbedImage(ctx, input)
	_ = input.Close()
	switch {
	case err != nil:
		return nil, aiFailure{err}
	case !validVector(vector, dimensions):
		return nil, errInvalidWindowVector
	}
	return vector, nil
}

// montageWindows runs one chunk of a due window analysis. It reports whether
// it did any work, and whether the round ends with it: the gate hung the
// chunk up, the service is stopping or the Worker is away. A job with more
// to do goes back to the head of the queue without spending its attempt, so
// the next round, after asking the gate, does the next chunk.
func (r *aiRunner) montageWindows(ctx context.Context, embedder Embedder, gate Gate, info EmbedderInfo) (worked, ended bool, err error) {
	lib := r.lib
	taker, ok := lib.media.(FrameTaker)
	if !ok {
		return false, false, nil
	}
	// Most rounds have none: a read says so before any write.
	if due, err := lib.montageWorkWaiting(ctx, false); err != nil || !due {
		return false, false, err
	}
	if err := lib.dropUnreferencedWindows(ctx); err != nil {
		return false, false, err
	}
	job, err := lib.claimJob(ctx, windowsDerivation)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	durationMs := int(math.Round(job.duration * 1000))
	windows, middles := videoWindows(durationMs)
	switch {
	case !job.video || len(windows) == 0:
		return true, false, lib.failJob(ctx, job, jobErrorUndecodable)
	case len(windows) > MaxVideoWindows:
		return true, false, lib.failJob(ctx, job, jobErrorTooLong)
	}
	identity := windowsIdentity(info.Model, info.Dimensions)
	var stored int
	if err := lib.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM video_windows WHERE object_id = ? AND identity = ?", job.objectID, identity).Scan(&stored); err != nil {
		return false, false, errors.Join(err, lib.returnJob(context.WithoutCancel(ctx), job))
	}
	if stored >= len(windows) {
		return true, false, lib.completeWindows(ctx, job, identity, len(windows))
	}
	original, err := lib.openObject(job.objectID)
	if errors.Is(err, fs.ErrNotExist) {
		return true, false, lib.failJob(ctx, job, jobErrorMissingOriginal)
	}
	if err != nil {
		return false, false, errors.Join(err, lib.releaseJob(ctx, job))
	}
	defer func() { _ = original.Close() }()
	r.progress.set(AIWorking, "", info.Model)
	chunk := windows[stored:min(stored+windowChunk, len(windows))]
	stopLease := lib.keepLease(ctx, job)
	defer stopLease()
	// Someone using the library, or short memory, hangs the chunk up at
	// once; the device's other work waits for the chunk to end.
	working, cancel := context.WithCancel(ctx)
	defer cancel()
	stopWatching := watchGate(working, continuing(gate), info.MemoryBytes, cancel)
	times := make([]float64, len(chunk))
	for i, window := range chunk {
		// A frame within the last 100 ms may not be there; FFmpeg steps back
		// from where none is.
		times[i] = float64(min(middles[window.Index], max(durationMs-100, 0))) / 1000
	}
	request := ffmpegworker.FramesRequest{Times: times, Format: ffmpegworker.FormatJPEG, Width: windowFrameEdge, Quality: 2}
	var done []Window
	var vectors [][]float32
	_, err = taker.Frames(ffmpegworker.WithPriority(working, ffmpegworker.Background), original, request, func(frame []byte) error {
		window := chunk[len(done)]
		data, _, _, err := renderUpright(working, bytes.NewReader(frame), 1, thumbnailEdge, thumbnailQuality)
		if err != nil {
			return err
		}
		deviation, err := lumaDeviation(data)
		if err != nil {
			return err
		}
		vector, err := embedFrame(working, embedder, data, info.Dimensions)
		if err != nil {
			return err
		}
		window.Flat = deviation < windowFlat
		done, vectors = append(done, window), append(vectors, vector)
		return nil
	})
	paused := stopWatching()
	cancel()
	// What came before a stop is kept: the next round goes on from there.
	kept, storeErr := lib.storeWindows(context.WithoutCancel(ctx), job, identity, done, vectors)
	if storeErr != nil {
		return false, false, errors.Join(storeErr, lib.returnJob(context.WithoutCancel(ctx), job))
	}
	if !kept {
		// Purged, or pruned, while the chunk ran.
		return true, false, nil
	}
	var failure aiFailure
	switch {
	case ctx.Err() != nil:
		// Stopping: the job keeps its place and its attempt.
		return false, true, lib.returnJob(context.WithoutCancel(ctx), job)
	case paused != "":
		// Hung up: the round ends here, as a paused detection's does, and
		// the job keeps its place and its attempt.
		r.progress.set(AIPaused, paused, "")
		err := lib.returnJob(ctx, job)
		detector, _ := embedder.(Detector)
		r.releaseModels(ctx, detector, paused)
		return false, true, err
	case err == nil:
	case errors.Is(err, ErrAIUnavailable):
		r.progress.set(AIUnavailable, "", "")
		return false, true, lib.releaseJob(ctx, job)
	case errors.Is(err, ErrAIRejected):
		return true, false, lib.failJob(ctx, job, jobErrorUndecodable)
	case errors.Is(err, errInvalidWindowVector):
		return true, false, lib.failJob(ctx, job, jobErrorInvalidResult)
	case errors.As(err, &failure):
		// The Worker's own trouble, as with a photo's vector: tried again.
		return true, false, lib.failJob(ctx, job, jobErrorIO)
	default:
		// Taking and rendering the frames fail as a thumbnail's do.
		if class := decodeFailure(err); class != jobWaiting {
			return true, false, lib.failJob(ctx, job, class)
		}
		return false, false, lib.releaseJob(ctx, job)
	}
	if stored+len(done) >= len(windows) {
		return true, false, lib.completeWindows(ctx, job, identity, len(windows))
	}
	return true, false, lib.returnJob(ctx, job)
}

// storeWindows stores the windows a chunk made, replacing those of another
// identity, and reports whether the job was still there: a chunk whose
// video was purged, or whose analysis was pruned, while it ran keeps
// nothing.
func (s *Service) storeWindows(ctx context.Context, job claimedJob, identity string, windows []Window, vectors [][]float32) (bool, error) {
	var kept bool
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM jobs WHERE object_id = ? AND derivation = ?)",
			job.objectID, job.derivation).Scan(&kept); err != nil || !kept {
			return err
		}
		now := formatTime(s.now())
		for i, window := range windows {
			if _, err := tx.ExecContext(ctx, `INSERT INTO video_windows(object_id, idx, start_ms, end_ms, flat, vector, identity, created_at)
				VALUES(?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(object_id, idx) DO UPDATE SET start_ms = excluded.start_ms,
				end_ms = excluded.end_ms, flat = excluded.flat, vector = excluded.vector, identity = excluded.identity,
				created_at = excluded.created_at`,
				job.objectID, window.Index, window.StartMs, window.EndMs, window.Flat, encodeVector(vectors[i]), identity, now); err != nil {
				return err
			}
		}
		return nil
	})
	return kept, err
}

// completeWindows retires the job once every window has its vector, and
// drops windows beyond the video's end left by an earlier identity. A job
// that went meanwhile leaves the windows alone.
func (s *Service) completeWindows(ctx context.Context, job claimedJob, identity string, count int) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "DELETE FROM jobs WHERE object_id = ? AND derivation = ?", job.objectID, job.derivation)
		if err != nil {
			return err
		}
		if gone, err := result.RowsAffected(); err != nil || gone == 0 {
			return err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM video_windows WHERE object_id = ? AND (idx >= ? OR identity <> ?)", job.objectID, count, identity); err != nil { return err }
		// The cards follow the windows: new windows, new cards.
		now := formatTime(s.now())
		_, err = tx.ExecContext(ctx, `INSERT INTO jobs(object_id, derivation, state, not_before, created_at) VALUES(?, ?, 'pending', ?, ?)
			ON CONFLICT(object_id, derivation) DO UPDATE SET state = 'pending', attempts = 0, lease_until = NULL, not_before = excluded.not_before, error_class = ''`,
			job.objectID, cardsDerivation, now, now)
		return err
	})
}

// referencedObjects is the SQL of the objects a grant not revoked refers to.
const referencedObjects = `SELECT a.object_id FROM montage_grant_assets ga
	JOIN montage_grants g ON g.id = ga.grant_id AND g.revoked_at IS NULL
	JOIN assets a ON a.id = ga.asset_id`

// requeueStaleWindows queues again the window analysis of the objects a
// grant still refers to whose windows have another identity. Objects only
// grants since revoked asked for are not computed again; a grant asking
// for one again queues it (AnalyzeGrant).
func (s *Service) requeueStaleWindows(ctx context.Context, identity string) error {
	now := formatTime(s.now())
	_, err := s.db.ExecContext(ctx, `INSERT INTO jobs(object_id, derivation, state, not_before, created_at)
		SELECT DISTINCT w.object_id, ?, 'pending', ?, ? FROM video_windows w
		WHERE w.identity <> ? AND w.object_id IN (`+referencedObjects+`)
		ON CONFLICT DO NOTHING`, windowsDerivation, now, now, identity)
	return err
}

// PruneMontageAnalysis marks the analyses no grant refers to any more, and
// removes those that stayed so for montageRetention. The window points go
// from the index with them, through the outbox.
func (s *Service) PruneMontageAnalysis(ctx context.Context) (int, error) {
	var removed int64
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		now := s.now()
		if _, err := tx.ExecContext(ctx, `UPDATE montage_objects SET unreferenced_since = NULL
			WHERE unreferenced_since IS NOT NULL AND object_id IN (`+referencedObjects+`)`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE montage_objects SET unreferenced_since = ?
			WHERE unreferenced_since IS NULL AND object_id NOT IN (`+referencedObjects+`)`, formatTime(now)); err != nil {
			return err
		}
		cutoff := sqlitedb.FormatTime(now.Add(-montageRetention))
		for _, statement := range []string{
			"DELETE FROM video_card_quality WHERE object_id IN (SELECT object_id FROM montage_objects WHERE unreferenced_since <= ?)",
			"DELETE FROM video_cards WHERE object_id IN (SELECT object_id FROM montage_objects WHERE unreferenced_since <= ?)",
			"DELETE FROM video_windows WHERE object_id IN (SELECT object_id FROM montage_objects WHERE unreferenced_since <= ?)",
			"DELETE FROM video_card_descriptions WHERE object_id IN (SELECT object_id FROM montage_objects WHERE unreferenced_since <= ?)",
			"DELETE FROM jobs WHERE derivation LIKE 'montage-%' AND object_id IN (SELECT object_id FROM montage_objects WHERE unreferenced_since <= ?)",
		} {
			if _, err := tx.ExecContext(ctx, statement, cutoff); err != nil {
				return err
			}
		}
		result, err := tx.ExecContext(ctx, "DELETE FROM montage_objects WHERE unreferenced_since <= ?", cutoff)
		if err != nil {
			return err
		}
		removed, err = result.RowsAffected()
		return err
	})
	return int(removed), err
}
