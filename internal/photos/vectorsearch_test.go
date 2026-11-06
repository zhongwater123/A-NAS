package photos_test

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

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
// detector, AI labels and more photos than search keeps by similarity, some
// of them in two libraries.
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
	service, err := photos.Open(filepath.Join(t.TempDir(), "photos"), photos.Options{Now: newClock().Now,
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
	return service, worker, vectors, fell, private, imported
}

// everything is every page of a search, with what the first page says.
type everything struct {
	ids     []string
	closest int
	objects *photos.ObjectsFound
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
			all.closest, all.objects = page.Closest, page.Objects
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
	for _, query := range []string{"红", "绿色的手机", "手机", "蓝色", "猫", "手机壳", "傍晚在海边散步时用手机拍下的日落"} {
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
		if !slices.Equal(g.ids, w.ids) || g.closest != w.closest || fmt.Sprint(g.objects) != fmt.Sprint(w.objects) {
			t.Errorf("%s: search %q = %d results, closest %d, objects %v; want %d, %d, %v (first difference at %d)",
				what, query, len(g.ids), g.closest, g.objects, len(w.ids), w.closest, w.objects, firstDifference(g.ids, w.ids))
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
		if len(want.searches["蓝色"].ids) < 500 || len(want.clusters["red"]) == 0 {
			t.Fatalf("%s: the library is too small to tell: %d results, %d in the red cluster", what,
				len(want.searches["蓝色"].ids), len(want.clusters["red"]))
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
