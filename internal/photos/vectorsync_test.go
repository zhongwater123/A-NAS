package photos_test

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/vectorindex"
	"github.com/zhongwater123/A-NAS/internal/vectorindex/vectorindextest"
)

// outboxVersion is an object's version in the vector outbox, 0 when it is
// not queued.
func outboxVersion(t *testing.T, db *sql.DB, object string) int {
	t.Helper()
	var version int
	err := db.QueryRow("SELECT version FROM vector_outbox WHERE object_id = ?", object).Scan(&version)
	if err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	return version
}

func objectOf(t *testing.T, db *sql.DB, assetID string) string {
	t.Helper()
	var object string
	if err := db.QueryRow("SELECT object_id FROM assets WHERE id = ?", assetID).Scan(&object); err != nil {
		t.Fatal(err)
	}
	return object
}

// Every change to a vector or to the libraries holding a photo of an object
// queues the object in the same transaction, whichever path makes it.
func TestVectorOutboxFollowsTheCatalog(t *testing.T) {
	service, clock, root := newService(t)
	db := catalogDB(t, root)
	ctx := context.Background()
	private, shared := libraries(t, service, alice)
	embedder, gate := newFakeEmbedder(), &fakeGate{open: true}
	asset := importPhoto(t, service, alice, private.ID, "", "a.png", encodePNG(1))
	object := objectOf(t, db, asset.ID)
	drain := func() {
		t.Helper()
		if _, err := db.Exec("DELETE FROM vector_outbox"); err != nil {
			t.Fatal(err)
		}
	}
	queued := func(step string) {
		t.Helper()
		if outboxVersion(t, db, object) == 0 {
			t.Errorf("%s did not queue the object", step)
		}
		drain()
	}
	queued("an import")

	processAll(t, service)
	processAI(t, service, embedder, gate)
	queued("storing its vector")

	// A new model, which comes with a release and its restart, stores the
	// vector again over the old one, an upsert; the queue must not make that
	// fail while the object is still queued.
	if _, err := db.Exec("INSERT INTO vector_outbox(object_id) VALUES(?)", object); err != nil {
		t.Fatal(err)
	}
	embedder.model = "fake-2"
	_ = service.Close()
	service = openService(t, root, clock.Now)
	if processed := processAI(t, service, embedder, gate); processed != 1 {
		t.Fatalf("re-embedding processed %d jobs, want 1", processed)
	}
	if version := outboxVersion(t, db, object); version < 2 {
		t.Errorf("re-embedding a queued object left version %d", version)
	}
	drain()

	if _, err := service.Trash(ctx, alice, asset.ID); err != nil {
		t.Fatal(err)
	}
	queued("trashing")
	if _, err := service.Restore(ctx, alice, asset.ID); err != nil {
		t.Fatal(err)
	}
	queued("restoring")
	if _, err := service.Copy(ctx, alice, asset.ID, shared.ID, ""); err != nil {
		t.Fatal(err)
	}
	queued("copying to another library")

	// Renaming changes neither.
	if _, err := service.Update(ctx, alice, asset.ID, photos.AssetUpdate{Name: new("renamed.png")}); err != nil {
		t.Fatal(err)
	}
	if outboxVersion(t, db, object) != 0 {
		t.Error("renaming queued the object")
	}

	// Deleting the last photo of an object deletes its vector by cascade;
	// the queued row must outlive the object for the index to drop it.
	page, err := service.Timeline(ctx, alice, shared.ID, "", 10)
	if err != nil || len(page.Assets) != 1 {
		t.Fatalf("shared library = %+v, %v", page.Assets, err)
	}
	for _, id := range []string{asset.ID, page.Assets[0].ID} {
		if _, err := service.Trash(ctx, alice, id); err != nil {
			t.Fatal(err)
		}
		if err := service.Purge(ctx, alice, id); err != nil {
			t.Fatal(err)
		}
	}
	var vectors int
	if err := db.QueryRow("SELECT COUNT(*) FROM embeddings WHERE object_id = ?", object).Scan(&vectors); err != nil || vectors != 0 {
		t.Fatalf("vectors after purge = %d, %v", vectors, err)
	}
	queued("purging")
}

// syncAll brings the index fully in line with the Catalog.
func syncAll(t *testing.T, service *photos.Service, index photos.VectorIndex) []photos.VectorSyncEvent {
	t.Helper()
	var events []photos.VectorSyncEvent
	for range 100 {
		worked, err := service.SyncVectors(context.Background(), index, func(event photos.VectorSyncEvent) { events = append(events, event) })
		if err != nil {
			t.Fatalf("SyncVectors() error = %v", err)
		}
		if !worked {
			return events
		}
	}
	t.Fatal("SyncVectors() never caught up")
	return nil
}

// imagesCollection is the one collection of image vectors in the index.
func imagesCollection(t *testing.T, index *vectorindex.Client) string {
	t.Helper()
	names, err := index.Collections(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var images []string
	for _, name := range names {
		if strings.HasPrefix(name, "images_v1_") {
			images = append(images, name)
		}
	}
	if len(images) != 1 {
		t.Fatalf("collections = %v, want one of image vectors", names)
	}
	return images[0]
}

// inLibraries lists, sorted, the objects the index finds within libraries.
func inLibraries(t *testing.T, index *vectorindex.Client, libraries ...string) []string {
	t.Helper()
	hits, err := index.Search(context.Background(), imagesCollection(t, index), vectorindex.Query{Vector: []float32{0.5, 0.5, 0.5, 0.5},
		Scope: vectorindex.Scope{Field: "libraries", Values: libraries}, Limit: 100, Exact: true})
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, len(hits))
	for i, hit := range hits {
		keys[i] = hit.Key
	}
	slices.Sort(keys)
	return keys
}

func sorted(values ...string) []string {
	values = slices.Clone(values)
	slices.Sort(values)
	return values
}

func indexedService(t *testing.T) (*photos.Service, *fakeEmbedder, *vectorindex.Client, *sql.DB, string) {
	t.Helper()
	instance := vectorindextest.Start(t)
	index := vectorindex.New(instance.Socket, nil)
	t.Cleanup(func() { _ = index.Close() })
	service, _, root := newService(t)
	return service, newFakeEmbedder(), index, catalogDB(t, root), root
}

// restarted closes service and opens its root again, as a release's install
// restarts the photo service.
func restarted(t *testing.T, service *photos.Service, root string) *photos.Service {
	t.Helper()
	_ = service.Close()
	return openService(t, root, newClock().Now)
}

func TestVectorIndexFollowsTheCatalog(t *testing.T) {
	service, embedder, index, db, _ := indexedService(t)
	ctx := context.Background()
	private, shared := libraries(t, service, alice)
	bobs, _ := libraries(t, service, bob)
	a := importPhoto(t, service, alice, private.ID, "", "a.png", encodePNG(1))
	b := importPhoto(t, service, alice, private.ID, "", "b.png", encodePNG(2))
	c := importPhoto(t, service, alice, shared.ID, "", "c.png", encodePNG(3))
	d := importPhoto(t, service, bob, bobs.ID, "", "d.png", encodePNG(4))
	processAll(t, service)
	processAI(t, service, embedder, &fakeGate{open: true})
	objectA, objectB, objectC, objectD := objectOf(t, db, a.ID), objectOf(t, db, b.ID), objectOf(t, db, c.ID), objectOf(t, db, d.ID)

	events := syncAll(t, service, index)
	if len(events) == 0 || events[0].Kind != "created" || events[0].Queued != 4 {
		t.Fatalf("first sync events = %+v, want a new collection with every vector queued", events)
	}
	if got := inLibraries(t, index, private.ID); !slices.Equal(got, sorted(objectA, objectB)) {
		t.Errorf("alice's library = %v, want a and b", got)
	}
	if got := inLibraries(t, index, private.ID, shared.ID); !slices.Equal(got, sorted(objectA, objectB, objectC)) {
		t.Errorf("alice's scope = %v, want a, b and c; never bob's d", got)
	}
	if got := inLibraries(t, index, bobs.ID); !slices.Equal(got, []string{objectD}) {
		t.Errorf("bob's library = %v, want d", got)
	}

	if _, err := service.Trash(ctx, alice, a.ID); err != nil {
		t.Fatal(err)
	}
	syncAll(t, service, index)
	if got := inLibraries(t, index, private.ID); !slices.Equal(got, []string{objectB}) {
		t.Errorf("after trashing a, alice's library = %v", got)
	}
	if _, err := service.Restore(ctx, alice, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Copy(ctx, alice, b.ID, shared.ID, ""); err != nil {
		t.Fatal(err)
	}
	syncAll(t, service, index)
	if got := inLibraries(t, index, shared.ID); !slices.Equal(got, sorted(objectB, objectC)) {
		t.Errorf("after copying b, the shared library = %v", got)
	}
	if got := inLibraries(t, index, private.ID); !slices.Equal(got, sorted(objectA, objectB)) {
		t.Errorf("after restoring a, alice's library = %v", got)
	}

	if _, err := service.Trash(ctx, bob, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.Purge(ctx, bob, d.ID); err != nil {
		t.Fatal(err)
	}
	syncAll(t, service, index)
	if got := inLibraries(t, index, bobs.ID); len(got) != 0 {
		t.Errorf("after purging d, bob's library = %v", got)
	}
	var queued int
	if err := db.QueryRow("SELECT COUNT(*) FROM vector_outbox").Scan(&queued); err != nil || queued != 0 {
		t.Errorf("outbox after sync = %d, %v", queued, err)
	}
}

// An instance that lost its storage, as after a version change, is rebuilt
// from the Catalog once reconciliation finds out; so is one whose points went
// missing.
func TestVectorIndexIsRebuiltFromTheCatalog(t *testing.T) {
	service, embedder, index, db, _ := indexedService(t)
	ctx := context.Background()
	private, _ := libraries(t, service, alice)
	a := importPhoto(t, service, alice, private.ID, "", "a.png", encodePNG(1))
	b := importPhoto(t, service, alice, private.ID, "", "b.png", encodePNG(2))
	processAll(t, service)
	processAI(t, service, embedder, &fakeGate{open: true})
	syncAll(t, service, index)
	first := imagesCollection(t, index)
	all := sorted(objectOf(t, db, a.ID), objectOf(t, db, b.ID))

	if err := index.DeleteCollection(ctx, first); err != nil {
		t.Fatal(err)
	}
	if events := syncAll(t, service, index); len(events) != 0 {
		t.Fatalf("with nothing queued a sync round did %+v", events)
	}
	var events []photos.VectorSyncEvent
	if err := service.ReconcileVectors(ctx, index, func(event photos.VectorSyncEvent) { events = append(events, event) }); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != "created" || events[0].Queued != 2 {
		t.Fatalf("reconciling a lost index = %+v, want a new collection with both vectors queued", events)
	}
	syncAll(t, service, index)
	if second := imagesCollection(t, index); second == first {
		t.Error("the lost collection's name came back")
	}
	if got := inLibraries(t, index, private.ID); !slices.Equal(got, all) {
		t.Errorf("rebuilt index = %v, want %v", got, all)
	}

	if err := index.Delete(ctx, imagesCollection(t, index), all[:1]); err != nil {
		t.Fatal(err)
	}
	events = nil
	if err := service.ReconcileVectors(ctx, index, func(event photos.VectorSyncEvent) { events = append(events, event) }); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != "reconciled" || events[0].Queued != 1 {
		t.Fatalf("reconciliation = %+v, want the lost point queued", events)
	}
	syncAll(t, service, index)
	if got := inLibraries(t, index, private.ID); !slices.Equal(got, all) {
		t.Errorf("reconciled index = %v, want %v", got, all)
	}
}

// With nothing queued a sync round leaves the index alone, so that its
// instance can stop while the library does not change.
func TestAnIdleSyncLeavesTheIndexAlone(t *testing.T) {
	instance := vectorindextest.Start(t)
	index := vectorindex.New(instance.Socket, nil)
	defer func() { _ = index.Close() }()
	service, _, _ := newService(t)
	private, _ := libraries(t, service, alice)
	importPhoto(t, service, alice, private.ID, "", "a.png", encodePNG(1))
	processAll(t, service)
	processAI(t, service, newFakeEmbedder(), &fakeGate{open: true})
	syncAll(t, service, index)
	_ = index.Close()
	for deadline := time.Now().Add(5 * time.Second); instance.Connections() != 0; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the closed client's connection stayed open")
		}
	}
	if worked, err := service.SyncVectors(context.Background(), index, nil); worked || err != nil {
		t.Fatalf("idle SyncVectors() = %v, %v", worked, err)
	}
	time.Sleep(100 * time.Millisecond)
	if open := instance.Connections(); open != 0 {
		t.Errorf("an idle sync round opened %d connections", open)
	}
}

// Another model's vectors go to a new collection, and the old one goes.
func TestANewModelGetsANewCollection(t *testing.T) {
	service, embedder, index, _, root := indexedService(t)
	private, _ := libraries(t, service, alice)
	importPhoto(t, service, alice, private.ID, "", "a.png", encodePNG(1))
	processAll(t, service)
	gate := &fakeGate{open: true}
	processAI(t, service, embedder, gate)
	syncAll(t, service, index)
	first := imagesCollection(t, index)

	embedder.model = "fake-2"
	service = restarted(t, service, root)
	processAI(t, service, embedder, gate)
	syncAll(t, service, index)
	if second := imagesCollection(t, index); second == first {
		t.Error("the new model's vectors went to the old collection")
	}
	if got := inLibraries(t, index, private.ID); len(got) != 1 {
		t.Errorf("index after the new model = %v, want the one photo", got)
	}
}

// Without an index the queue would only grow; a later index starts anew.
func TestForgettingTheVectorIndex(t *testing.T) {
	service, _, root := newService(t)
	db := catalogDB(t, root)
	private, _ := libraries(t, service, alice)
	importPhoto(t, service, alice, private.ID, "", "a.png", encodePNG(1))
	if _, err := db.Exec("INSERT INTO vector_index_state VALUES('images', 'images_v1_x', 'm', '')"); err != nil {
		t.Fatal(err)
	}
	if err := service.ForgetVectorIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	var queued, state int
	if err := db.QueryRow("SELECT (SELECT COUNT(*) FROM vector_outbox), (SELECT COUNT(*) FROM vector_index_state)").Scan(&queued, &state); err != nil ||
		queued != 0 || state != 0 {
		t.Fatalf("after forgetting: %d queued, %d state rows, %v", queued, state, err)
	}
}

// A search's first page is compared with the index in the background; later
// pages are not.
func TestShadowComparesFirstPagesOfSearches(t *testing.T) {
	instance := vectorindextest.Start(t)
	index := vectorindex.New(instance.Socket, nil)
	t.Cleanup(func() { _ = index.Close() })
	embedder := newFakeEmbedder()
	service, err := photos.Open(t.TempDir()+"/photos", photos.Options{Now: newClock().Now, DisableCapacityReserve: true, AI: embedder})
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
	syncAll(t, service, index)

	ctx, cancel := context.WithCancel(context.Background())
	comparisons := make(chan photos.ShadowComparison, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		service.RunVectorShadow(ctx, index, index.Warm, func(c photos.ShadowComparison) { comparisons <- c }, func(err error) { t.Error(err) })
	}()
	defer func() { cancel(); <-done }()

	var comparison photos.ShadowComparison
	var next string
	deadline := time.After(30 * time.Second)
	for compared := false; !compared; {
		page, err := service.Search(context.Background(), alice, photos.SearchRequest{Query: "猫", Limit: 1})
		if err != nil || !page.Semantic {
			t.Fatalf("Search() = %+v, %v", page, err)
		}
		next = page.Next
		select {
		case comparison = <-comparisons:
			compared = true
		case <-time.After(100 * time.Millisecond):
		case <-deadline:
			t.Fatal("no search was compared with the index")
		}
	}
	if comparison.Candidates != 3 || comparison.Overlap[10] != 1 || comparison.Overlap[500] != 1 {
		t.Errorf("comparison = %+v, want the index to agree on all three photos", comparison)
	}

	if _, err := service.Search(context.Background(), alice, photos.SearchRequest{Query: "猫", Limit: 1, Cursor: next}); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-comparisons:
		t.Errorf("a later page was compared: %+v", c)
	case <-time.After(500 * time.Millisecond):
	}
}
