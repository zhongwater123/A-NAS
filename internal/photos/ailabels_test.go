package photos_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photos/labels"
)

// colourLabels shows red and blue for the colour embedder; green only takes
// part in search.
func colourLabels(t *testing.T, model string) *labels.Set {
	t.Helper()
	set, err := labels.Parse(
		[]byte(`{"version": 1, "labels": [
			{"id": "red", "name": "红色", "synonyms": [], "category": "object"},
			{"id": "blue", "name": "蓝色", "synonyms": [], "category": "object"},
			{"id": "green", "name": "绿色", "synonyms": [], "category": "object"}]}`),
		[]byte(`{"labels": 1, "model": "`+model+`", "phrase": "{}", "synonyms": false,
			"thresholds": {"red": 0.9, "blue": 0.9}}`))
	if err != nil {
		t.Fatal(err)
	}
	return &set
}

func labelService(t *testing.T, root string, embedder photos.Embedder, set *labels.Set) *photos.Service {
	t.Helper()
	service, err := photos.Open(root, photos.Options{Now: newClock().Now, DisableCapacityReserve: true, AI: embedder, Labels: set})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service
}

func labelIDs(t *testing.T, service *photos.Service, p photos.Principal, assetID string) []string {
	t.Helper()
	asset, err := service.Get(context.Background(), p, assetID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	var ids []string
	for _, label := range asset.AILabels {
		ids = append(ids, label.ID)
	}
	return ids
}

func TestPhotosShowOnlyLabelsAboveTheirThresholds(t *testing.T) {
	embedder := &colorEmbedder{}
	root := filepath.Join(t.TempDir(), "photos")
	service := labelService(t, root, embedder, colourLabels(t, "colour-1"))
	ctx := context.Background()
	private, _ := libraries(t, service, namedAlice)
	redPhoto := importPhoto(t, service, namedAlice, private.ID, "", "a.png", red)
	bluePhoto := importPhoto(t, service, namedAlice, private.ID, "", "b.png", blue)
	greenPhoto := importPhoto(t, service, namedAlice, private.ID, "", "c.png", green)
	index(t, service, embedder)

	if got := labelIDs(t, service, namedAlice, redPhoto.ID); !slices.Equal(got, []string{"red"}) {
		t.Fatalf("red photo labels = %v", got)
	}
	if got := labelIDs(t, service, namedAlice, bluePhoto.ID); !slices.Equal(got, []string{"blue"}) {
		t.Fatalf("blue photo labels = %v", got)
	}
	// Green has no threshold, so it is never shown.
	if got := labelIDs(t, service, namedAlice, greenPhoto.ID); len(got) != 0 {
		t.Fatalf("green photo labels = %v", got)
	}
	page, err := service.Search(ctx, namedAlice, photos.SearchRequest{Label: "red"})
	if err != nil || !page.Semantic || !slices.Equal(names(page.Assets), []string{"a.png"}) {
		t.Fatalf("Search(label red) = %v semantic=%v, %v", names(page.Assets), page.Semantic, err)
	}
	for _, request := range []photos.SearchRequest{{Label: "green"}, {Label: "missing"}, {Label: "red", Query: "红"}} {
		if _, err := service.Search(ctx, namedAlice, request); !errors.Is(err, photos.ErrInvalidQuery) {
			t.Fatalf("Search(%+v) error = %v, want ErrInvalidQuery", request, err)
		}
	}
	// Each label text was encoded once, before any photo.
	if !slices.Equal(embedder.queries, []string{"红色", "蓝色"}) {
		t.Fatalf("label texts encoded = %v", embedder.queries)
	}

	// The vectors are kept: after a restart labels show without the Worker.
	_ = service.Close()
	embedder.setUnavailable(true)
	restarted := labelService(t, root, embedder, colourLabels(t, "colour-1"))
	if got := labelIDs(t, restarted, namedAlice, redPhoto.ID); !slices.Equal(got, []string{"red"}) {
		t.Fatalf("red photo labels after a restart = %v", got)
	}
}

func TestLabelsHoldOnlyForTheCalibratedModel(t *testing.T) {
	embedder := &colorEmbedder{}
	service := labelService(t, filepath.Join(t.TempDir(), "photos"), embedder, colourLabels(t, "another model"))
	private, _ := libraries(t, service, namedAlice)
	redPhoto := importPhoto(t, service, namedAlice, private.ID, "", "a.png", red)
	index(t, service, embedder)

	if got := labelIDs(t, service, namedAlice, redPhoto.ID); len(got) != 0 {
		t.Fatalf("labels calibrated for another model show %v", got)
	}
	page, err := service.Search(context.Background(), namedAlice, photos.SearchRequest{Label: "red"})
	if err != nil || page.Semantic || len(page.Assets) != 0 {
		t.Fatalf("Search(label) with another model = %v semantic=%v, %v", names(page.Assets), page.Semantic, err)
	}
	if len(embedder.queries) != 0 {
		t.Fatalf("encoded label texts %v for a model they were not calibrated for", embedder.queries)
	}
}
