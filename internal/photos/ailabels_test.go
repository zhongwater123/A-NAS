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

// cluster lists every page of a label's cluster.
func cluster(t *testing.T, service *photos.Service, p photos.Principal, label string) []string {
	t.Helper()
	var found []string
	cursor := ""
	for {
		page, err := service.LabelPhotos(context.Background(), p, label, "", cursor, 1)
		if err != nil {
			t.Fatalf("LabelPhotos(%s) error = %v", label, err)
		}
		found = append(found, names(page.Assets)...)
		if cursor = page.Next; cursor == "" {
			return found
		}
	}
}

func TestClustersHoldOnlyPhotosAboveTheirThresholds(t *testing.T) {
	embedder := &colorEmbedder{}
	root := filepath.Join(t.TempDir(), "photos")
	service := labelService(t, root, embedder, colourLabels(t, "colour-1"))
	ctx := context.Background()
	private, _ := libraries(t, service, namedAlice)
	importPhoto(t, service, namedAlice, private.ID, "", "a.png", red)
	importPhoto(t, service, namedAlice, private.ID, "", "crimson.png", solidPNG(250, 5, 5))
	importPhoto(t, service, namedAlice, private.ID, "", "b.png", blue)
	importPhoto(t, service, namedAlice, private.ID, "", "c.png", green)
	index(t, service, embedder)

	// Clearest first, one photo per page.
	if got := cluster(t, service, namedAlice, "red"); !slices.Equal(got, []string{"crimson.png", "a.png"}) {
		t.Fatalf("red cluster = %v", got)
	}
	if got := cluster(t, service, namedAlice, "blue"); !slices.Equal(got, []string{"b.png"}) {
		t.Fatalf("blue cluster = %v", got)
	}
	// Green has no threshold, so it has no cluster.
	for _, label := range []string{"green", "missing"} {
		if _, err := service.LabelPhotos(ctx, namedAlice, label, "", "", 0); !errors.Is(err, photos.ErrNotFound) {
			t.Fatalf("LabelPhotos(%s) error = %v, want ErrNotFound", label, err)
		}
	}
	// Each label text was encoded once, before any photo.
	if !slices.Equal(embedder.queries, []string{"红色", "蓝色"}) {
		t.Fatalf("label texts encoded = %v", embedder.queries)
	}

	// The vectors are kept: after a restart clusters list without the Worker.
	_ = service.Close()
	embedder.setUnavailable(true)
	restarted := labelService(t, root, embedder, colourLabels(t, "colour-1"))
	if got := cluster(t, restarted, namedAlice, "red"); !slices.Equal(got, []string{"crimson.png", "a.png"}) {
		t.Fatalf("red cluster after a restart = %v", got)
	}
}

// A photo taken out of a cluster stays out, on that photo only, through a
// copy and a restart.
func TestCorrectionsTakeOnePhotoOutOfACluster(t *testing.T) {
	embedder := &colorEmbedder{}
	root := filepath.Join(t.TempDir(), "photos")
	service := labelService(t, root, embedder, colourLabels(t, "colour-1"))
	ctx := context.Background()
	private, shared := libraries(t, service, namedAlice)
	first := importPhoto(t, service, namedAlice, private.ID, "", "a.png", red)
	importPhoto(t, service, namedAlice, private.ID, "", "b.png", solidPNG(240, 20, 20))
	index(t, service, embedder)

	if _, err := service.HideAILabel(ctx, namedAlice, first.ID, "red"); err != nil {
		t.Fatalf("HideAILabel() error = %v", err)
	}
	if got := cluster(t, service, namedAlice, "red"); !slices.Equal(got, []string{"b.png"}) {
		t.Fatalf("red cluster = %v, want the photo taken out left out", got)
	}
	if _, err := service.HideAILabel(ctx, namedAlice, first.ID, "no-such-label"); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("HideAILabel(unknown) error = %v", err)
	}
	if _, err := service.HideAILabel(ctx, namedBob, first.ID, "red"); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("taking another member's photo out of a cluster error = %v", err)
	}
	// A copy keeps the correction.
	if _, err := service.Copy(ctx, namedAlice, first.ID, shared.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got := cluster(t, service, namedAlice, "red"); !slices.Equal(got, []string{"b.png"}) {
		t.Fatalf("red cluster after a copy = %v", got)
	}
	// Corrections are user data: a restart keeps them.
	_ = service.Close()
	restarted := labelService(t, root, embedder, colourLabels(t, "colour-1"))
	if got := cluster(t, restarted, namedAlice, "red"); !slices.Equal(got, []string{"b.png"}) {
		t.Fatalf("red cluster after a restart = %v", got)
	}
}

// Label counts cover a search's scope: the caller's and the shared library,
// without photos taken out of a cluster.
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

// The red line (docs/specs/photo-library.md): labels made from the vectors
// never take part in search. A query naming a calibrated label ranks every
// photo, and a photo taken out of the label's cluster still matches.
func TestSearchNeverUsesClusters(t *testing.T) {
	embedder := &colorEmbedder{}
	service := labelService(t, filepath.Join(t.TempDir(), "photos"), embedder, colourLabels(t, "colour-1"))
	ctx := context.Background()
	private, _ := libraries(t, service, namedAlice)
	importPhoto(t, service, namedAlice, private.ID, "", "red.png", red)
	importPhoto(t, service, namedAlice, private.ID, "", "rust.png", solidPNG(150, 60, 60))
	importPhoto(t, service, namedAlice, private.ID, "", "blue.png", blue)
	importPhoto(t, service, namedAlice, private.ID, "", "红色的车.png", blue)
	hiding := importPhoto(t, service, namedAlice, private.ID, "", "hiding.png", solidPNG(230, 15, 15))
	index(t, service, embedder)
	if _, err := service.HideAILabel(ctx, namedAlice, hiding.ID, "red"); err != nil {
		t.Fatal(err)
	}

	page := search(t, service, namedAlice, "红色")
	got := names(page.Assets)
	// Rust is below the red threshold and blue far from it; both are still
	// ranked, after the name match and the red photos.
	if len(got) != 5 || got[0] != "红色的车.png" || !slices.Contains(got, "hiding.png") || got[len(got)-1] != "blue.png" {
		t.Fatalf("Search(红色) = %v", got)
	}
}

func TestLabelsHoldOnlyForTheCalibratedModel(t *testing.T) {
	embedder := &colorEmbedder{}
	service := labelService(t, filepath.Join(t.TempDir(), "photos"), embedder, colourLabels(t, "another model"))
	private, _ := libraries(t, service, namedAlice)
	importPhoto(t, service, namedAlice, private.ID, "", "a.png", red)
	index(t, service, embedder)

	if got := cluster(t, service, namedAlice, "red"); len(got) != 0 {
		t.Fatalf("a cluster calibrated for another model holds %v", got)
	}
	if counts, ready, err := service.LabelCounts(context.Background(), namedAlice, ""); err != nil || ready || len(counts) != 0 {
		t.Fatalf("LabelCounts() with another model = %v ready=%v, %v", counts, ready, err)
	}
	if len(embedder.queries) != 0 {
		t.Fatalf("encoded label texts %v for a model they were not calibrated for", embedder.queries)
	}
	if page := search(t, service, namedAlice, "红色"); !slices.Equal(names(page.Assets), []string{"a.png"}) {
		t.Fatalf("Search(红色) with another model = %v", names(page.Assets))
	}
}
