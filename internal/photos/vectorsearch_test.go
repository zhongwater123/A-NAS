package photos_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/clocktest"
	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photos/objects"
	"github.com/zhongwater123/A-NAS/internal/vectorindex"
	"github.com/zhongwater123/A-NAS/internal/vectorindex/vectorindextest"
)

// fallbacks records why ranking read the Catalog instead of the index.
type fallbacks struct {
	mu      sync.Mutex
	reasons []string
}

func (f *fallbacks) record(reason string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reasons = append(f.reasons, fmt.Sprint(reason, err))
}

func (f *fallbacks) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	taken := f.reasons
	f.reasons = nil
	return taken
}

// indexedLibrary opens a library ranked through a real vector index, with a
// detector, AI labels, text read in some photos and more photos than search
// keeps by similarity, some of them in two libraries.
func indexedLibrary(t *testing.T) (*photos.Service, *colorDetector, *vectorindex.Client, *fallbacks, photos.Library, []photos.Asset) {
	t.Helper()
	instance := vectorindextest.Start(t)
	vectors := vectorindex.New(instance.Socket, nil)
	t.Cleanup(func() { _ = vectors.Close() })
	calibration, err := objects.ParseCalibration([]byte(`{"model": "detector-1", "weight": 0.2,
		"tierCoverage": 0.6, "fusion": [62, 140], "words": {"62": {"fusion": true, "tierScore": 0.6},
		"140": {"fusion": true, "tierScore": 0.6}, "24": {"fusion": false}}}`))
	if err != nil {
		t.Fatal(err)
	}
	worker := &colorDetector{colorEmbedder: &colorEmbedder{}, model: "detector-1"}
	fell := &fallbacks{}
	root := filepath.Join(t.TempDir(), "photos")
	service, err := photos.Open(root, photos.Options{Now: newClock().Now,
		DisableCapacityReserve: true, AI: worker, Objects: &calibration, Labels: colourLabels(t, "colour-1"),
		Vectors: vectors, OnVectorFallback: fell.record})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	private, shared := libraries(t, service, alice)
	var imported []photos.Asset
	for i := range 620 {
		// Prime periods: every photo has a colour of its own, so a content
		// object of its own.
		r, g, b := uint8(i*37%251), uint8(i*91%241), uint8(i*53%239)
		name := fmt.Sprintf("p%03d.png", i)
		if i%50 == 0 {
			name = fmt.Sprintf("手机壳%03d.png", i)
		}
		library := private.ID
		if i%7 == 0 {
			library = shared.ID
		}
		imported = append(imported, importPhoto(t, service, alice, library, "", name, solidPNG(r, g, b)))
		if i%31 == 0 {
			// The same photo in the other library too: one content object,
			// two assets.
			other := shared.ID
			if library == shared.ID {
				other = private.ID
			}
			importPhoto(t, service, alice, other, "", "copy-"+name, solidPNG(r, g, b))
		}
	}
	index(t, service, worker)
	syncAll(t, service, vectors)
	// Text read in every 13th photo: search places those photos in a band of
	// their own whatever their similarity, so most rank low by it.
	db := catalogDB(t, root)
	for i, asset := range imported {
		if i%13 != 5 {
			continue
		}
		if _, err := db.ExecContext(context.Background(), `
INSERT INTO texts(object_id, model, kind, text, finish, fields, checks, created_at)
SELECT object_id, 'ocr-1', 'plain', '超市小票 合计 128.00', 'stop', '{}', '[]', '2026-11-06T00:00:00.000000000Z'
FROM assets WHERE id = ?`, asset.ID); err != nil {
			t.Fatal(err)
		}
	}
	return service, worker, vectors, fell, private, imported
}

// everything is every page of a search, with what the first page says.
type everything struct {
	ids            []string
	closest, texts int
	objects        *photos.ObjectsFound
}

func searchEverything(t *testing.T, service *photos.Service, query string) everything {
	t.Helper()
	var all everything
	cursor := ""
	for first := true; ; first = false {
		page, err := service.Search(context.Background(), alice, photos.SearchRequest{Query: query, Cursor: cursor, Limit: 97})
		if err != nil {
			t.Fatalf("Search(%q) error = %v", query, err)
		}
		if first {
			all.closest, all.texts, all.objects = page.Closest, page.Texts, page.Objects
		}
		for _, asset := range page.Assets {
			all.ids = append(all.ids, asset.ID)
		}
		if cursor = page.Next; cursor == "" {
			return all
		}
	}
}

func similarEverything(t *testing.T, service *photos.Service, assetID string) []string {
	t.Helper()
	var ids []string
	cursor := ""
	for {
		page, err := service.Similar(context.Background(), alice, assetID, photos.SimilarRequest{Cursor: cursor, Limit: 89})
		if err != nil {
			t.Fatalf("Similar(%s) error = %v", assetID, err)
		}
		for _, asset := range page.Assets {
			ids = append(ids, asset.ID)
		}
		if cursor = page.Next; cursor == "" {
			return ids
		}
	}
}

func imageSearchEverything(t *testing.T, service *photos.Service, id string) []string {
	t.Helper()
	var ids []string
	cursor := ""
	for {
		page, err := service.ImageSearchResults(context.Background(), alice, id, photos.SimilarRequest{Cursor: cursor, Limit: 83})
		if err != nil {
			t.Fatalf("ImageSearchResults() error = %v", err)
		}
		for _, asset := range page.Assets {
			ids = append(ids, asset.ID)
		}
		if cursor = page.Next; cursor == "" {
			return ids
		}
	}
}

// ranking is everything the read paths answer for the library.
type ranking struct {
	searches map[string]everything
	similar  map[string][]string
	image    []string
	counts   []photos.LabelCount
	clusters map[string][]string
}

func rankAll(t *testing.T, service *photos.Service, sources []string, imageSearch string) ranking {
	t.Helper()
	r := ranking{searches: map[string]everything{}, similar: map[string][]string{}, clusters: map[string][]string{}}
	// 小票 finds the photos with text; png every name, p01 a few.
	for _, query := range []string{"红", "绿色的手机", "手机", "蓝色", "猫", "手机壳", "傍晚在海边散步时用手机拍下的日落",
		"小票", "png", "p01"} {
		r.searches[query] = searchEverything(t, service, query)
	}
	for _, source := range sources {
		r.similar[source] = similarEverything(t, service, source)
	}
	r.image = imageSearchEverything(t, service, imageSearch)
	counts, ready, err := service.LabelCounts(context.Background(), alice, "")
	if err != nil || !ready {
		t.Fatalf("LabelCounts() = %v, %v", ready, err)
	}
	r.counts = counts
	for _, label := range []string{"red", "blue"} {
		r.clusters[label] = cluster(t, service, alice, label)
	}
	return r
}

func compareRankings(t *testing.T, what string, got, want ranking) {
	t.Helper()
	for query, w := range want.searches {
		g := got.searches[query]
		if !slices.Equal(g.ids, w.ids) || g.closest != w.closest || g.texts != w.texts || fmt.Sprint(g.objects) != fmt.Sprint(w.objects) {
			t.Errorf("%s: search %q = %d results, closest %d, texts %d, objects %v; want %d, %d, %d, %v (first difference at %d)",
				what, query, len(g.ids), g.closest, g.texts, g.objects, len(w.ids), w.closest, w.texts, w.objects,
				firstDifference(g.ids, w.ids))
		}
	}
	for source, w := range want.similar {
		if g := got.similar[source]; !slices.Equal(g, w) {
			t.Errorf("%s: similar to %s differs at %d of %d", what, source, firstDifference(g, w), len(w))
		}
	}
	if !slices.Equal(got.image, want.image) {
		t.Errorf("%s: image search differs at %d of %d", what, firstDifference(got.image, want.image), len(want.image))
	}
	if fmt.Sprint(got.counts) != fmt.Sprint(want.counts) {
		t.Errorf("%s: label counts = %+v, want %+v", what, got.counts, want.counts)
	}
	for label, w := range want.clusters {
		if g := got.clusters[label]; !slices.Equal(g, w) {
			t.Errorf("%s: cluster %s differs at %d of %d", what, label, firstDifference(g, w), len(w))
		}
	}
}

func firstDifference(a, b []string) int {
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}

// Ranking through the index gives what ranking the Catalog's vectors gives:
// the same photos in the same order on every page, the same bands and the
// same clusters, also while photos wait to be indexed and when the index
// cannot answer.
func TestTheIndexRanksAsTheCatalogDoes(t *testing.T) {
	service, worker, vectors, fell, private, imported := indexedLibrary(t)
	var sources []string
	for i, asset := range imported {
		if i%101 == 0 {
			sources = append(sources, asset.ID)
		}
	}
	imageSearch, err := service.SearchByImage(context.Background(), alice, bytes.NewReader(solidPNG(200, 40, 40)))
	if err != nil {
		t.Fatal(err)
	}

	compare := func(what string) {
		t.Helper()
		photos.UseVectorIndex(service, nil)
		want := rankAll(t, service, sources, imageSearch)
		photos.UseVectorIndex(service, vectors)
		compareRankings(t, what, rankAll(t, service, sources, imageSearch), want)
		// Asking the index for no more than it needs makes it ask again
		// below the cut for what a bonus could lift above it.
		restore := photos.SetIndexOverfetch(0)
		compareRankings(t, what+", no overfetch", rankAll(t, service, sources, imageSearch), want)
		restore()
		// Search keeps 500 photos by similarity of the 620 and more: the cut
		// is where the index could go wrong.
		if len(want.searches["蓝色"].ids) < 500 || len(want.clusters["red"]) == 0 || want.searches["小票"].texts < 40 {
			t.Fatalf("%s: the library is too small to tell: %d results, %d in the red cluster, %d with text", what,
				len(want.searches["蓝色"].ids), len(want.clusters["red"]), want.searches["小票"].texts)
		}
	}
	compare("synced")
	if reasons := fell.take(); len(reasons) != 0 {
		t.Fatalf("with the index in line, ranking read the Catalog: %v", reasons)
	}

	// Photos the outbox queues are scored from the Catalog beside the index:
	// new ones, and one moved to the trash.
	for i := range 5 {
		importPhoto(t, service, alice, private.ID, "", fmt.Sprintf("late%d.png", i), solidPNG(250, uint8(10+i), 3))
	}
	index(t, service, worker)
	if _, err := service.Trash(context.Background(), alice, imported[3].ID); err != nil {
		t.Fatal(err)
	}
	compare("queued")
	if reasons := fell.take(); len(reasons) != 0 {
		t.Fatalf("with a few photos queued, ranking read the Catalog: %v", reasons)
	}

	// Without its collection the index cannot answer; the Catalog does.
	syncAll(t, service, vectors)
	if err := vectors.DeleteCollection(context.Background(), imagesCollection(t, vectors)); err != nil {
		t.Fatal(err)
	}
	compare("collection lost")
	if reasons := fell.take(); len(reasons) == 0 {
		t.Fatal("ranking without the collection did not say it read the Catalog")
	}
}

// preparedIndex counts how often it is asked to start.
type preparedIndex struct {
	photos.VectorIndex
	mu       sync.Mutex
	prepared int
}

func (p *preparedIndex) Prepare(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prepared++
	return nil
}

func (p *preparedIndex) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.prepared
}

// Requests prepare the index ahead of a search, but not each one of them.
func TestPreparingTheIndexIsSpaced(t *testing.T) {
	index := &preparedIndex{}
	service, err := photos.Open(filepath.Join(t.TempDir(), "photos"), photos.Options{Now: newClock().Now,
		DisableCapacityReserve: true, AI: newFakeEmbedder(), Vectors: index})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	for range 5 {
		service.PrepareVectors()
	}
	for deadline := time.Now().Add(10 * time.Second); index.count() == 0; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the index was not prepared")
		}
	}
	time.Sleep(100 * time.Millisecond)
	if n := index.count(); n != 1 {
		t.Errorf("prepared %d times for five requests at once, want once", n)
	}
}

// failingIndex is a vector index whose searches fail when fail says so for
// the call's number, counting the calls and the requests to start.
type failingIndex struct {
	photos.VectorIndex
	mu       sync.Mutex
	calls    int
	prepared int
	fail     func(call int) bool
}

func (f *failingIndex) call() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail != nil && f.fail(f.calls) {
		return errors.New("the index is broken")
	}
	return nil
}

func (f *failingIndex) Search(ctx context.Context, collection string, query vectorindex.Query) ([]vectorindex.Hit, error) {
	if err := f.call(); err != nil {
		return nil, err
	}
	return f.VectorIndex.Search(ctx, collection, query)
}

func (f *failingIndex) SearchBatch(ctx context.Context, collection string, queries []vectorindex.Query) ([][]vectorindex.Hit, error) {
	if err := f.call(); err != nil {
		return nil, err
	}
	return f.VectorIndex.SearchBatch(ctx, collection, queries)
}

func (f *failingIndex) Prepare(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepared++
	return nil
}

func (f *failingIndex) failWhen(fail func(call int) bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = fail
}

func (f *failingIndex) count() (calls, prepared int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.prepared
}

// smallIndexedLibrary opens a library of three photos synced to a real
// vector index, which ranking reads through a failingIndex.
func smallIndexedLibrary(t *testing.T) (*photos.Service, *failingIndex, *vectorindex.Client, *clocktest.Clock, *fallbacks) {
	t.Helper()
	instance := vectorindextest.Start(t)
	vectors := vectorindex.New(instance.Socket, nil)
	t.Cleanup(func() { _ = vectors.Close() })
	failing := &failingIndex{VectorIndex: vectors}
	clock := newClock()
	fell := &fallbacks{}
	embedder := newFakeEmbedder()
	service, err := photos.Open(filepath.Join(t.TempDir(), "photos"), photos.Options{Now: clock.Now, DisableCapacityReserve: true,
		AI: embedder, Vectors: failing, OnVectorFallback: fell.record})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	private, _ := libraries(t, service, alice)
	for shade := range uint8(3) {
		importPhoto(t, service, alice, private.ID, "", "p.png", encodePNG(shade+1))
	}
	processAll(t, service)
	processAI(t, service, embedder, &fakeGate{open: true})
	syncAll(t, service, vectors)
	return service, failing, vectors, clock, fell
}

func searchCount(t *testing.T, service *photos.Service) int {
	t.Helper()
	page, err := service.Search(context.Background(), alice, photos.SearchRequest{Query: "猫"})
	if err != nil || !page.Semantic {
		t.Fatalf("Search() = %+v, %v", page, err)
	}
	return len(page.Assets)
}

// An index that fails is left alone for a while, longer after each failure
// in a row: searches read the Catalog meanwhile, without waiting for the
// index or starting it.
func TestAFailingIndexIsLeftAloneForAWhile(t *testing.T) {
	service, failing, _, clock, fell := smallIndexedLibrary(t)
	failing.failWhen(func(int) bool { return true })

	if n := searchCount(t, service); n != 3 {
		t.Fatalf("with the index failing, search found %d photos, want 3 from the Catalog", n)
	}
	if calls, _ := failing.count(); calls != 1 {
		t.Fatalf("the failing index was asked %d times, want once", calls)
	}
	if reasons := fell.take(); len(reasons) != 1 || !strings.HasPrefix(reasons[0], "the vector index failed") {
		t.Errorf("fallbacks = %v, want the failure", reasons)
	}
	// Resting: the next search does not ask it, nor does a request start it.
	service.PrepareVectors()
	if n := searchCount(t, service); n != 3 {
		t.Fatalf("while the index rests, search found %d photos", n)
	}
	time.Sleep(100 * time.Millisecond)
	if calls, prepared := failing.count(); calls != 1 || prepared != 0 {
		t.Errorf("while resting the index was asked %d times and prepared %d times, want 1 and 0", calls, prepared)
	}
	// A minute later it is asked again; failing again, it rests two minutes.
	clock.Advance(time.Minute)
	searchCount(t, service)
	clock.Advance(90 * time.Second)
	searchCount(t, service)
	if calls, _ := failing.count(); calls != 2 {
		t.Errorf("after one and two and a half minutes the index was asked %d times in all, want 2", calls)
	}
	// Once it answers, it is asked at every search again.
	failing.failWhen(nil)
	clock.Advance(time.Minute)
	searchCount(t, service)
	searchCount(t, service)
	if calls, _ := failing.count(); calls != 4 {
		t.Errorf("after it recovered the index was asked %d times in all, want 4", calls)
	}
}

// A collection the index lost is made again as soon as a search finds it
// missing, though the gate keeps the sync loop from filling it.
func TestALostCollectionIsMadeAgainAtOnce(t *testing.T) {
	service, _, vectors, clock, fell := smallIndexedLibrary(t)
	lost := imagesCollection(t, vectors)
	if err := vectors.DeleteCollection(context.Background(), lost); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan photos.VectorSyncEvent, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		service.RunVectorSync(ctx, vectors, &fakeGate{open: false}, time.Hour, func(event photos.VectorSyncEvent) { events <- event },
			func(err error) { t.Error(err) })
	}()
	defer func() { cancel(); <-done }()

	if n := searchCount(t, service); n != 3 {
		t.Fatalf("without its collection, search found %d photos, want 3 from the Catalog", n)
	}
	select {
	case event := <-events:
		if event.Kind != "created" || event.Queued != 3 || event.Collection == lost {
			t.Errorf("event = %+v, want a new collection with the three photos queued", event)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the lost collection was not made again")
	}
	if reasons := fell.take(); len(reasons) != 1 || !strings.Contains(reasons[0], "no such collection") {
		t.Errorf("fallbacks = %v, want one for the missing collection", reasons)
	}
	// Filled, it answers searches again once the index has rested.
	syncAll(t, service, vectors)
	clock.Advance(time.Minute)
	if n := searchCount(t, service); n != 3 {
		t.Fatalf("with the collection made again, search found %d photos", n)
	}
	if reasons := fell.take(); len(reasons) != 0 {
		t.Errorf("with the collection made again, ranking read the Catalog: %v", reasons)
	}
}

// A check that fails is spaced like one that succeeds: the searches after it
// are not checked, nor is the failure logged again, until the interval ends.
func TestFailedChecksAreSpaced(t *testing.T) {
	service, failing, _, clock, _ := smallIndexedLibrary(t)
	// The search's own question succeeds; the check's, which follows it,
	// fails.
	failing.failWhen(func(call int) bool { return call == 2 })
	ctx, cancel := context.WithCancel(context.Background())
	checks := make(chan photos.VectorCheck, 4)
	failures := make(chan error, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		service.RunVectorCheck(ctx, time.Hour, func(c photos.VectorCheck) { checks <- c }, func(err error) { failures <- err })
	}()
	defer func() { cancel(); <-done }()
	for deadline := time.Now().Add(10 * time.Second); !service.CheckRunning(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the check worker did not start")
		}
	}

	searchCount(t, service)
	select {
	case <-failures:
	case c := <-checks:
		t.Fatalf("the check succeeded: %+v", c)
	case <-time.After(30 * time.Second):
		t.Fatal("the search was not checked")
	}
	// After the index has rested, a search reads it but is not checked.
	clock.Advance(time.Minute)
	searchCount(t, service)
	select {
	case err := <-failures:
		t.Errorf("a search within the hour was checked again: %v", err)
	case c := <-checks:
		t.Errorf("a search within the hour was checked again: %+v", c)
	case <-time.After(500 * time.Millisecond):
	}
	if calls, _ := failing.count(); calls != 3 {
		t.Errorf("the index was asked %d times, want 3: two searches and one check", calls)
	}
}
