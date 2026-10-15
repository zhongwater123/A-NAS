package photos_test

import (
	"bytes"
	"context"
	"image"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photos/objects"
)

// pettingEmbedder is a fake AI Worker whose detector finds an animal in each
// painted band of a people photo: red and green are two dogs, blue a dog the
// detector is not sure of, yellow a cat. Its animal model tells the dogs
// apart by colour, and it can lose sight of dogs or have no animal model.
type pettingEmbedder struct {
	*fakeEmbedder
	mu          sync.Mutex
	detector    string
	animalModel string
	blind       bool
	detections  int
	embedded    [][4]float32
}

func newPettingEmbedder() *pettingEmbedder {
	return &pettingEmbedder{fakeEmbedder: newFakeEmbedder(), detector: "detector-1", animalModel: "animals-1"}
}

func (p *pettingEmbedder) Info(ctx context.Context) (photos.EmbedderInfo, error) {
	info, err := p.fakeEmbedder.Info(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	if err == nil {
		info.Detector, info.Animals = p.detector, p.animalModel
	}
	return info, err
}

func (p *pettingEmbedder) Release(context.Context, bool) error { return nil }

func (p *pettingEmbedder) DetectObjects(_ context.Context, input *os.File) (string, []photos.Box, error) {
	img, _, err := image.Decode(input)
	if err != nil {
		return "", nil, photos.ErrAIRejected
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.detections++
	boxes := []photos.Box{}
	for i, painted := range paintedBands(img) {
		class, score := 93, float32(0.9)
		switch {
		case painted < 0, p.blind && painted < 3:
			continue
		case painted == 2:
			score = 0.5
		case painted == 3:
			class = 140
		}
		x0 := float32(i)/4 + 0.01
		boxes = append(boxes, photos.Box{Class: class, Score: score, X0: x0, Y0: 0.17, X1: x0 + 0.23, Y1: 0.83})
	}
	return p.detector, boxes, nil
}

func (p *pettingEmbedder) EmbedAnimals(_ context.Context, input *os.File, boxes [][4]float32) (string, [][]float32, error) {
	content, err := io.ReadAll(input)
	if err != nil {
		return "", nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(content))
	if err != nil {
		return "", nil, photos.ErrAIRejected
	}
	bands := paintedBands(img)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.embedded = append(p.embedded, boxes...)
	vectors := make([][]float32, len(boxes))
	for i, box := range boxes {
		vector := make([]float32, 8)
		vector[bands[int(box[0]*4)]] = 1
		vector[4+len(content)%4] = 0.05
		normalize(vector)
		vectors[i] = vector
	}
	return p.animalModel, vectors, nil
}

var petCalibration = []photos.PeopleCalibration{
	{Kind: photos.OccurrenceDog, Model: "animals-1", Threshold: 0.9, MinCore: 3, Rule: photos.RuleCentroid},
}

// openPetService opens the photo service with a detection calibration for
// detector, which says from which score a box is a dog.
func openPetService(t *testing.T, root, detector string) *photos.Service {
	t.Helper()
	calibration, err := objects.ParseCalibration([]byte(`{"model": "` + detector + `", "weight": 0.2, "tierCoverage": 0.6,
		"fusion": [93, 140], "words": {"93": {"fusion": true, "tierScore": 0.65}, "140": {"fusion": true, "tierScore": 0.6}}}`))
	if err != nil {
		t.Fatal(err)
	}
	service, err := photos.Open(root, photos.Options{Now: newClock().Now, DisableCapacityReserve: true,
		People: petCalibration, Objects: &calibration})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service
}

func importPetPhotos(t *testing.T, service *photos.Service) {
	t.Helper()
	private, _ := libraries(t, service, alice)
	// Red is in four photos and green in four; blue is never sure enough to
	// be a dog, and the cat in yellow is never told apart.
	for i, painted := range [][]int{{0}, {0, 1}, {1, -1, 0}, {0, 1, 2}, {3, 1}, {2, 3}} {
		importPhoto(t, service, alice, private.ID, "", string(rune('1'+i))+".png", peoplePhoto(t, uint8(i), painted...))
	}
}

func dogs(t *testing.T, service *photos.Service) []photos.Person {
	t.Helper()
	var found []photos.Person
	for _, person := range people(t, service, alice, true) {
		if person.Kind != photos.PersonKindDog {
			t.Fatalf("People() has %+v; only dogs are told apart", person)
		}
		found = append(found, person)
	}
	return found
}

func TestDogsClusterIntoPetsAndCatsDoNot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "photos")
	service := openPetService(t, root, "detector-1")
	worker := newPettingEmbedder()
	importPetPhotos(t, service)
	organise(t, service, worker)

	// Only the five photos with a sure dog are embedded, and only their dogs.
	if len(worker.embedded) != 8 {
		t.Fatalf("embedded %d animals, want the 8 sure dogs", len(worker.embedded))
	}
	found := dogs(t, service)
	if len(found) != 2 || found[0].Photos != 4 || found[1].Photos != 4 {
		t.Fatalf("People() = %+v; want red and green, in four photos each", found)
	}
	var names [][]string
	for _, dog := range found {
		names = append(names, personPhotos(t, service, alice, dog.ID))
		avatar, err := service.OccurrenceAvatar(context.Background(), alice, dog.CoverID)
		if err != nil {
			t.Fatalf("OccurrenceAvatar() error = %v", err)
		}
		if config, _, err := image.DecodeConfig(bytes.NewReader(avatar)); err != nil || config.Width != config.Height {
			t.Fatalf("avatar = %+v, %v; want a square JPEG", config, err)
		}
	}
	slices.SortFunc(names, slices.Compare)
	if want := [][]string{{"1.png", "2.png", "3.png", "4.png"}, {"2.png", "3.png", "4.png", "5.png"}}; !slices.EqualFunc(names, want, slices.Equal) {
		t.Fatalf("the dogs' photos = %v, want %v", names, want)
	}
	status, err := service.AIStatus(context.Background(), alice)
	if err != nil || status.AnimalModel != "animals-1" || status.Ready != 6 || status.Pending != 0 {
		t.Fatalf("AIStatus() = %+v, %v; want every photo ready", status, err)
	}

	// A detection that no longer finds the dogs takes them out again.
	worker.mu.Lock()
	worker.detector, worker.blind = "detector-2", true
	worker.mu.Unlock()
	service = openPetService(t, root, "detector-2")
	organise(t, service, worker)
	if found := dogs(t, service); len(found) != 0 {
		t.Fatalf("People() = %+v after the dogs left every photo", found)
	}
}

func TestDogsDetectedBeforeTheAnimalModelAreScannedOnceItArrives(t *testing.T) {
	root := filepath.Join(t.TempDir(), "photos")
	service := openPetService(t, root, "detector-1")
	worker := newPettingEmbedder()
	worker.animalModel = ""
	importPetPhotos(t, service)
	organise(t, service, worker)
	if found := dogs(t, service); len(found) != 0 || len(worker.embedded) != 0 {
		t.Fatalf("People() = %+v with no animal model", found)
	}
	detections := worker.detections

	// The animal model arrives with a release, which restarts the service.
	worker.animalModel = "animals-1"
	service = openPetService(t, root, "detector-1")
	organise(t, service, worker)
	if found := dogs(t, service); len(found) != 2 {
		t.Fatalf("People() = %+v; want red and green", found)
	}
	if worker.detections != detections || len(worker.embedded) != 8 {
		t.Fatalf("detected %d more times and embedded %d animals; want no detection again and the 8 sure dogs",
			worker.detections-detections, len(worker.embedded))
	}
}

func TestReconcileRescansAMissingAvatarSheetAndRemovesAStrayOne(t *testing.T) {
	root := filepath.Join(t.TempDir(), "photos")
	service := openPetService(t, root, "detector-1")
	worker := newPettingEmbedder()
	importPetPhotos(t, service)
	organise(t, service, worker)
	sheets, err := filepath.Glob(filepath.Join(root, "derived", "pets-v1", "*", "*.jpg"))
	if err != nil || len(sheets) != 5 {
		t.Fatalf("pet sheets = %v, %v; want one per photo with a dog", sheets, err)
	}
	if err := os.Remove(sheets[0]); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(root, "derived", "faces-v1", "ab", "ab"+string(bytes.Repeat([]byte("0"), 62))+".jpg")
	writeFile(t, stray, []byte("left by a crash"))

	report, err := service.Reconcile(context.Background())
	if err != nil || report.DerivedRequeued != 1 || report.DerivedRemoved != 1 || report.UnexpectedEntries != 0 {
		t.Fatalf("Reconcile() = %+v, %v; want the missing sheet rescanned and the stray one removed", report, err)
	}
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Fatalf("stray sheet kept: %v", err)
	}
	embedded := len(worker.embedded)
	processAI(t, service, worker, &fakeGate{open: true})
	if _, err := os.Stat(sheets[0]); err != nil || len(worker.embedded) == embedded {
		t.Fatalf("missing sheet not rescanned: %v", err)
	}
	if found := dogs(t, service); len(found) != 2 || found[0].Photos != 4 || found[1].Photos != 4 {
		t.Fatalf("People() = %+v after the rescan; want red and green unchanged", found)
	}
}

func TestFacesOnADogAreNotPeople(t *testing.T) {
	ctx := context.Background()
	service, _ := newPeopleService(t)
	private, _ := libraries(t, service, alice)
	worker := newFacingEmbedder()
	// A dog fills the left half; a person stands over the right three
	// quarters, holding the dog's head in the second band.
	worker.boxes = []photos.Box{
		{Class: 93, Score: 0.9, X0: 0, Y0: 0, X1: 0.5, Y1: 1},
		{Class: 1, Score: 0.9, X0: 0.25, Y0: 0, X1: 1, Y1: 1},
	}
	var onDog photos.Asset
	for i := range 3 {
		onDog = importPhoto(t, service, alice, private.ID, "", "dog"+string(rune('a'+i))+".png", peoplePhoto(t, uint8(i), 0))
		importPhoto(t, service, alice, private.ID, "", "held"+string(rune('a'+i))+".png", peoplePhoto(t, uint8(i), -1, 2))
	}
	organise(t, service, worker)
	found := people(t, service, alice, false)
	if len(found) != 1 || found[0].Photos != 3 {
		t.Fatalf("People() = %+v; want only the person holding the dog", found)
	}
	if got := personPhotos(t, service, alice, found[0].ID); !slices.Equal(got, []string{"helda.png", "heldb.png", "heldc.png"}) {
		t.Fatalf("the person's photos = %v", got)
	}
	if details, err := service.Get(ctx, alice, onDog.ID); err != nil || len(details.People) != 0 {
		t.Fatalf("the dog's face = %+v, %v; want none kept", details.People, err)
	}
}
