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

// Label counts follow a search by label: the caller's and the shared
// library, without photos that hid the label.
func TestLabelCountsCoverTheSearchScope(t *testing.T) {
	embedder := &colorEmbedder{}
	service := labelService(t, filepath.Join(t.TempDir(), "photos"), embedder, colourLabels(t, "colour-1"))
	ctx := context.Background()
	alicePrivate, shared := libraries(t, service, namedAlice)
	bobPrivate, _ := libraries(t, service, namedBob)
	if counts, ready, err := service.LabelCounts(ctx, namedAlice, ""); err != nil || ready || len(counts) != 0 {
		t.Fatalf("LabelCounts() before label vectors = %v ready=%v, %v", counts, ready, err)
	}
	clearest := importPhoto(t, service, namedAlice, alicePrivate.ID, "", "clearest.png", solidPNG(250, 5, 5))
	importPhoto(t, service, namedAlice, alicePrivate.ID, "", "red.png", red)
	hiding := importPhoto(t, service, namedAlice, alicePrivate.ID, "", "hiding.png", red)
	bluePhoto := importPhoto(t, service, namedAlice, alicePrivate.ID, "", "blue.png", blue)
	importPhoto(t, service, namedBob, shared.ID, "", "shared.png", red)
	importPhoto(t, service, namedBob, bobPrivate.ID, "", "bob.png", red)
	index(t, service, embedder)
	if _, err := service.HideAILabel(ctx, namedAlice, hiding.ID, "red"); err != nil {
		t.Fatalf("HideAILabel() error = %v", err)
	}

	counts, ready, err := service.LabelCounts(ctx, namedAlice, "")
	want := []photos.LabelCount{
		{ID: "red", Name: "红色", Category: "object", Photos: 3, CoverID: clearest.ID},
		{ID: "blue", Name: "蓝色", Category: "object", Photos: 1, CoverID: bluePhoto.ID},
	}
	if err != nil || !ready || !slices.Equal(counts, want) {
		t.Fatalf("LabelCounts() = %+v ready=%v, %v; want %+v", counts, ready, err, want)
	}
	if _, _, err := service.LabelCounts(ctx, namedBob, alicePrivate.ID); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("LabelCounts() naming another member's library error = %v, want ErrNotFound", err)
	}
}

// A query naming a calibrated label keeps only the photos that show it; a
// query naming none keeps those near its best match.
func TestSearchNamingACalibratedLabelKeepsOnlyPhotosShowingIt(t *testing.T) {
	embedder := &colorEmbedder{}
	service := labelService(t, filepath.Join(t.TempDir(), "photos"), embedder, colourLabels(t, "colour-1"))
	ctx := context.Background()
	private, _ := libraries(t, service, namedAlice)
	importPhoto(t, service, namedAlice, private.ID, "", "red.png", red)
	importPhoto(t, service, namedAlice, private.ID, "", "crimson.png", solidPNG(250, 10, 10))
	importPhoto(t, service, namedAlice, private.ID, "", "rust.png", solidPNG(150, 60, 60))
	importPhoto(t, service, namedAlice, private.ID, "", "blue.png", blue)
	importPhoto(t, service, namedAlice, private.ID, "", "红色的车.png", blue)
	hiding := importPhoto(t, service, namedAlice, private.ID, "", "hiding.png", red)
	index(t, service, embedder)
	if _, err := service.HideAILabel(ctx, namedAlice, hiding.ID, "red"); err != nil {
		t.Fatal(err)
	}

	// Rust is reddish but below the threshold; a name match still comes first.
	for query, want := range map[string][]string{
		"红色":    {"红色的车.png", "crimson.png", "red.png"},
		"红色的衣服": {"crimson.png", "red.png"},
	} {
		page := search(t, service, namedAlice, query)
		if page.Match != photos.MatchLabels || !slices.Equal(page.Labels, []photos.LabelRef{{ID: "red", Name: "红色"}}) || !slices.Equal(names(page.Assets), want) {
			t.Fatalf("Search(%q) = %v match=%q labels=%v", query, names(page.Assets), page.Match, page.Labels)
		}
	}
	// Green is in the vocabulary but has no threshold.
	if page := search(t, service, namedAlice, "绿色"); page.Match != photos.MatchClosest || len(page.Labels) != 0 || len(page.Assets) == 0 {
		t.Fatalf("Search(绿色) = %v match=%q labels=%v", names(page.Assets), page.Match, page.Labels)
	}
	page, err := service.Search(ctx, namedAlice, photos.SearchRequest{Label: "red"})
	if err != nil || page.Match != photos.MatchLabels || !slices.Equal(page.Labels, []photos.LabelRef{{ID: "red", Name: "红色"}}) {
		t.Fatalf("Search(label red) match=%q labels=%v, %v", page.Match, page.Labels, err)
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
	// Naming the label in a query does not filter by its threshold either.
	if page := search(t, service, namedAlice, "红色"); page.Match != photos.MatchClosest || !slices.Equal(names(page.Assets), []string{"a.png"}) {
		t.Fatalf("Search(红色) with another model = %v match=%q", names(page.Assets), page.Match)
	}
}
