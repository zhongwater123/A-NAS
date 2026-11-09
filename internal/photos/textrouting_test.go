package photos_test

import (
	"context"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photos/textroute"
)

// redRoute reads the text of red photos for the colour embedder: red stands
// for the documents the real route finds.
func redRoute(t *testing.T, model string) *textroute.Route {
	t.Helper()
	route, err := textroute.Parse([]byte(`{"model": "` + model + `", "queries": ["红色"], "threshold": 0.9}`))
	if err != nil {
		t.Fatal(err)
	}
	return &route
}

func routeService(t *testing.T, root string, embedder photos.Embedder, route *textroute.Route) *photos.Service {
	t.Helper()
	service, err := photos.Open(root, photos.Options{Now: newClock().Now, DisableCapacityReserve: true, AI: embedder,
		Labels: colourLabels(t, "other"), TextRoute: route})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service
}

// shown formats an optional count.
func shown(count *int) string {
	if count == nil {
		return "absent"
	}
	return strconv.Itoa(*count)
}

func textPhotos(t *testing.T, service *photos.Service, p photos.Principal) *int {
	t.Helper()
	status, err := service.AIStatus(context.Background(), p)
	if err != nil {
		t.Fatalf("AIStatus() error = %v", err)
	}
	return status.TextPhotos
}

// textProgress is how far reading text has come for p.
func textProgress(t *testing.T, service *photos.Service, p photos.Principal) photos.TextProgress {
	t.Helper()
	status, err := service.AIStatus(context.Background(), p)
	if err != nil {
		t.Fatalf("AIStatus() error = %v", err)
	}
	if status.Texts == nil {
		t.Fatal("AIStatus() reports no text progress")
	}
	return *status.Texts
}

func TestPhotosWhoseVectorLooksLikeTextAreQueuedForReading(t *testing.T) {
	embedder := &colorEmbedder{}
	service := routeService(t, filepath.Join(t.TempDir(), "photos"), embedder, redRoute(t, "colour-1"))
	alicePrivate, shared := libraries(t, service, namedAlice)
	bobPrivate, _ := libraries(t, service, namedBob)
	if got := textPhotos(t, service, namedAlice); got != nil {
		t.Fatalf("TextPhotos before the route's queries have vectors = %d, want absent", *got)
	}
	// Distinct originals: photos with equal bytes are one content object.
	receipt := importPhoto(t, service, namedAlice, alicePrivate.ID, "", "receipt.png", red)
	importPhoto(t, service, namedAlice, alicePrivate.ID, "", "crimson.png", solidPNG(250, 5, 5))
	importPhoto(t, service, namedAlice, alicePrivate.ID, "", "sea.png", blue)
	importPhoto(t, service, namedBob, shared.ID, "", "shared.png", solidPNG(240, 10, 10))
	importPhoto(t, service, namedBob, bobPrivate.ID, "", "bob.png", solidPNG(230, 20, 20))
	index(t, service, embedder)

	// The query was encoded once, as a search query is.
	if !slices.Equal(embedder.queries, []string{"红色"}) {
		t.Fatalf("queries encoded = %v", embedder.queries)
	}
	// Each count covers the photos its caller can see.
	if got := textPhotos(t, service, namedAlice); got == nil || *got != 3 {
		t.Fatalf("Alice's TextPhotos = %s, want her two red photos and the shared one", shown(got))
	}
	if got := textPhotos(t, service, namedBob); got == nil || *got != 2 {
		t.Fatalf("Bob's TextPhotos = %s, want his red photo and the shared one", shown(got))
	}
	// This embedder reads no text: all of them wait.
	if got := textProgress(t, service, namedAlice); got != (photos.TextProgress{Queued: 3}) {
		t.Fatalf("Alice's text progress = %+v, want three queued", got)
	}
	if details, err := service.Get(context.Background(), namedAlice, receipt.ID); err != nil || details.Text != photos.TextQueued {
		t.Fatalf("the receipt's details: Text = %q, %v; want queued", details.Text, err)
	}
}

// Vectors stored before the route applied are routed once it does.
func TestVectorsStoredEarlierAreRoutedOnce(t *testing.T) {
	embedder := &colorEmbedder{}
	root := filepath.Join(t.TempDir(), "photos")
	before := routeService(t, root, embedder, redRoute(t, "another-model"))
	private, _ := libraries(t, before, namedAlice)
	importPhoto(t, before, namedAlice, private.ID, "", "receipt.png", red)
	importPhoto(t, before, namedAlice, private.ID, "", "sea.png", blue)
	index(t, before, embedder)
	if got := textPhotos(t, before, namedAlice); got != nil {
		t.Fatalf("TextPhotos with a route for another model = %d, want absent", *got)
	}
	_ = before.Close()

	after := routeService(t, root, embedder, redRoute(t, "colour-1"))
	index(t, after, embedder)
	if got := textPhotos(t, after, namedAlice); got == nil || *got != 1 {
		t.Fatalf("TextPhotos once the route applies = %s, want the red photo", shown(got))
	}
}
