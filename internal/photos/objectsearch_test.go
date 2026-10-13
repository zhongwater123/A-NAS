package photos_test

import (
	"context"
	"image"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photos/objects"
)

// colorDetector adds a detector to colorEmbedder that finds a phone in red
// photos, a faint one in green photos and nothing in blue ones.
type colorDetector struct {
	*colorEmbedder
	model string
}

func (d *colorDetector) Info(ctx context.Context) (photos.EmbedderInfo, error) {
	info, err := d.colorEmbedder.Info(ctx)
	info.Detector = d.model
	return info, err
}

func (d *colorDetector) DetectObjects(_ context.Context, input *os.File) (string, []photos.Box, error) {
	img, _, err := image.Decode(input)
	if err != nil {
		return "", nil, photos.ErrAIRejected
	}
	r, g, _, _ := img.At(img.Bounds().Dx()/2, img.Bounds().Dy()/2).RGBA()
	phone := photos.Box{Class: 62, X0: 0.4, Y0: 0.4, X1: 0.45, Y1: 0.45}
	switch {
	case r > 0x8000:
		phone.Score = 0.9
		return d.model, []photos.Box{phone}, nil
	case g > 0x8000:
		phone.Score = 0.3
		return d.model, []photos.Box{phone}, nil
	}
	return d.model, []photos.Box{}, nil
}

func (d *colorDetector) Release(context.Context, bool) error { return nil }

// objectSearchService has phones and cats on the fusion list and phones on
// the tier list, for a detector named model.
func objectSearchService(t *testing.T, calibrated string) (*photos.Service, *colorDetector) {
	t.Helper()
	calibration, err := objects.ParseCalibration([]byte(`{"model": "` + calibrated + `", "weight": 0.2,
		"tierScore": 0.6, "tierCoverage": 0.6, "fusion": [62, 140], "tier": [62, 140], "disabledWords": ["花"]}`))
	if err != nil {
		t.Fatal(err)
	}
	worker := &colorDetector{colorEmbedder: &colorEmbedder{}, model: "detector-1"}
	service, err := photos.Open(filepath.Join(t.TempDir(), "photos"), photos.Options{
		Now: newClock().Now, DisableCapacityReserve: true, AI: worker, Objects: &calibration})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service, worker
}

func TestAQueryAboutAThingPutsThePhotosItIsFoundInFirst(t *testing.T) {
	service, worker := objectSearchService(t, "detector-1")
	private, _ := libraries(t, service, alice)
	importPhoto(t, service, alice, private.ID, "", "blue.png", blue)
	importPhoto(t, service, alice, private.ID, "", "green.png", green)
	importPhoto(t, service, alice, private.ID, "", "red.png", red)
	importPhoto(t, service, alice, private.ID, "", "手机壳.png", blue)
	index(t, service, worker)

	page := search(t, service, alice, "手机")
	if got := names(page.Assets); !slices.Equal(got[:2], []string{"手机壳.png", "red.png"}) || len(got) != 4 {
		t.Fatalf("results = %v; want the name match, then the photo with a phone, then the rest", got)
	}
	if page.Closest != 2 || page.Objects == nil || page.Objects.Detected != 1 || page.Objects.Undetected != 0 ||
		!slices.Equal(page.Objects.Words, []string{"手机"}) {
		t.Fatalf("closest %d, objects %+v; want the detected group to be the closest", page.Closest, page.Objects)
	}

	// Nothing has a cat: the page can say so.
	page = search(t, service, alice, "猫")
	if page.Objects == nil || page.Objects.Detected != 0 || page.Closest == 0 {
		t.Fatalf("closest %d, objects %+v; want none detected and the closest photos by meaning", page.Closest, page.Objects)
	}

	// A sentence that only mentions a phone is not about it.
	page = search(t, service, alice, "傍晚在海边散步时用手机拍下的日落")
	if page.Objects != nil {
		t.Fatalf("objects %+v for a sentence", page.Objects)
	}
	// A disabled word adds nothing and is no thing to look for.
	if page := search(t, service, alice, "花"); page.Objects != nil {
		t.Fatalf("objects %+v for a disabled word", page.Objects)
	}
}

func TestPagesCrossFromTheDetectedGroupToTheRest(t *testing.T) {
	service, worker := objectSearchService(t, "detector-1")
	private, _ := libraries(t, service, alice)
	for i, content := range [][]byte{red, solidPNG(230, 40, 40), green, blue, solidPNG(30, 30, 200)} {
		importPhoto(t, service, alice, private.ID, "", string(rune('a'+i))+".png", content)
	}
	index(t, service, worker)
	all := search(t, service, alice, "手机")
	var paged []string
	cursor := ""
	for {
		page, err := service.Search(context.Background(), alice, photos.SearchRequest{Query: "手机", Cursor: cursor, Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if page.Closest != 2 || page.Objects == nil || page.Objects.Detected != 2 {
			t.Fatalf("page closest %d, objects %+v; every page carries the same counts", page.Closest, page.Objects)
		}
		paged = append(paged, names(page.Assets)...)
		if page.Next == "" {
			break
		}
		cursor = page.Next
	}
	if !slices.Equal(paged, names(all.Assets)) {
		t.Fatalf("paged %v, want %v", paged, names(all.Assets))
	}
}

func TestTheDetectedGroupNeedsNoWorkerAndCountsWhatIsStillWaiting(t *testing.T) {
	service, worker := objectSearchService(t, "detector-1")
	private, _ := libraries(t, service, alice)
	bobPrivate, _ := libraries(t, service, bob)
	importPhoto(t, service, alice, private.ID, "", "red.png", red)
	importPhoto(t, service, alice, private.ID, "", "blue.png", blue)
	index(t, service, worker)
	// Not yet detected: one of alice's, and bob's, which her search never counts.
	importPhoto(t, service, alice, private.ID, "", "later.png", green)
	importPhoto(t, service, bob, bobPrivate.ID, "", "bob.png", green)
	processAll(t, service)

	worker.setUnavailable(true)
	page := search(t, service, alice, "手机")
	if page.Semantic || !slices.Equal(names(page.Assets), []string{"red.png"}) {
		t.Fatalf("results without the Worker = %v (semantic %v); want the detected photo alone", names(page.Assets), page.Semantic)
	}
	if page.Objects == nil || page.Objects.Detected != 1 || page.Objects.Undetected != 1 {
		t.Fatalf("objects %+v; want alice's one photo still waiting counted", page.Objects)
	}
}

func TestBoxesOfAnotherModelTakeNoPart(t *testing.T) {
	service, worker := objectSearchService(t, "another-detector")
	private, _ := libraries(t, service, alice)
	importPhoto(t, service, alice, private.ID, "", "blue.png", blue)
	importPhoto(t, service, alice, private.ID, "", "red.png", red)
	index(t, service, worker)
	if page := search(t, service, alice, "手机"); page.Objects != nil {
		t.Fatalf("objects %+v from boxes of an uncalibrated model", page.Objects)
	}
}
