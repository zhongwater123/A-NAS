package photos_test

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/vectorindex"
)

// searchFixture is a montage fixture whose embedder encodes search texts,
// with videos of Alice's in two projects and one of Bob's, their windows
// analysed and each window's vector made its own.
type searchFixture struct {
	montageFixture
	// trip and beach are Alice's first project, the second in what everyone
	// shares; hike is her second project, and garden Bob's.
	trip, beach, hike, garden                         photos.Asset
	tripGrant, hikeGrant, gardenGrant                 photos.Grant
	tripObject, beachObject, hikeObject, gardenObject string
}

func newSearchFixture(t *testing.T) *searchFixture {
	t.Helper()
	f := &searchFixture{montageFixture: newMontageFixture(t)}
	f.search, f.fell = true, &fallbacks{}
	f.restart(t)
	ctx := context.Background()
	alicePrivate, shared := libraries(t, f.service, namedAlice)
	bobPrivate, _ := libraries(t, f.service, namedBob)
	f.trip = importPhoto(t, f.service, namedAlice, alicePrivate.ID, "", "trip.mp4", mp4Bytes(1))
	f.beach = importPhoto(t, f.service, namedAlice, shared.ID, "", "beach.mp4", mp4Bytes(2))
	f.hike = importPhoto(t, f.service, namedAlice, alicePrivate.ID, "", "hike.mp4", mp4Bytes(3))
	f.garden = importPhoto(t, f.service, namedBob, bobPrivate.ID, "", "garden.mp4", mp4Bytes(4))
	var err error
	f.tripGrant, err = f.service.RegisterGrant(ctx, granting(namedAlice), "project:trip", []string{f.trip.ID, f.beach.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.AnalyzeGrant(ctx, granting(namedAlice), f.tripGrant.ID, []string{f.trip.ID, f.beach.ID}, false); err != nil {
		t.Fatal(err)
	}
	f.hikeGrant, f.hikeObject = f.grant(t, namedAlice, "project:hike", f.hike)
	f.gardenGrant, f.gardenObject = f.grant(t, namedBob, "project:garden", f.garden)
	looked, err := f.service.LookupGrant(ctx, granting(namedAlice), f.tripGrant.ID, []string{f.trip.ID, f.beach.ID})
	if err != nil {
		t.Fatal(err)
	}
	f.tripObject, f.beachObject = looked[0].ObjectID, looked[1].ObjectID
	processAI(t, f.service, f.embedder, f.gate)
	f.spreadWindows(t)
	return f
}

// spreadWindows gives every window a vector of its own, as real frames
// would; the fixture's frames are all alike. The first ten windows of the
// trip are alike, so that ranks tie.
func (f *searchFixture) spreadWindows(t *testing.T) {
	t.Helper()
	db := catalog(t, f.root)
	rows, err := db.Query("SELECT object_id, idx FROM video_windows")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	type window struct {
		object string
		idx    int
	}
	var windows []window
	for rows.Next() {
		var w window
		if err := rows.Scan(&w.object, &w.idx); err != nil {
			t.Fatal(err)
		}
		windows = append(windows, w)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatal(err)
	}
	for _, w := range windows {
		key := fmt.Sprintf("%s#%d", w.object, w.idx)
		if w.object == f.tripObject && w.idx < 10 {
			key = "tie"
		}
		if _, err := db.Exec("UPDATE video_windows SET vector = ? WHERE object_id = ? AND idx = ?", unitVector(key), w.object, w.idx); err != nil {
			t.Fatal(err)
		}
	}
}

// unitVector is a normalised 4-dimension vector, encoded as the Catalog
// keeps it, that only key makes.
func unitVector(key string) []byte {
	sum := sha256.Sum256([]byte(key))
	vector := make([]float32, 4)
	var norm float64
	for i := range vector {
		vector[i] = float32(sum[i])/127.5 - 1
		norm += float64(vector[i]) * float64(vector[i])
	}
	return encodeFloats(vector, float32(math.Sqrt(norm)))
}

// queryVector is the vector the fake embedder gives text, normalised and
// encoded: of all window vectors, the one most like the text.
func queryVector(text string) []byte {
	sum := sha256.Sum256([]byte(text))
	vector := make([]float32, 4)
	var norm float64
	for i := range vector {
		vector[i] = float32(sum[i]) / 255
		norm += float64(vector[i]) * float64(vector[i])
	}
	return encodeFloats(vector, float32(math.Sqrt(norm)))
}

func encodeFloats(vector []float32, divisor float32) []byte {
	encoded := make([]byte, 4*len(vector))
	for i, value := range vector {
		binary.LittleEndian.PutUint32(encoded[4*i:], math.Float32bits(value/divisor))
	}
	return encoded
}

var searchTexts = []string{"sunset over the sea", "a dog running", "sunset over the sea", "城市夜景", "mountains", "birthday cake"}

func (f *searchFixture) searchWindows(t *testing.T, p photos.Principal, grant photos.Grant, k int) [][]photos.WindowMatch {
	t.Helper()
	matches, err := f.service.SearchWindows(context.Background(), granting(p), grant.ID, searchTexts, k)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != len(searchTexts) {
		t.Fatalf("SearchWindows() answered %d lists for %d texts", len(matches), len(searchTexts))
	}
	return matches
}

// The search ranks the windows of the grant's videos for each text, the
// same text alike, and answers no more than it is asked for.
func TestWindowSearchRanksTheGrantsWindows(t *testing.T) {
	f := newSearchFixture(t)
	for _, k := range []int{1, 7, 50, photos.MaxWindowMatches} {
		matches := f.searchWindows(t, namedAlice, f.tripGrant, k)
		for i, list := range matches {
			// The trip and the beach have 70 windows.
			if len(list) != min(k, 70) {
				t.Fatalf("k=%d: %q found %d windows", k, searchTexts[i], len(list))
			}
			for n, match := range list {
				if match.AssetID != f.trip.ID && match.AssetID != f.beach.ID || match.Window < 0 || match.Window >= 35 {
					t.Fatalf("k=%d: %q found %+v", k, searchTexts[i], match)
				}
				if n > 0 && match.Score > list[n-1].Score {
					t.Fatalf("k=%d: %q is not most alike first: %+v", k, searchTexts[i], list)
				}
			}
		}
		if !reflect.DeepEqual(matches[0], matches[2]) {
			t.Fatalf("k=%d: the same text found %+v and %+v", k, matches[0], matches[2])
		}
	}
	// A trashed video is no longer searched.
	if _, err := f.service.Trash(context.Background(), namedAlice, f.beach.ID); err != nil {
		t.Fatal(err)
	}
	for _, list := range f.searchWindows(t, namedAlice, f.tripGrant, photos.MaxWindowMatches) {
		if len(list) != 35 {
			t.Fatalf("after trashing the beach, found %d windows, want the trip's 35", len(list))
		}
		for _, match := range list {
			if match.AssetID != f.trip.ID {
				t.Fatalf("after trashing the beach, found %+v", match)
			}
		}
	}
}

// A grant whose videos have no windows yet finds nothing, without asking
// the Worker; requests out of bounds are refused.
func TestWindowSearchBeforeAnyWindowsAndOutOfBounds(t *testing.T) {
	ctx := context.Background()
	f := newMontageFixture(t)
	f.search = true
	f.restart(t)
	private, _ := libraries(t, f.service, namedAlice)
	video := importPhoto(t, f.service, namedAlice, private.ID, "", "trip.mp4", mp4Bytes(1))
	grant, _ := f.grant(t, namedAlice, "project:1", video)
	f.embedder.unavailable = true
	matches, err := f.service.SearchWindows(ctx, granting(namedAlice), grant.ID, []string{"sea", "sky"}, 5)
	if err != nil || len(matches) != 2 || matches[0] == nil || len(matches[0])+len(matches[1]) != 0 {
		t.Fatalf("SearchWindows() before any windows = %+v, %v", matches, err)
	}
	for name, request := range map[string]struct {
		texts []string
		k     int
	}{
		"no texts":        {nil, 5},
		"many texts":      {slices.Repeat([]string{"sea"}, photos.MaxWindowSearchTexts+1), 5},
		"blank text":      {[]string{"sea", "  "}, 5},
		"long text":       {[]string{strings.Repeat("海", photos.MaxQueryRunes+1)}, 5},
		"no k":            {[]string{"sea"}, 0},
		"too many k":      {[]string{"sea"}, photos.MaxWindowMatches + 1},
		"too many in all": {slices.Repeat([]string{"sea"}, photos.MaxWindowSearchHits/photos.MaxWindowMatches+1), photos.MaxWindowMatches},
		"all at most":     {slices.Repeat([]string{"sea"}, photos.MaxWindowSearchHits/photos.MaxWindowMatches), photos.MaxWindowMatches},
		"negative k":      {[]string{"sea"}, -1},
		"exactly max":     {slices.Repeat([]string{"sea"}, photos.MaxWindowSearchTexts), photos.MaxWindowSearchHits / photos.MaxWindowSearchTexts},
	} {
		_, err := f.service.SearchWindows(ctx, granting(namedAlice), grant.ID, request.texts, request.k)
		if want := name != "exactly max" && name != "all at most"; errors.Is(err, photos.ErrInvalidQuery) != want {
			t.Errorf("%s: SearchWindows() = %v", name, err)
		}
	}
}

// The index and the Catalog answer alike: with every video in the index,
// with one queued for it, and when the index is asked for no more than it
// must give.
func TestWindowSearchReadsTheIndexAsTheCatalogDoes(t *testing.T) {
	f := newSearchFixture(t)
	w := newWindowIndex(t, &f.montageFixture)
	f.vectors = w.index
	photos.UseVectorIndex(f.service, w.index)
	compare := func(stage string) {
		t.Helper()
		for _, k := range []int{1, 5, 12, 50, photos.MaxWindowMatches} {
			for _, grant := range []photos.Grant{f.tripGrant, f.hikeGrant} {
				photos.UseVectorIndex(f.service, w.index)
				indexed := f.searchWindows(t, namedAlice, grant, k)
				if searched := w.index.searched(); searched == 0 {
					t.Fatalf("%s, k=%d: the index was not asked; fell back for %v", stage, k, f.fell.take())
				}
				if fell := f.fell.take(); len(fell) > 0 {
					t.Fatalf("%s, k=%d: fell back for %v", stage, k, fell)
				}
				photos.UseVectorIndex(f.service, nil)
				if scanned := f.searchWindows(t, namedAlice, grant, k); !reflect.DeepEqual(indexed, scanned) {
					t.Fatalf("%s, k=%d: the index found\n%+v\nthe Catalog\n%+v", stage, k, indexed, scanned)
				}
			}
		}
		photos.UseVectorIndex(f.service, w.index)
	}
	w.sync(t)
	compare("all in the index")
	defer photos.SetIndexOverfetch(0)()
	compare("no overfetch")
	// The beach's windows change and wait for the index: they are scored
	// from the Catalog beside the index's answer.
	db := catalog(t, f.root)
	if _, err := db.Exec("UPDATE video_windows SET vector = ? WHERE object_id = ? AND idx < 5", queryVector("mountains"), f.beachObject); err != nil {
		t.Fatal(err)
	}
	compare("one video queued")
	matches := f.searchWindows(t, namedAlice, f.tripGrant, 5)
	for _, match := range matches[4] {
		if match.AssetID != f.beach.ID || match.Window >= 5 {
			t.Fatalf("the queued windows made like the text rank as %+v", matches[4])
		}
	}
}

// With the index stopped, a search reads the Catalog and finds the same,
// then leaves the index alone for a while.
func TestWindowSearchWorksWithTheIndexStopped(t *testing.T) {
	f := newSearchFixture(t)
	newWindowIndex(t, &f.montageFixture).sync(t)
	photos.UseVectorIndex(f.service, nil)
	want := f.searchWindows(t, namedAlice, f.tripGrant, 10)
	// The collection is in the index; then the instance stops.
	stopped := vectorindex.New(filepath.Join(t.TempDir(), "stopped.sock"), nil)
	t.Cleanup(func() { _ = stopped.Close() })
	photos.UseVectorIndex(f.service, stopped)
	if got := f.searchWindows(t, namedAlice, f.tripGrant, 10); !reflect.DeepEqual(got, want) {
		t.Fatalf("with the index stopped, found\n%+v\nwant\n%+v", got, want)
	}
	if fell := f.fell.take(); len(fell) != 1 || !strings.HasPrefix(fell[0], "the vector index failed") {
		t.Fatalf("fell back for %v", fell)
	}
	if got := f.searchWindows(t, namedAlice, f.tripGrant, 10); !reflect.DeepEqual(got, want) {
		t.Fatalf("again with the index stopped, found %+v", got)
	}
	if fell := f.fell.take(); len(fell) != 1 || fell[0] != "the vector index failed recently<nil>" {
		t.Fatalf("fell back again for %v", fell)
	}
}

// After the model changes, a search refuses with its own error until every
// video of the grant has windows of the new model: an empty or partial
// answer would hide videos. Without the Worker it says so.
func TestWindowSearchRefusesWindowsOfAnotherModel(t *testing.T) {
	ctx := context.Background()
	f := newSearchFixture(t)
	f.embedder.unavailable = true
	if _, err := f.service.SearchWindows(ctx, granting(namedAlice), f.tripGrant.ID, []string{"sea"}, 5); !errors.Is(err, photos.ErrAIUnavailable) {
		t.Fatalf("SearchWindows() without the Worker = %v", err)
	}
	f.embedder.unavailable = false
	f.restart(t)
	f.embedder.model = "fake-2"
	f.clock.Advance(time.Minute)
	if _, err := f.service.SearchWindows(ctx, granting(namedAlice), f.tripGrant.ID, []string{"sea"}, 5); !errors.Is(err, photos.ErrWindowModelMismatch) {
		t.Fatalf("SearchWindows() with the windows of the model before = %v", err)
	}
	// One chunk of one video made again: the grant still has older windows.
	if worked, err := f.service.ProcessAIJob(ctx, f.embedder, f.gate); !worked || err != nil {
		t.Fatalf("ProcessAIJob() = %v, %v", worked, err)
	}
	if _, err := f.service.SearchWindows(ctx, granting(namedAlice), f.tripGrant.ID, []string{"sea"}, 5); !errors.Is(err, photos.ErrWindowModelMismatch) {
		t.Fatalf("SearchWindows() with windows of two models = %v", err)
	}
	processAI(t, f.service, f.embedder, f.gate)
	matches, err := f.service.SearchWindows(ctx, granting(namedAlice), f.tripGrant.ID, []string{"sea"}, 5)
	if err != nil || len(matches[0]) != 5 {
		t.Fatalf("SearchWindows() once the windows are made again = %+v, %v", matches, err)
	}
}

// The alias marks the collection a search may read: the one sync has
// filled. A new model's collection is filled beside the old one, and until
// the alias moves to it a search reads the Catalog, not the old collection,
// whose vectors are of another model than its texts. The old one stays, and
// the model before taken back takes it up again.
func TestTheWindowsAliasMovesOnceTheNewCollectionIsFull(t *testing.T) {
	ctx := context.Background()
	f := newSearchFixture(t)
	w := newWindowIndex(t, &f.montageFixture)
	f.vectors = w.index
	photos.UseVectorIndex(f.service, w.index)
	var events []photos.VectorSyncEvent
	sync := func() {
		t.Helper()
		for range 100 {
			worked, err := f.service.SyncWindows(ctx, w.index, func(event photos.VectorSyncEvent) { events = append(events, event) })
			if err != nil {
				t.Fatal(err)
			}
			if !worked {
				return
			}
		}
		t.Fatal("SyncWindows() never caught up")
	}
	aliased := func() string {
		t.Helper()
		name, err := w.index.Alias(ctx, "video_windows_v1")
		if err != nil {
			t.Fatal(err)
		}
		return name
	}
	collections := func() []string {
		t.Helper()
		names, err := w.index.Collections(ctx)
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(names)
		return names
	}
	readsIndex := func(stage string) {
		t.Helper()
		f.searchWindows(t, namedAlice, f.tripGrant, 5)
		if searched, fell := w.index.searched(), f.fell.take(); searched == 0 || len(fell) > 0 {
			t.Fatalf("%s: %d index searches; fell back for %v", stage, searched, fell)
		}
	}
	sync()
	first := aliased()
	if !strings.HasPrefix(first, "video_windows_v1_") || !slices.Equal(collections(), []string{first}) {
		t.Fatalf("alias = %q, collections = %v", first, collections())
	}
	readsIndex("first model")

	// A new model: videos are made again one by one, until a grant has
	// all of its videos in the new model, and written into a new collection
	// while the others wait for theirs.
	f.restart(t)
	photos.UseVectorIndex(f.service, w.index)
	f.embedder.model = "fake-2"
	f.clock.Advance(time.Minute)
	db := catalog(t, f.root)
	redone := func(object string) bool {
		t.Helper()
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM video_windows WHERE object_id = ? AND identity LIKE 'fake-2|%'
AND object_id NOT IN (SELECT object_id FROM jobs WHERE derivation = 'montage-windows/v1')`, object).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count == 35
	}
	type project struct {
		owner   photos.Principal
		grant   photos.Grant
		objects []string
	}
	projects := []project{{namedAlice, f.tripGrant, []string{f.tripObject, f.beachObject}},
		{namedAlice, f.hikeGrant, []string{f.hikeObject}}, {namedBob, f.gardenGrant, []string{f.gardenObject}}}
	var whole *project
	for whole == nil {
		if worked, err := f.service.ProcessAIJob(ctx, f.embedder, f.gate); !worked || err != nil {
			t.Fatalf("ProcessAIJob() = %v, %v", worked, err)
		}
		for i := range projects {
			if !slices.ContainsFunc(projects[i].objects, func(object string) bool { return !redone(object) }) {
				whole = &projects[i]
				break
			}
		}
	}
	sync()
	names := collections()
	if len(names) != 2 || aliased() != first {
		t.Fatalf("while the new collection fills: alias = %q, collections = %v", aliased(), names)
	}
	second := names[0]
	if second == first {
		second = names[1]
	}
	// The project is whole in the new model, yet its collection is not
	// full: the search reads the Catalog.
	if _, err := f.service.SearchWindows(ctx, granting(whole.owner), whole.grant.ID, []string{"sea"}, 5); err != nil {
		t.Fatal(err)
	}
	if fell := f.fell.take(); len(fell) != 1 || fell[0] != "the vector index is still filling the windows collection<nil>" {
		t.Fatalf("while the new collection fills, fell back for %v", fell)
	}

	// Every video made again and written: the alias moves.
	processAI(t, f.service, f.embedder, f.gate)
	sync()
	if aliased() != second || !slices.Equal(collections(), names) {
		t.Fatalf("once full: alias = %q, collections = %v; want %s of %v", aliased(), collections(), second, names)
	}
	if last := events[len(events)-1]; last.Kind != "switched" || last.Collection != second {
		t.Fatalf("the last sync event = %+v", last)
	}
	readsIndex("second model")

	// The model before taken back: its collection is taken up again,
	// rewritten, and named by the alias once full.
	f.restart(t)
	photos.UseVectorIndex(f.service, w.index)
	f.embedder.model = "fake-1"
	f.clock.Advance(time.Minute)
	processAI(t, f.service, f.embedder, f.gate)
	f.spreadWindows(t)
	events = nil
	sync()
	if aliased() != first || !slices.Equal(collections(), names) {
		t.Fatalf("after going back: alias = %q, collections = %v; want %s of %v", aliased(), collections(), first, names)
	}
	if events[0].Kind != "reused" || events[0].Collection != first || events[len(events)-1].Kind != "switched" {
		t.Fatalf("sync events going back = %+v", events)
	}
	readsIndex("model taken back")
	photos.UseVectorIndex(f.service, nil)
	scanned := f.searchWindows(t, namedAlice, f.tripGrant, 5)
	photos.UseVectorIndex(f.service, w.index)
	if indexed := f.searchWindows(t, namedAlice, f.tripGrant, 5); !reflect.DeepEqual(indexed, scanned) {
		t.Fatalf("after going back, the index found %+v, the Catalog %+v", indexed, scanned)
	}
}
