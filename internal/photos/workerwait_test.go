package photos_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/clocktest"
	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
	"github.com/zhongwater123/A-NAS/internal/photos"
)

// awayDecoder is a FFmpeg Worker that answers each HEIC or video frame with
// the next queued error until none is left, and then decodes. While held is
// set, a decode reports on held and waits for its caller to give up.
type awayDecoder struct {
	mu       sync.Mutex
	failures []error
	held     chan struct{}
}

func (d *awayDecoder) fail(errs ...error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failures = append(d.failures, errs...)
}

func (d *awayDecoder) decode(ctx context.Context) ([]byte, error) {
	d.mu.Lock()
	held := d.held
	var err error
	if len(d.failures) > 0 {
		err, d.failures = d.failures[0], d.failures[1:]
	}
	d.mu.Unlock()
	if held != nil {
		held <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 48, 32)), nil); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func (d *awayDecoder) Probe(context.Context, *os.File) (ffmpegworker.Probe, error) {
	return phoneVideo(), nil
}
func (d *awayDecoder) Image(ctx context.Context, _ *os.File, _ int) ([]byte, error) {
	return d.decode(ctx)
}
func (d *awayDecoder) Frame(ctx context.Context, _ *os.File, _ float64, _ int) ([]byte, error) {
	return d.decode(ctx)
}
func (d *awayDecoder) Stream(context.Context, *os.File, ffmpegworker.StreamRequest) (io.ReadCloser, error) {
	return nil, errors.New("not used")
}

// heicBytes is the header of a 16 px square HEIC; the decoder reads no more.
func heicBytes(seed byte) []byte {
	box := func(kind string, parts ...[]byte) []byte {
		payload := bytes.Join(parts, nil)
		out := make([]byte, 8)
		binary.BigEndian.PutUint32(out, uint32(8+len(payload)))
		copy(out[4:], kind)
		return append(out, payload...)
	}
	size := make([]byte, 12)
	binary.BigEndian.PutUint32(size[4:], 16)
	binary.BigEndian.PutUint32(size[8:], 16)
	heic := box("ftyp", []byte("heic\x00\x00\x00\x00heic"))
	heic = append(heic, box("meta", make([]byte, 4), box("pitm", []byte{0, 0, 0, 0, 0, 1}),
		box("iprp", box("ipco", box("ispe", size)), box("ipma", []byte{0, 0, 0, 0, 0, 0, 0, 1, 0, 1, 1, 1})))...)
	return append(heic, box("mdat", []byte{seed})...)
}

func awayService(t *testing.T) (*photos.Service, *awayDecoder, *clocktest.Clock, string) {
	t.Helper()
	decoder, c := &awayDecoder{}, newClock()
	root := filepath.Join(t.TempDir(), "photos")
	s, err := photos.Open(root, photos.Options{Now: c.Now, Media: decoder, DisableCapacityReserve: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, decoder, c, root
}

func jobCount(t *testing.T, root, derivation string) int {
	t.Helper()
	var count int
	if err := catalogDB(t, root).QueryRow("SELECT COUNT(*) FROM jobs WHERE derivation = ?", derivation).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// The failure scenario of issue #168: the worker is away for longer than a
// job's three attempts would last, while HEIC photos arrive.
func TestThumbnailsAndAIWaitForAMediaWorkerThatIsAway(t *testing.T) {
	ctx := context.Background()
	s, decoder, c, root := awayService(t)
	private, _ := libraries(t, s, alice)
	asset := importPhoto(t, s, alice, private.ID, "", "IMG_0001.HEIC", heicBytes(1))

	away := []error{
		fmt.Errorf("%w: dial unix /run/a-nas-ffmpeg/ffmpeg.sock: connect: no such file or directory", ffmpegworker.ErrUnavailable),
		fmt.Errorf("%w: EOF", ffmpegworker.ErrUnavailable), // systemd dropped the connection
		ffmpegworker.ErrBusy,
		fmt.Errorf("%w: decoder is unavailable", ffmpegworker.ErrUnavailable),
	}
	decoder.fail(away...)
	for range away {
		if ok, err := s.ProcessMediaJob(ctx); ok || err != nil {
			t.Fatalf("ProcessMediaJob() while the worker is away = %v, %v; want the job to wait", ok, err)
		}
		if state, attempts, _ := jobState(t, root, "thumbnail/v1"); state != "pending" || attempts != 0 {
			t.Fatalf("thumbnail job while the worker is away: %s after %d attempts", state, attempts)
		}
		if ok, err := s.ProcessMediaJob(ctx); ok || err != nil {
			t.Fatalf("ProcessMediaJob() right after waiting = %v, %v; want the job to wait a minute", ok, err)
		}
		c.Advance(time.Minute)
	}
	if ok, err := s.ProcessMediaJob(ctx); !ok || err != nil {
		t.Fatalf("ProcessMediaJob() once the worker is back = %v, %v", ok, err)
	}
	if got, err := s.Get(ctx, alice, asset.ID); err != nil || got.Thumbnail != photos.ThumbnailReady {
		t.Fatalf("thumbnail after the worker came back = %q, %v", got.Thumbnail, err)
	}

	// The detector's input is rendered from the original again, and waits
	// the same way.
	worker, gate := newDetectingEmbedder(), &fakeGate{open: true}
	decoder.fail(away...)
	// The vector reads the thumbnail; the detection then waits.
	if processed := processAI(t, s, worker, gate); processed != 1 {
		t.Fatalf("processed %d jobs, want the vector", processed)
	}
	for i := range away {
		if state, attempts, _ := jobState(t, root, "objects/v1"); state != "pending" || attempts != 0 {
			t.Fatalf("detection job while the worker is away: %s after %d attempts", state, attempts)
		}
		c.Advance(time.Minute)
		if i == len(away)-1 {
			break
		}
		if ok, err := s.ProcessAIJob(ctx, worker, gate); ok || err != nil {
			t.Fatalf("ProcessAIJob() while the worker is away = %v, %v; want the detection to wait", ok, err)
		}
	}
	if processed := processAI(t, s, worker, gate); processed != 1 || jobCount(t, root, "objects/v1") != 0 {
		t.Fatalf("processed %d jobs once the worker is back, %d detections left", processed, jobCount(t, root, "objects/v1"))
	}
}

func TestAWorkerThatStopsWhileDecodingSpendsTheAttempts(t *testing.T) {
	ctx := context.Background()
	s, decoder, c, root := awayService(t)
	private, _ := libraries(t, s, alice)
	asset := importPhoto(t, s, alice, private.ID, "", "IMG_0002.HEIC", heicBytes(2))
	stopped := fmt.Errorf("%w: EOF", ffmpegworker.ErrInterrupted)
	decoder.fail(stopped, stopped, stopped)
	for attempt := 1; attempt <= 3; attempt++ {
		if ok, err := s.ProcessMediaJob(ctx); !ok || err != nil {
			t.Fatalf("attempt %d = %v, %v", attempt, ok, err)
		}
		c.Advance(5 * time.Minute)
	}
	if state, attempts, class := jobState(t, root, "thumbnail/v1"); state != "failed" || attempts != 3 || class != "interrupted" {
		t.Fatalf("job = %s, %d attempts, %q; want failed as interrupted", state, attempts, class)
	}
	// An original that keeps killing the worker does not come back at the
	// next start.
	if recovery, err := s.RecoverJobs(ctx); err != nil || recovery != (photos.JobRecovery{}) {
		t.Fatalf("RecoverJobs() = %+v, %v", recovery, err)
	}
	if got, _ := s.Get(ctx, alice, asset.ID); got.Thumbnail != photos.ThumbnailFailed {
		t.Fatalf("thumbnail = %q, want failed", got.Thumbnail)
	}

	// Running out of time also spends attempts, and a later start retries it.
	slow := importPhoto(t, s, alice, private.ID, "", "IMG_0003.HEIC", heicBytes(3))
	timedOut := fmt.Errorf("%w: timed out", ffmpegworker.ErrFailed)
	decoder.fail(timedOut, context.DeadlineExceeded, timedOut)
	for range 3 {
		if ok, err := s.ProcessMediaJob(ctx); !ok || err != nil {
			t.Fatalf("ProcessMediaJob() = %v, %v", ok, err)
		}
		c.Advance(5 * time.Minute)
	}
	if got, _ := s.Get(ctx, alice, slow.ID); got.Thumbnail != photos.ThumbnailFailed {
		t.Fatalf("thumbnail after three timeouts = %q, want failed", got.Thumbnail)
	}
	if recovery, err := s.RecoverJobs(ctx); err != nil || recovery != (photos.JobRecovery{Requeued: 1}) {
		t.Fatalf("RecoverJobs() = %+v, %v; want the timed out job requeued", recovery, err)
	}
	processAll(t, s)
	if got, _ := s.Get(ctx, alice, slow.ID); got.Thumbnail != photos.ThumbnailReady {
		t.Fatalf("thumbnail after the start = %q, want ready", got.Thumbnail)
	}
}

func TestStoppingGivesTheJobBackWithItsAttempt(t *testing.T) {
	s, decoder, _, root := awayService(t)
	private, _ := libraries(t, s, alice)
	importPhoto(t, s, alice, private.ID, "", "IMG_0004.HEIC", heicBytes(4))
	decoder.held = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-decoder.held
		cancel()
	}()
	if ok, err := s.ProcessMediaJob(ctx); ok || err != nil {
		t.Fatalf("ProcessMediaJob() while stopping = %v, %v", ok, err)
	}
	if state, attempts, _ := jobState(t, root, "thumbnail/v1"); state != "pending" || attempts != 0 {
		t.Fatalf("job after stopping = %s after %d attempts; want pending with its attempt", state, attempts)
	}
}

func TestRecoverJobsResumesWhatACrashLeftAndRetriesStorageFaults(t *testing.T) {
	ctx := context.Background()
	s, c, root := newService(t)
	private, _ := libraries(t, s, alice)
	var objects []string
	for shade := range uint8(3) {
		asset := importPhoto(t, s, alice, private.ID, "", fmt.Sprintf("p%d.png", shade), encodePNG(40+shade))
		var object string
		if err := catalogDB(t, root).QueryRow("SELECT object_id FROM assets WHERE id = ?", asset.ID).Scan(&object); err != nil {
			t.Fatal(err)
		}
		objects = append(objects, object)
	}
	// A crash cut off one job on its first attempt and one on its last; a
	// third failed on storage before.
	lease := c.Now().Add(5 * time.Minute).UTC().Format("2006-01-02T15:04:05.000000000Z")
	db := catalogDB(t, root)
	for i, change := range []string{
		"UPDATE jobs SET state = 'running', attempts = 1, lease_until = ? WHERE object_id = ?",
		"UPDATE jobs SET state = 'running', attempts = 3, lease_until = ? WHERE object_id = ?",
		"UPDATE jobs SET state = 'failed', attempts = 3, error_class = 'io', lease_until = ? WHERE object_id = ?",
	} {
		if _, err := db.Exec(change, lease, objects[i]); err != nil {
			t.Fatal(err)
		}
	}
	recovery, err := s.RecoverJobs(ctx)
	if err != nil || recovery != (photos.JobRecovery{Resumed: 1, Requeued: 1}) {
		t.Fatalf("RecoverJobs() = %+v, %v", recovery, err)
	}
	// Both run now, not once the lease would have run out.
	if processed := processAll(t, s); processed != 2 {
		t.Fatalf("processed %d jobs after recovery, want 2", processed)
	}
	var state, class string
	var attempts int
	if err := db.QueryRow("SELECT state, attempts, error_class FROM jobs WHERE object_id = ?", objects[1]).Scan(&state, &attempts, &class); err != nil ||
		state != "failed" || attempts != 3 || class != "interrupted" {
		t.Fatalf("job cut off on its last attempt = %s, %d, %q, %v; want failed as interrupted", state, attempts, class, err)
	}
}
