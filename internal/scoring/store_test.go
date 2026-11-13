//go:build linux

package scoring

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/filesystem"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type harness struct {
	store  *Store
	photos string
	clock  *clock
	random func() float64
	mu     sync.Mutex
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{photos: t.TempDir(), clock: &clock{now: time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)}}
	h.random = func() float64 { return 0.99 }
	store, err := Open(t.TempDir(), Options{
		PhotosDir: h.photos, Photos: filesystem.NewRootFileSystem(h.photos, false), Now: h.clock.Now,
		Random: func() float64 {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.random()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	h.store = store
	return h
}

func (h *harness) setRandom(random func() float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.random = random
}

func picture(t *testing.T, width, height int, shade uint8, encode func(io.Writer, image.Image) error) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.Set(x, y, color.RGBA{shade, uint8(x), uint8(y), 255})
		}
	}
	var buffer bytes.Buffer
	if err := encode(&buffer, img); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func jpegPicture(t *testing.T, width, height int, shade uint8) []byte {
	return picture(t, width, height, shade, func(w io.Writer, img image.Image) error { return jpeg.Encode(w, img, nil) })
}

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) importAll(t *testing.T) {
	t.Helper()
	for range 1000 {
		worked, err := h.store.ImportStep(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			return
		}
	}
	t.Fatal("import did not finish")
}

// batchOf makes a batch of count distinct photos and imports it.
func (h *harness) batchOf(t *testing.T, count, required int) Batch {
	t.Helper()
	source := fmt.Sprintf("批次%d", count*100+required)
	for i := range count {
		// PNG keeps every shade, so that no two photos are the same file.
		write(t, filepath.Join(h.photos, source, fmt.Sprintf("%03d.png", i)), picture(t, 16, 12, uint8(i), png.Encode))
	}
	batch, err := h.store.CreateBatch(context.Background(), "user:admin", BatchInput{Name: source, Source: source, Required: required})
	if err != nil {
		t.Fatal(err)
	}
	h.importAll(t)
	batch, err = h.store.Batch(context.Background(), batch.ID)
	if err != nil || batch.State != "open" || batch.Photos != count {
		t.Fatalf("batch %+v, %v", batch, err)
	}
	return batch
}

func (h *harness) raters(t *testing.T, accounts ...string) {
	t.Helper()
	for _, account := range accounts {
		if err := h.store.SetRater(context.Background(), account, "user:admin", true); err != nil {
			t.Fatal(err)
		}
	}
}

var submissions int

// half turns a test's score of 1 to 10 into hundredths of 0.5 to 5.00.
func half(value int) *int {
	hundredths := value * 50
	return &hundredths
}

// score answers with value, 1 to 10, as half points.
func score(value int) Submission {
	submissions++
	return Submission{ID: fmt.Sprintf("submission-%06d", submissions), Kind: "score", Score: half(value), ViewMilliseconds: 2500}
}

// itemOf reads which photo a token stands for, which no rater learns.
func (h *harness) itemOf(t *testing.T, token string) int64 {
	t.Helper()
	var item int64
	if err := h.store.db.QueryRow("SELECT item_id FROM claims WHERE token = ?", token).Scan(&item); err != nil {
		t.Fatal(err)
	}
	return item
}

func TestImportRendersUprightPreviewsMergesCopiesAndSkipsWhatItCannotUse(t *testing.T) {
	h := newHarness(t)
	source := filepath.Join(h.photos, "风景第一批")
	lake := jpegPicture(t, 300, 200, 10)
	write(t, filepath.Join(source, "湖.jpg"), lake)
	write(t, filepath.Join(source, "山", "副本.jpg"), lake)
	write(t, filepath.Join(source, "雪.png"), picture(t, 40, 60, 90, png.Encode))
	write(t, filepath.Join(source, "说明.txt"), []byte("not a photo"))
	write(t, filepath.Join(source, "坏.jpg"), append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte{7}, 600)...))
	write(t, filepath.Join(source, ".隐藏.jpg"), jpegPicture(t, 8, 8, 1))
	write(t, filepath.Join(source, AnchorFolder, "3.5-日落.jpg"), jpegPicture(t, 30, 20, 200))
	write(t, filepath.Join(source, AnchorFolder, "日落.jpg"), jpegPicture(t, 30, 20, 201))
	if err := os.Mkdir(filepath.Join(h.photos, "_备用"), 0o755); err != nil {
		t.Fatal(err)
	}
	sources, err := h.store.Sources(context.Background())
	if err != nil || !slices.Equal(sources, []string{"风景第一批"}) {
		t.Fatalf("sources %v, %v", sources, err)
	}
	batch, err := h.store.CreateBatch(context.Background(), "user:admin", BatchInput{Name: "第一批", Source: "风景第一批", Required: 3, Rubric: "构图与光线"})
	if err != nil || batch.State != "importing" {
		t.Fatalf("created %+v, %v", batch, err)
	}
	h.importAll(t)
	batch, err = h.store.Batch(context.Background(), batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if batch.State != "open" || batch.Files != 7 || batch.Imported != 4 || batch.Skipped != 3 || batch.Photos != 2 || batch.Anchors != 1 {
		t.Fatalf("batch after import %+v", batch)
	}
	var reasons []string
	err = each(context.Background(), h.store.db, "SELECT path || ':' || reason FROM import_files WHERE status = 'skipped' ORDER BY path", nil,
		func(rows *sql.Rows) error {
			var reason string
			err := rows.Scan(&reason)
			reasons = append(reasons, reason)
			return err
		})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"_锚点/日落.jpg:anchor_name", "坏.jpg:undecodable", "说明.txt:unsupported_type"}; !slices.Equal(reasons, want) {
		t.Fatalf("skipped %v, want %v", reasons, want)
	}
	sum := sha256.Sum256(lake)
	preview, err := os.ReadFile(h.store.previewPath(hex.EncodeToString(sum[:])))
	if err != nil {
		t.Fatal(err)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(preview))
	if err != nil || format != "jpeg" || config.Width != 300 || config.Height != 200 {
		t.Fatalf("preview %s %dx%d, %v; small photos keep their size", format, config.Width, config.Height, err)
	}
	if bytes.Contains(preview, []byte("Exif")) {
		t.Fatal("the preview kept EXIF")
	}
	anchors, err := h.store.Anchors(context.Background(), batch.ID)
	if err != nil || len(anchors) != 1 || anchors[0].Score != 350 {
		t.Fatalf("anchors %+v, %v", anchors, err)
	}
}

func TestPhotosAreCompletedBeforeNewOnesStartAndHeldPlacesCount(t *testing.T) {
	h := newHarness(t)
	batch := h.batchOf(t, 6, 2)
	h.raters(t, "a", "b", "c")
	ctx := context.Background()
	first, _, err := h.store.Claim(ctx, batch.ID, "a")
	if err != nil || len(first) != MaxHeld {
		t.Fatalf("a holds %d, %v", len(first), err)
	}
	again, _, err := h.store.Claim(ctx, batch.ID, "a")
	if err != nil || len(again) != MaxHeld || again[0].Token != first[0].Token {
		t.Fatalf("claiming again must return the held photos: %+v, %v", again, err)
	}
	var rated []int64
	for _, claim := range first {
		rated = append(rated, h.itemOf(t, claim.Token))
		if _, err := h.store.Submit(ctx, claim.Token, "a", score(6)); err != nil {
			t.Fatal(err)
		}
	}
	second, _, err := h.store.Claim(ctx, batch.ID, "b")
	if err != nil {
		t.Fatal(err)
	}
	for _, claim := range second {
		if !slices.Contains(rated, h.itemOf(t, claim.Token)) {
			t.Fatalf("b got a new photo while a's were not complete")
		}
	}
	// c finds a's photos taken by b's held places and gets new ones.
	third, _, err := h.store.Claim(ctx, batch.ID, "c")
	if err != nil {
		t.Fatal(err)
	}
	for _, claim := range third {
		if slices.Contains(rated, h.itemOf(t, claim.Token)) {
			t.Fatalf("c got a photo whose last place b holds")
		}
	}
}

func TestConcurrentRatersNeverGiveAPhotoMoreThanItsRaters(t *testing.T) {
	h := newHarness(t)
	batch := h.batchOf(t, 25, 3)
	var raters []string
	for i := range 8 {
		raters = append(raters, fmt.Sprintf("rater-%d", i))
	}
	h.raters(t, raters...)
	var wait sync.WaitGroup
	errs := make(chan error, len(raters))
	for i, rater := range raters {
		wait.Go(func() {
			for n := 0; ; n++ {
				claims, finished, err := h.store.Claim(context.Background(), batch.ID, rater)
				if err != nil {
					errs <- err
					return
				}
				if finished || len(claims) == 0 {
					return
				}
				submission := Submission{ID: fmt.Sprintf("concurrent-%d-%d", i, n), Kind: "score", Score: half(1 + n%10), ViewMilliseconds: 1}
				if _, err := h.store.Submit(context.Background(), claims[0].Token, rater, submission); err != nil {
					errs <- err
					return
				}
			}
		})
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var over, under, doubled int
	if err := h.store.db.QueryRow("SELECT count(*) FROM items WHERE anchor_score IS NULL AND covered > 3").Scan(&over); err != nil {
		t.Fatal(err)
	}
	if err := h.store.db.QueryRow("SELECT count(*) FROM items WHERE anchor_score IS NULL AND covered < 3").Scan(&under); err != nil {
		t.Fatal(err)
	}
	if err := h.store.db.QueryRow("SELECT count(*) FROM (SELECT 1 FROM ratings GROUP BY item_id, rater HAVING count(*) > 1)").Scan(&doubled); err != nil {
		t.Fatal(err)
	}
	if over != 0 || under != 0 || doubled != 0 {
		t.Fatalf("photos over %d, under %d, rated twice by one rater %d", over, under, doubled)
	}
}

func TestLeasesEndAndLateAnswersCountUnlessTheRaterAnsweredAgain(t *testing.T) {
	h := newHarness(t)
	batch := h.batchOf(t, 1, 1)
	h.raters(t, "a", "b")
	ctx := context.Background()
	held, _, _ := h.store.Claim(ctx, batch.ID, "a")
	if _, finished, _ := h.store.Claim(ctx, batch.ID, "b"); !finished {
		t.Fatal("b got the photo a holds")
	}
	h.clock.Advance(LeaseDuration + time.Second)
	taken, _, err := h.store.Claim(ctx, batch.ID, "b")
	if err != nil || len(taken) != 1 {
		t.Fatalf("b after a's lease: %+v, %v", taken, err)
	}
	if _, err := h.store.Submit(ctx, taken[0].Token, "b", score(4)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Submit(ctx, held[0].Token, "a", score(8)); err != nil {
		t.Fatalf("a's late answer: %v", err)
	}
	var covered int
	_ = h.store.db.QueryRow("SELECT covered FROM items").Scan(&covered)
	if covered != 2 {
		t.Fatalf("covered %d; a late answer is kept even past the batch's raters", covered)
	}

	h2 := newHarness(t)
	batch = h2.batchOf(t, 1, 2)
	h2.raters(t, "a")
	old, _, _ := h2.store.Claim(ctx, batch.ID, "a")
	h2.clock.Advance(LeaseDuration + time.Second)
	fresh, _, _ := h2.store.Claim(ctx, batch.ID, "a")
	if len(fresh) != 1 || fresh[0].Token == old[0].Token {
		t.Fatalf("a's photo did not come back after the lease: %+v", fresh)
	}
	if _, err := h2.store.Submit(ctx, fresh[0].Token, "a", score(5)); err != nil {
		t.Fatal(err)
	}
	if _, err := h2.store.Submit(ctx, old[0].Token, "a", score(5)); !errors.Is(err, ErrAlreadyRated) {
		t.Fatalf("second answer to the same photo: %v", err)
	}
}

func TestRetriesAreHarmlessAndOnlyTheLatestAnswerChanges(t *testing.T) {
	h := newHarness(t)
	batch := h.batchOf(t, 3, 1)
	h.raters(t, "a")
	ctx := context.Background()
	claims, _, _ := h.store.Claim(ctx, batch.ID, "a")
	first := score(3)
	for range 2 {
		result, err := h.store.Submit(ctx, claims[0].Token, "a", first)
		if err != nil || result.Rated != 1 {
			t.Fatalf("submit %+v, %v", result, err)
		}
	}
	if _, err := h.store.Submit(ctx, claims[1].Token, "a", first); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a submission id used for another photo: %v", err)
	}
	if _, err := h.store.Submit(ctx, claims[1].Token, "a", score(9)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Submit(ctx, claims[0].Token, "a", score(4)); !errors.Is(err, ErrLocked) {
		t.Fatalf("changing an answer that is not the latest: %v", err)
	}
	skip := Submission{ID: "change-latest-1", Kind: "skip", ViewMilliseconds: 100}
	if result, err := h.store.Submit(ctx, claims[1].Token, "a", skip); err != nil || result.Rated != 2 {
		t.Fatalf("changing the latest answer: %+v, %v", result, err)
	}
	var superseded, covered int
	_ = h.store.db.QueryRow("SELECT count(*) FROM ratings WHERE superseded_at IS NOT NULL").Scan(&superseded)
	_ = h.store.db.QueryRow("SELECT covered FROM items WHERE id = ?", h.itemOf(t, claims[1].Token)).Scan(&covered)
	if superseded != 1 || covered != 0 {
		t.Fatalf("superseded %d, covered %d after a score became a skip", superseded, covered)
	}
	for _, bad := range []Submission{
		{ID: "short", Kind: "score", Score: half(5)},
		{ID: "valid-id-1", Kind: "score", Score: half(11)},
		{ID: "valid-id-4", Kind: "score"},
		{ID: "valid-id-2", Kind: "skip", Score: half(3)},
		{ID: "valid-id-3", Kind: "love"},
	} {
		if _, err := h.store.Submit(ctx, claims[2].Token, "a", bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v accepted: %v", bad, err)
		}
	}
}

func TestRepeatsComeFromTheRatersOwnEarlierScoresAndDoNotCount(t *testing.T) {
	h := newHarness(t)
	batch := h.batchOf(t, repeatAfter+5, 1)
	h.raters(t, "a")
	ctx := context.Background()
	scores := map[int64]int{}
	for i := range repeatAfter {
		claims, _, err := h.store.Claim(ctx, batch.ID, "a")
		if err != nil {
			t.Fatal(err)
		}
		scores[h.itemOf(t, claims[0].Token)] = 1 + i%10
		if _, err := h.store.Submit(ctx, claims[0].Token, "a", score(1+i%10)); err != nil {
			t.Fatal(err)
		}
	}
	// The rater still holds the two photos preloaded last; the next one
	// handed out is a repeat of an earlier score.
	h.setRandom(func() float64 { return 0 })
	claims, _, err := h.store.Claim(ctx, batch.ID, "a")
	if err != nil || len(claims) != MaxHeld {
		t.Fatalf("claims %+v, %v", claims, err)
	}
	for _, claim := range claims[:2] {
		if _, earlier := scores[h.itemOf(t, claim.Token)]; earlier {
			t.Fatal("a preloaded photo is a repeat")
		}
		if _, err := h.store.Submit(ctx, claim.Token, "a", score(5)); err != nil {
			t.Fatal(err)
		}
	}
	repeat := claims[2]
	original, earlier := scores[h.itemOf(t, repeat.Token)]
	if !earlier {
		t.Fatal("the repeat is not one of the rater's earlier photos")
	}
	if _, err := h.store.Submit(ctx, repeat.Token, "a", score(original%10+1)); err != nil {
		t.Fatal(err)
	}
	stats, err := h.store.Stats(ctx, batch.ID)
	if err != nil || len(stats) != 1 {
		t.Fatalf("stats %+v, %v", stats, err)
	}
	want := math.Abs(float64(original%10+1-original)) / 2
	if stats[0].Repeats != 1 || stats[0].RepeatDifference == nil || *stats[0].RepeatDifference != want || stats[0].Scores != repeatAfter+2 {
		t.Fatalf("stats %+v", stats[0])
	}
	var covered int
	_ = h.store.db.QueryRow("SELECT covered FROM items WHERE id = ?", h.itemOf(t, repeat.Token)).Scan(&covered)
	if covered != 1 {
		t.Fatalf("a repeat counted towards the photo's raters: %d", covered)
	}
}

func TestOnlyChosenRatersRateAndClosedBatchesHandOutNothing(t *testing.T) {
	h := newHarness(t)
	batch := h.batchOf(t, 4, 1)
	ctx := context.Background()
	if _, _, err := h.store.Claim(ctx, batch.ID, "stranger"); !errors.Is(err, ErrNotRater) {
		t.Fatalf("claim by a non-rater: %v", err)
	}
	if _, err := h.store.OpenBatches(ctx, "stranger"); !errors.Is(err, ErrNotRater) {
		t.Fatalf("batches for a non-rater: %v", err)
	}
	h.raters(t, "a")
	held, _, _ := h.store.Claim(ctx, batch.ID, "a")
	closed := "closed"
	if _, err := h.store.UpdateBatch(ctx, batch.ID, BatchPatch{State: &closed}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Submit(ctx, held[0].Token, "a", score(2)); err != nil {
		t.Fatalf("a held photo is still answered after closing: %v", err)
	}
	claims, _, err := h.store.Claim(ctx, batch.ID, "a")
	if err != nil || len(claims) != 2 {
		t.Fatalf("closing hands out nothing new: %+v, %v", claims, err)
	}
	if err := h.store.SetRater(ctx, "a", "user:admin", false); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Submit(ctx, claims[0].Token, "a", score(2)); !errors.Is(err, ErrNotRater) {
		t.Fatalf("a removed rater answered: %v", err)
	}
	if err := h.store.DeleteBatch(ctx, batch.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("deleting a rated batch: %v", err)
	}
}

func TestStatsCompareEachRaterWithTheOthers(t *testing.T) {
	h := newHarness(t)
	batch := h.batchOf(t, 30, 3)
	h.raters(t, "steady", "copy", "contrary")
	ctx := context.Background()
	// steady and copy agree; contrary answers unrelated to them.
	answers := map[string]func(int) int{
		"steady":   func(n int) int { return 1 + n%10 },
		"copy":     func(n int) int { return 1 + n%10 },
		"contrary": func(n int) int { return 1 + (n*7+n/10*3)%10 },
	}
	for rater, answer := range answers {
		for {
			claims, finished, err := h.store.Claim(ctx, batch.ID, rater)
			if err != nil {
				t.Fatal(err)
			}
			if finished {
				break
			}
			item := h.itemOf(t, claims[0].Token)
			if _, err := h.store.Submit(ctx, claims[0].Token, rater, score(answer(int(item)))); err != nil {
				t.Fatal(err)
			}
		}
	}
	stats, err := h.store.Stats(ctx, batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	byRater := map[string]RaterStats{}
	for _, st := range stats {
		byRater[st.Rater] = st
	}
	steady, contrary := byRater["steady"], byRater["contrary"]
	if steady.Scores != 30 || steady.AgreementPhotos != 30 || steady.Agreement == nil || contrary.Agreement == nil {
		t.Fatalf("stats %+v", stats)
	}
	if *steady.Agreement <= *contrary.Agreement {
		t.Fatalf("agreement steady %.2f, contrary %.2f", *steady.Agreement, *contrary.Agreement)
	}
	if steady.MedianViewMilliseconds != 2500 || steady.FastShare != 0 || steady.LongestRun < 1 {
		t.Fatalf("steady %+v", steady)
	}
}

func TestExportHoldsEveryAnswerWithAManifestThatChecksIt(t *testing.T) {
	h := newHarness(t)
	batch := h.batchOf(t, 2, 1)
	h.raters(t, "a")
	ctx := context.Background()
	claims, _, _ := h.store.Claim(ctx, batch.ID, "a")
	if _, err := h.store.Submit(ctx, claims[0].Token, "a", score(3)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Submit(ctx, claims[0].Token, "a", score(7)); err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	if err := h.store.Export(ctx, batch.ID, &buffer, map[string]Person{"a": {Username: "alice"}}, "test"); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(buffer.Bytes()), int64(buffer.Len()))
	if err != nil {
		t.Fatal(err)
	}
	contents := map[string][]byte{}
	for _, file := range archive.File {
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		contents[file.Name], _ = io.ReadAll(reader)
		_ = reader.Close()
	}
	var manifest exportManifest
	if err := json.Unmarshal(contents["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Format != ExportFormat || manifest.Batch.ID != batch.ID || manifest.Batch.Required != 1 {
		t.Fatalf("manifest %+v", manifest)
	}
	for name, file := range manifest.Files {
		sum := sha256.Sum256(contents[name])
		if hex.EncodeToString(sum[:]) != file.SHA256 || strings.Count(string(contents[name]), "\n") != file.Lines {
			t.Errorf("%s does not match the manifest", name)
		}
	}
	if manifest.Files["images.jsonl"].Lines != 2 || manifest.Files["ratings.jsonl"].Lines != 2 {
		t.Fatalf("files %+v", manifest.Files)
	}
	if !strings.Contains(string(contents["ratings.jsonl"]), `"superseded":true`) || !strings.Contains(string(contents["raters.jsonl"]), `"username":"alice"`) {
		t.Fatalf("ratings %s raters %s", contents["ratings.jsonl"], contents["raters.jsonl"])
	}
}

func TestBackupsAreDailyAndKeepTheLatestFourteen(t *testing.T) {
	h := newHarness(t)
	for range 20 {
		if err := h.store.BackupIfDue(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := h.store.BackupIfDue(context.Background()); err != nil {
			t.Fatal(err)
		}
		h.clock.Advance(24 * time.Hour)
	}
	entries, err := os.ReadDir(filepath.Join(h.store.dir, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != backupsKept || entries[0].Name() != "scoring-2026-11-18.db" {
		t.Fatalf("backups %d, first %s", len(entries), entries[0].Name())
	}
}

func TestOnlyAnUnratedBatchIsDeletedAndItsPreviewsGo(t *testing.T) {
	h := newHarness(t)
	batch := h.batchOf(t, 2, 1)
	previews, _ := filepath.Glob(filepath.Join(h.store.dir, "previews", "*", "*.jpg"))
	if len(previews) != 2 {
		t.Fatalf("previews %v", previews)
	}
	if err := h.store.DeleteBatch(context.Background(), batch.ID); err != nil {
		t.Fatal(err)
	}
	previews, _ = filepath.Glob(filepath.Join(h.store.dir, "previews", "*", "*.jpg"))
	if len(previews) != 0 {
		t.Fatalf("previews left %v", previews)
	}
	if _, err := h.store.Batch(context.Background(), batch.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted batch: %v", err)
	}
}

func TestTheRulerShowsTheRatersLatestTwelveScoresWithSmallThumbnails(t *testing.T) {
	h := newHarness(t)
	batch := h.batchOf(t, Recent+3, 1)
	h.raters(t, "a", "b")
	ctx := context.Background()
	var tokens []string
	for i := range Recent + 2 {
		claims, _, err := h.store.Claim(ctx, batch.ID, "a")
		if err != nil {
			t.Fatal(err)
		}
		submission := score(1 + i%10)
		if i == 4 {
			submission = Submission{ID: "recent-skip-0001", Kind: "skip", ViewMilliseconds: 10}
		}
		if _, err := h.store.Submit(ctx, claims[0].Token, "a", submission); err != nil {
			t.Fatal(err)
		}
		if submission.Kind == "score" {
			tokens = append(tokens, claims[0].Token)
		}
	}
	recent, err := h.store.RecentScores(ctx, batch.ID, "a")
	if err != nil || len(recent) != Recent {
		t.Fatalf("recent %d, %v", len(recent), err)
	}
	if recent[len(recent)-1].Token != tokens[len(tokens)-1] || recent[0].Token != tokens[len(tokens)-Recent] {
		t.Fatal("the ruler does not hold the latest scores, oldest first")
	}
	if recent[len(recent)-1].Score != 13%10*50+50 || recent[0].Width != 16 || recent[0].Height != 12 {
		t.Fatalf("latest %+v, first %+v", recent[len(recent)-1], recent[0])
	}
	if others, err := h.store.RecentScores(ctx, batch.ID, "b"); err != nil || len(others) != 0 {
		t.Fatalf("b sees %d of a's scores, %v", len(others), err)
	}
	name, err := h.store.ClaimThumbnail(ctx, recent[0].Token, "a")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if config, format, err := image.DecodeConfig(bytes.NewReader(data)); err != nil || format != "jpeg" || config.Width > ThumbnailEdge {
		t.Fatalf("thumbnail %s %+v, %v", format, config, err)
	}
	if _, err := h.store.ClaimThumbnail(ctx, recent[0].Token, "b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("b opened a's thumbnail: %v", err)
	}
	cover, err := h.store.BatchCover(ctx, batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	var first string
	if err := h.store.db.QueryRow(`SELECT sha256 FROM items WHERE batch_id = ? AND anchor_score IS NULL ORDER BY shuffle LIMIT 1`, batch.ID).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if cover != h.store.thumbnailPath(first) {
		t.Fatalf("cover %s is not the first photo in the batch's order", cover)
	}
	if _, err := h.store.BatchCover(ctx, "no-such-batch"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cover of a batch that does not exist: %v", err)
	}
}
