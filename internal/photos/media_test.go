package photos_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/photos"
)

// exifJPEG returns a w×h JPEG whose APP1 segment records an orientation and
// a DateTimeOriginal with an optional OffsetTimeOriginal.
func exifJPEG(t *testing.T, w, h int, orientation uint16, taken, offset string) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		for y := range h {
			// Left half red, right half blue, so orientation is observable.
			if x < w/2 {
				img.Set(x, y, color.RGBA{R: 0xff, A: 0xff})
			} else {
				img.Set(x, y, color.RGBA{B: 0xff, A: 0xff})
			}
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatalf("encode JPEG: %v", err)
	}

	// Little-endian TIFF: header, IFD0 {Orientation, ExifIFD pointer}, then
	// the Exif IFD {DateTimeOriginal, OffsetTimeOriginal} and its strings.
	le := binary.LittleEndian
	var tiff bytes.Buffer
	write := func(values ...any) {
		for _, value := range values {
			_ = binary.Write(&tiff, le, value)
		}
	}
	entry := func(tag, kind uint16, count, value uint32) { write(tag, kind, count, value) }
	const ifd0 = 8
	const exifIFD = ifd0 + 2 + 2*12 + 4
	entries := uint16(1)
	if offset != "" {
		entries = 2
	}
	strings := uint32(exifIFD + 2 + int(entries)*12 + 4)
	write([]byte("II"), uint16(42), uint32(ifd0))
	write(uint16(2))
	entry(0x0112, 3, 1, uint32(orientation))
	entry(0x8769, 4, 1, exifIFD)
	write(uint32(0))
	write(entries)
	entry(0x9003, 2, uint32(len(taken)+1), strings)
	if offset != "" {
		entry(0x9011, 2, uint32(len(offset)+1), strings+uint32(len(taken)+1))
	}
	write(uint32(0))
	tiff.WriteString(taken + "\x00")
	if offset != "" {
		tiff.WriteString(offset + "\x00")
	}

	app1 := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	var out bytes.Buffer
	out.Write(encoded.Bytes()[:2])
	out.Write([]byte{0xff, 0xe1})
	_ = binary.Write(&out, binary.BigEndian, uint16(len(app1)+2))
	out.Write(app1)
	out.Write(encoded.Bytes()[2:])
	return out.Bytes()
}

func readThumbnail(t *testing.T, service *photos.Service, p photos.Principal, assetID string) image.Image {
	t.Helper()
	content, err := service.Thumbnail(context.Background(), p, assetID)
	if err != nil {
		t.Fatalf("Thumbnail() error = %v", err)
	}
	defer content.Reader.Close()
	if content.MediaType != "image/jpeg" {
		t.Fatalf("thumbnail type = %q", content.MediaType)
	}
	img, err := jpeg.Decode(content.Reader)
	if err != nil {
		t.Fatalf("decode thumbnail: %v", err)
	}
	return img
}

func processAll(t *testing.T, service *photos.Service) int {
	t.Helper()
	var processed int
	for {
		ok, err := service.ProcessMediaJob(context.Background())
		if err != nil {
			t.Fatalf("ProcessMediaJob() error = %v", err)
		}
		if !ok {
			return processed
		}
		processed++
	}
}

func TestImportReadsEXIFOrientationAndCaptureTime(t *testing.T) {
	ctx := context.Background()
	c := newClock()
	shanghai := time.FixedZone("CST", 8*3600)
	service, err := photos.Open(filepath.Join(t.TempDir(), "photos"), photos.Options{
		Now: c.Now, DisableCapacityReserve: true, Location: shanghai,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	private, _ := libraries(t, service, alice)

	rotated := importPhoto(t, service, alice, private.ID, "", "rotated.jpg", exifJPEG(t, 40, 20, 6, "2026:05:01 10:00:00", "+02:00"))
	if rotated.Width != 20 || rotated.Height != 40 {
		t.Fatalf("display size = %dx%d, want 20x40 for orientation 6", rotated.Width, rotated.Height)
	}
	if want := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC); rotated.TakenAt == nil || !rotated.TakenAt.Equal(want) {
		t.Fatalf("TakenAt = %v, want %v", rotated.TakenAt, want)
	}
	local := importPhoto(t, service, alice, private.ID, "", "local.jpg", exifJPEG(t, 8, 8, 1, "2026:05:01 10:00:00", ""))
	if want := time.Date(2026, 5, 1, 2, 0, 0, 0, time.UTC); local.TakenAt == nil || !local.TakenAt.Equal(want) {
		t.Fatalf("TakenAt without offset = %v, want NAS local time %v", local.TakenAt, want)
	}
	plain := importPhoto(t, service, alice, private.ID, "", "plain.png", encodePNG(30))
	if plain.TakenAt != nil || plain.Width != 4 || plain.Height != 3 {
		t.Fatalf("PNG without EXIF = %+v", plain)
	}

	// Capture time orders the timeline; the PNG falls back to its import time.
	page, err := service.Timeline(ctx, alice, private.ID, "", 10)
	if err != nil {
		t.Fatalf("Timeline() error = %v", err)
	}
	var order []string
	for _, asset := range page.Assets {
		order = append(order, asset.Name)
	}
	if len(order) != 3 || order[0] != "plain.png" || order[1] != "rotated.jpg" || order[2] != "local.jpg" {
		t.Fatalf("timeline order = %v", order)
	}
}

func TestMediaJobsRenderUprightThumbnailsOncePerOriginal(t *testing.T) {
	ctx := context.Background()
	service, _, root := newService(t)
	private, shared := libraries(t, service, alice)

	rotated := importPhoto(t, service, alice, private.ID, "", "rotated.jpg", exifJPEG(t, 1200, 600, 6, "2026:05:01 10:00:00", ""))
	if rotated.Thumbnail != photos.ThumbnailPending {
		t.Fatalf("Thumbnail = %q right after import, want pending", rotated.Thumbnail)
	}
	if _, err := service.Thumbnail(ctx, alice, rotated.ID); !errors.Is(err, photos.ErrThumbnailUnavailable) {
		t.Fatalf("Thumbnail() before processing error = %v", err)
	}
	if processed := processAll(t, service); processed != 1 {
		t.Fatalf("processed %d jobs, want 1", processed)
	}
	got, err := service.Get(ctx, alice, rotated.ID)
	if err != nil || got.Thumbnail != photos.ThumbnailReady {
		t.Fatalf("Get() = %+v, %v; want a ready thumbnail", got, err)
	}
	thumbnail := readThumbnail(t, service, alice, rotated.ID)
	if bounds := thumbnail.Bounds(); bounds.Dx() != 256 || bounds.Dy() != 512 {
		t.Fatalf("thumbnail = %dx%d, want 256x512 upright", bounds.Dx(), bounds.Dy())
	}
	// Orientation 6 turns the red left half into the top half.
	if r, _, b, _ := thumbnail.At(128, 64).RGBA(); r < 0xc000 || b > 0x4000 {
		t.Fatalf("top of the upright thumbnail is not red: r=%x b=%x", r, b)
	}

	// Another asset with the same original reuses the thumbnail.
	copied := importPhoto(t, service, bob, shared.ID, "", "same.jpg", exifJPEG(t, 1200, 600, 6, "2026:05:01 10:00:00", ""))
	if copied.Thumbnail != photos.ThumbnailReady || processAll(t, service) != 0 {
		t.Fatalf("identical original queued a second thumbnail")
	}

	if _, err := service.Trash(ctx, alice, rotated.ID); err != nil {
		t.Fatalf("Trash() error = %v", err)
	}
	if err := service.Purge(ctx, alice, rotated.ID); err != nil {
		t.Fatalf("Purge() error = %v", err)
	}
	if _, err := service.Trash(ctx, bob, copied.ID); err != nil {
		t.Fatalf("Trash() error = %v", err)
	}
	if err := service.Purge(ctx, bob, copied.ID); err != nil {
		t.Fatalf("Purge() error = %v", err)
	}
	if derived, _ := filepath.Glob(filepath.Join(root, "derived", "*", "*", "*")); len(derived) != 0 {
		t.Fatalf("thumbnail kept after its original was purged: %v", derived)
	}
}

func TestUndecodableOriginalFailsItsThumbnailButStaysUsable(t *testing.T) {
	ctx := context.Background()
	service, _, _ := newService(t)
	private, _ := libraries(t, service, alice)
	noise := image.NewGray(image.Rect(0, 0, 256, 256))
	for i := range noise.Pix {
		noise.Pix[i] = uint8(i * 7919 % 251)
	}
	var whole bytes.Buffer
	if err := jpeg.Encode(&whole, noise, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatalf("encode JPEG: %v", err)
	}
	// The header is intact, so import accepts it; half the scan data is cut off.
	truncated := importPhoto(t, service, alice, private.ID, "", "truncated.jpg", whole.Bytes()[:whole.Len()/2])

	if processed := processAll(t, service); processed != 1 {
		t.Fatalf("processed %d jobs, want 1", processed)
	}
	got, err := service.Get(ctx, alice, truncated.ID)
	if err != nil || got.Thumbnail != photos.ThumbnailFailed {
		t.Fatalf("Get() = %+v, %v; want a failed thumbnail", got, err)
	}
	content, err := service.Open(ctx, alice, truncated.ID)
	if err != nil {
		t.Fatalf("Open() of an asset without thumbnail error = %v", err)
	}
	_ = content.Reader.Close()
}

func TestExpiredJobLeaseIsReclaimedAfterACrash(t *testing.T) {
	ctx := context.Background()
	service, c, root := newService(t)
	private, _ := libraries(t, service, alice)
	asset := importPhoto(t, service, alice, private.ID, "", "p.png", encodePNG(31))

	// A worker claimed the job and then the process died.
	db, err := sql.Open("sqlite3", filepath.Join(root, "catalog.db")+"?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	defer db.Close()
	leaseUntil := c.Now().Add(5 * time.Minute).UTC().Format("2006-01-02T15:04:05.000000000Z")
	if _, err := db.Exec("UPDATE jobs SET state = 'running', attempts = 1, lease_until = ?", leaseUntil); err != nil {
		t.Fatalf("simulate claimed job: %v", err)
	}

	if ok, err := service.ProcessMediaJob(ctx); err != nil || ok {
		t.Fatalf("ProcessMediaJob() during the lease = %v, %v; want nothing to do", ok, err)
	}
	c.Advance(5 * time.Minute)
	if ok, err := service.ProcessMediaJob(ctx); err != nil || !ok {
		t.Fatalf("ProcessMediaJob() after the lease = %v, %v", ok, err)
	}
	if got, _ := service.Get(ctx, alice, asset.ID); got.Thumbnail != photos.ThumbnailReady {
		t.Fatalf("Thumbnail = %q after reclaiming the job", got.Thumbnail)
	}
}

func TestRunMediaWakesOnImport(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	service, _, _ := newService(t)
	private, _ := libraries(t, service, alice)
	done := make(chan struct{})
	go func() {
		defer close(done)
		service.RunMedia(ctx, time.Hour, func(err error) { t.Errorf("RunMedia error: %v", err) })
	}()
	t.Cleanup(func() { cancel(); <-done })

	asset := importPhoto(t, service, alice, private.ID, "", "p.png", encodePNG(32))
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, err := service.Get(context.Background(), alice, asset.ID)
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if got.Thumbnail == photos.ThumbnailReady {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("thumbnail not rendered after an import woke the worker")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestReconcileRepairsDerivedFiles(t *testing.T) {
	ctx := context.Background()
	service, _, root := newService(t)
	private, _ := libraries(t, service, alice)
	asset := importPhoto(t, service, alice, private.ID, "", "p.png", encodePNG(33))
	processAll(t, service)
	thumbnails, _ := filepath.Glob(filepath.Join(root, "derived", "thumbnail-v1", "*", "*.jpg"))
	if len(thumbnails) != 1 {
		t.Fatalf("thumbnails = %v", thumbnails)
	}
	if err := os.Remove(thumbnails[0]); err != nil {
		t.Fatalf("remove thumbnail: %v", err)
	}
	stray := filepath.Join(root, "derived", "thumbnail-v1", "00", "00"+string(bytes.Repeat([]byte("0"), 62))+".jpg")
	writeFile(t, stray, []byte("stale"))

	report, err := service.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if report.DerivedRemoved != 1 || report.DerivedRequeued != 1 {
		t.Fatalf("Reconcile() = %+v", report)
	}
	if got, _ := service.Get(ctx, alice, asset.ID); got.Thumbnail != photos.ThumbnailPending {
		t.Fatalf("Thumbnail = %q after its file was lost, want pending", got.Thumbnail)
	}
	if processAll(t, service) != 1 {
		t.Fatalf("lost thumbnail was not rebuilt")
	}
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Fatalf("stray derived file kept")
	}
}

func TestPNGWithTransparencyIsFlattenedOnWhite(t *testing.T) {
	service, _, _ := newService(t)
	private, _ := libraries(t, service, alice)
	img := image.NewNRGBA(image.Rect(0, 0, 10, 10))
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatalf("encode PNG: %v", err)
	}
	asset := importPhoto(t, service, alice, private.ID, "", "clear.png", encoded.Bytes())
	processAll(t, service)
	if r, g, b, _ := readThumbnail(t, service, alice, asset.ID).At(5, 5).RGBA(); r < 0xf000 || g < 0xf000 || b < 0xf000 {
		t.Fatalf("transparent pixel = %x,%x,%x; want white", r, g, b)
	}
}
