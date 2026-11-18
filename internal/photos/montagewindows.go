package photos

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"image"
	"image/jpeg"
	"io/fs"
	"math"
	"os"
	"sync/atomic"
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
	// windowsIdentitySuffix is what the analysis is besides the model:
	// any change makes another analysis.
	windowsIdentitySuffix = "|win=2s1f,frame=edge512,jpeg82,rot=display,dim=768,l2"
	// montageRetention is how long an analysis no grant refers to stays.
	montageRetention = 7 * 24 * time.Hour
	// leaseRenewal is how often a long call renews its job's lease.
	leaseRenewal = 30 * time.Second
	// windowFrameEdge is the width of the frame FFmpeg hands over, as for a
	// video's poster; the thumbnail pipeline then renders it to 512.
	windowFrameEdge = posterEdge
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

// windowsIdentity is the identity of window vectors made with model.
func windowsIdentity(model string) string { return model + windowsIdentitySuffix }

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

// grantedObjects are the content objects of assetIDs that the grant holds
// and its owner sees as available videos.
func (s *Service) grantedObjects(ctx context.Context, owner Principal, grantID string, assetIDs []string) ([]string, error) {
	looked, err := s.LookupGrant(ctx, owner, grantID, assetIDs)
	if err != nil {
		return nil, err
	}
	var objects []string
	for _, asset := range looked {
		if asset.Status == GrantAvailable {
			objects = append(objects, asset.ObjectID)
		}
	}
	return objects, nil
}

// AnalyzeGrant queues the montage analysis of the grant's assetIDs that
// lack it; an object two grants ask for is analysed once. With retry, a
// failed analysis starts over.
func (s *Service) AnalyzeGrant(ctx context.Context, owner Principal, grantID string, assetIDs []string, retry bool) error {
	objects, err := s.grantedObjects(ctx, owner, grantID, assetIDs)
	if err != nil || len(objects) == 0 {
		return err
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		now := formatTime(s.now())
		for _, object := range objects {
			if _, err := tx.ExecContext(ctx, `INSERT INTO montage_objects(object_id, requested_at) VALUES(?, ?)
				ON CONFLICT(object_id) DO UPDATE SET unreferenced_since = NULL`, object, now); err != nil {
				return err
			}
			if retry {
				if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state = 'pending', attempts = 0, lease_until = NULL, not_before = ?, error_class = ''
					WHERE object_id = ? AND derivation = ? AND state = 'failed'`, now, object, windowsDerivation); err != nil {
					return err
				}
			}
			// Windows already made with any model are left to requeueStale.
			if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(object_id, derivation, state, not_before, created_at)
				SELECT ?, ?, 'pending', ?, ? WHERE NOT EXISTS (SELECT 1 FROM video_windows WHERE object_id = ?)
				ON CONFLICT DO NOTHING`, object, windowsDerivation, now, now, object); err != nil {
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

// windowsState says where the window vectors of object stand.
func (s *Service) windowsState(ctx context.Context, q queryer, object string) (AnalysisState, error) {
	var state sql.NullString
	err := q.QueryRowContext(ctx, "SELECT state FROM jobs WHERE object_id = ? AND derivation = ?", object, windowsDerivation).Scan(&state)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		var done bool
		if err := q.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM video_windows WHERE object_id = ?)", object).Scan(&done); err != nil || !done {
			return AnalysisNone, err
		}
		return AnalysisDone, nil
	case err != nil:
		return AnalysisNone, err
	case state.String == "failed":
		return AnalysisFailed, nil
	case state.String == "running":
		return AnalysisWorking, nil
	}
	return AnalysisQueued, nil
}

// GrantAnalysis is the montage analysis of one granted video so far.
type GrantAnalysis struct {
	ObjectID string        `json:"objectId"`
	Windows  AnalysisState `json:"windowsState"`
	Identity string        `json:"windowsIdentity,omitempty"`
	List     []Window      `json:"windows,omitempty"`
}

// AnalysisOfGrant returns the analysis of a granted video and, once its
// windows are done, their vectors: n × dimensions little-endian float32 in
// window order.
func (s *Service) AnalysisOfGrant(ctx context.Context, owner Principal, grantID, assetID string) (GrantAnalysis, []byte, error) {
	looked, err := s.LookupGrant(ctx, owner, grantID, []string{assetID})
	if err != nil {
		return GrantAnalysis{}, nil, err
	}
	if len(looked) != 1 || looked[0].Status != GrantAvailable {
		return GrantAnalysis{}, nil, ErrNotFound
	}
	analysis := GrantAnalysis{ObjectID: looked[0].ObjectID}
	if analysis.Windows, err = s.windowsState(ctx, s.db, analysis.ObjectID); err != nil || analysis.Windows != AnalysisDone {
		return analysis, nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT idx, start_ms, end_ms, flat, vector, identity FROM video_windows WHERE object_id = ? ORDER BY idx", analysis.ObjectID)
	if err != nil {
		return GrantAnalysis{}, nil, err
	}
	defer rows.Close()
	var vectors []byte
	for rows.Next() {
		var window Window
		var vector []byte
		if err := rows.Scan(&window.Index, &window.StartMs, &window.EndMs, &window.Flat, &vector, &analysis.Identity); err != nil {
			return GrantAnalysis{}, nil, err
		}
		analysis.List, vectors = append(analysis.List, window), append(vectors, vector...)
	}
	return analysis, vectors, rows.Err()
}

// montageWorkWaiting reports whether a montage analysis is due.
func (s *Service) montageWorkWaiting(ctx context.Context) (bool, error) {
	now := formatTime(s.now())
	var due bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM jobs WHERE derivation = ?
		AND ((state = 'pending' AND not_before <= ?) OR (state = 'running' AND lease_until <= ?)))`, windowsDerivation, now, now).Scan(&due)
	return due, err
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

// closesForeground reports whether a reason to stop is someone using the
// library or short memory, which stop a chunk at once; pressure from other
// work waits for the chunk to end.
func closesForeground(reason string) bool {
	return reason == "foreground" || reason == "memory_low" || reason == "memory_for_models" || reason == "memory_pressure"
}

// montageWindows runs one chunk of a due window analysis, and reports whether
// it did any work. A job with more to do goes back to the head of the queue
// without spending its attempt, so the next round, after asking the gate,
// does the next chunk.
func (r *aiRunner) montageWindows(ctx context.Context, embedder Embedder, gate Gate, info EmbedderInfo) (bool, error) {
	lib := r.lib
	taker, ok := lib.media.(FrameTaker)
	if !ok {
		return false, nil
	}
	job, err := lib.claimJob(ctx, windowsDerivation)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !job.video || job.duration <= 0 {
		return true, lib.failJob(ctx, job, jobErrorUndecodable)
	}
	identity := windowsIdentity(info.Model)
	windows, middles := videoWindows(int(math.Round(job.duration * 1000)))
	var stored int
	if err := lib.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM video_windows WHERE object_id = ? AND identity = ?", job.objectID, identity).Scan(&stored); err != nil {
		return false, errors.Join(err, lib.returnJob(context.WithoutCancel(ctx), job))
	}
	if stored >= len(windows) {
		return true, lib.completeWindows(ctx, job, identity, len(windows))
	}
	r.progress.set(AIWorking, "", info.Model)
	chunk := windows[stored:min(stored+windowChunk, len(windows))]
	stopLease := lib.keepLease(ctx, job)
	defer stopLease()
	// Someone using the library, or short memory, hangs the chunk up.
	working, cancel := context.WithCancel(ctx)
	defer cancel()
	var hungUp atomic.Bool
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		ticker := time.NewTicker(gateWatchInterval)
		defer ticker.Stop()
		for {
			select {
			case <-working.Done():
				return
			case <-ticker.C:
				if open, reason := gate.Open(working, r.memoryNeed()); !open && closesForeground(reason) {
					r.progress.set(AIPaused, reason, "")
					hungUp.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	original, err := lib.openObject(job.objectID)
	if errors.Is(err, fs.ErrNotExist) {
		cancel()
		<-watched
		return true, lib.failJob(ctx, job, jobErrorMissingOriginal)
	}
	if err != nil {
		cancel()
		<-watched
		return false, errors.Join(err, lib.releaseJob(ctx, job))
	}
	defer func() { _ = original.Close() }()
	times := make([]float64, len(chunk))
	durationMs := int(math.Round(job.duration * 1000))
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
		input, err := memoryFile(data)
		if err != nil {
			return err
		}
		vector, err := embedder.EmbedImage(working, input)
		_ = input.Close()
		if err != nil {
			return err
		}
		if !validVector(vector, info.Dimensions) {
			return ErrAIRejected
		}
		window.Flat = deviation < windowFlat
		done, vectors = append(done, window), append(vectors, vector)
		return nil
	})
	cancel()
	<-watched
	// What came before a stop is kept: the next round goes on from there.
	if storeErr := lib.storeWindows(context.WithoutCancel(ctx), job.objectID, identity, done, vectors); storeErr != nil {
		return false, errors.Join(storeErr, lib.returnJob(context.WithoutCancel(ctx), job))
	}
	switch {
	case ctx.Err() != nil, hungUp.Load():
		// Stopping, or hung up: the job keeps its place and its attempt.
		return false, lib.returnJob(context.WithoutCancel(ctx), job)
	case err == nil:
	case errors.Is(err, ErrAIUnavailable):
		r.progress.set(AIUnavailable, "", "")
		return false, lib.releaseJob(ctx, job)
	case errors.Is(err, ErrAIRejected):
		return true, lib.failJob(ctx, job, jobErrorUndecodable)
	default:
		// The FFmpeg Worker's and the decoder's failures count as a
		// thumbnail's do.
		if class := decodeFailure(err); class != jobWaiting {
			return true, lib.failJob(ctx, job, class)
		}
		return false, lib.releaseJob(ctx, job)
	}
	if stored+len(done) >= len(windows) {
		return true, lib.completeWindows(ctx, job, identity, len(windows))
	}
	return true, lib.returnJob(ctx, job)
}

// storeWindows stores the windows a chunk made, replacing those of another
// identity.
func (s *Service) storeWindows(ctx context.Context, object, identity string, windows []Window, vectors [][]float32) error {
	if len(windows) == 0 {
		return nil
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		for i, window := range windows {
			if _, err := tx.ExecContext(ctx, `INSERT INTO video_windows(object_id, idx, start_ms, end_ms, flat, vector, identity)
				VALUES(?, ?, ?, ?, ?, ?, ?) ON CONFLICT(object_id, idx) DO UPDATE SET start_ms = excluded.start_ms,
				end_ms = excluded.end_ms, flat = excluded.flat, vector = excluded.vector, identity = excluded.identity`,
				object, window.Index, window.StartMs, window.EndMs, window.Flat, encodeVector(vectors[i]), identity); err != nil {
				return err
			}
		}
		return nil
	})
}

// completeWindows retires the job once every window has its vector, and
// drops windows beyond the video's end left by an earlier identity.
func (s *Service) completeWindows(ctx context.Context, job claimedJob, identity string, count int) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM video_windows WHERE object_id = ? AND (idx >= ? OR identity <> ?)", job.objectID, count, identity); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM jobs WHERE object_id = ? AND derivation = ?", job.objectID, job.derivation)
		return err
	})
}

// referencedObjects is the SQL of the objects a grant not revoked refers to.
const referencedObjects = `SELECT a.object_id FROM montage_grant_assets ga
	JOIN montage_grants g ON g.id = ga.grant_id AND g.revoked_at IS NULL
	JOIN assets a ON a.id = ga.asset_id`

// requeueStaleWindows queues again the window analysis of the objects a
// grant still refers to whose windows another model made. Objects only
// grants since revoked asked for are not computed again.
func (s *Service) requeueStaleWindows(ctx context.Context, model string) error {
	now := formatTime(s.now())
	_, err := s.db.ExecContext(ctx, `INSERT INTO jobs(object_id, derivation, state, not_before, created_at)
		SELECT DISTINCT w.object_id, ?, 'pending', ?, ? FROM video_windows w
		WHERE w.identity <> ? AND w.object_id IN (`+referencedObjects+`)
		ON CONFLICT DO NOTHING`, windowsDerivation, now, now, windowsIdentity(model))
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
			"DELETE FROM video_windows WHERE object_id IN (SELECT object_id FROM montage_objects WHERE unreferenced_since <= ?)",
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
