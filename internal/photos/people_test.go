package photos_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/photos"
)

// A people photo is four vertical bands on white; a band painted in one of
// the palette's colours is that person's face. The fake face models read the
// bands back from the AI input, so a test says who is in each photo.
var palette = []color.RGBA{
	{R: 200, G: 30, B: 30, A: 255},
	{R: 30, G: 200, B: 30, A: 255},
	{R: 30, G: 30, B: 200, A: 255},
	{R: 200, G: 200, B: 30, A: 255},
}

const bandWidth, photoHeight = 40, 60

// peoplePhoto paints the people named by palette index into bands, left to
// right; -1 leaves a band empty. variant makes otherwise equal photos differ.
func peoplePhoto(t *testing.T, variant uint8, people ...int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4*bandWidth, photoHeight))
	for x := range 4 * bandWidth {
		for y := range photoHeight {
			img.Set(x, y, color.White)
		}
	}
	for band, person := range people {
		if person < 0 {
			continue
		}
		for x := band * bandWidth; x < (band+1)*bandWidth; x++ {
			for y := 10; y < 50; y++ {
				img.Set(x, y, palette[person])
			}
		}
	}
	img.Set(0, photoHeight-1, color.RGBA{R: variant, G: variant, B: variant, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// facingEmbedder is a fake AI Worker with an object detector and face models
// that find the people of a people photo. Person k's vector is the k-th unit
// vector with a little noise from the photo, so the same person is the same
// in every photo and different people are far apart.
type facingEmbedder struct {
	*detectingEmbedder
	mu         sync.Mutex
	faceModel  string
	faceInputs [][]byte
}

func newFacingEmbedder() *facingEmbedder {
	return &facingEmbedder{detectingEmbedder: newDetectingEmbedder(), faceModel: "faces-1"}
}

func (f *facingEmbedder) Info(ctx context.Context) (photos.EmbedderInfo, error) {
	info, err := f.detectingEmbedder.Info(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		info.Faces = f.faceModel
	}
	return info, err
}

func (f *facingEmbedder) setFaceModel(model string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.faceModel = model
}

func (f *facingEmbedder) DetectFaces(_ context.Context, input *os.File) (string, []photos.Face, error) {
	content, err := io.ReadAll(input)
	if err != nil {
		return "", nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(content))
	if err != nil {
		return "", nil, photos.ErrAIRejected
	}
	f.mu.Lock()
	f.faceInputs = append(f.faceInputs, content)
	model := f.faceModel
	f.mu.Unlock()
	bounds := img.Bounds()
	band := bounds.Dx() / 4
	var faces []photos.Face
	for i := range 4 {
		var r, g, b, n float64
		for x := bounds.Min.X + i*band; x < bounds.Min.X+(i+1)*band; x++ {
			for y := bounds.Min.Y + bounds.Dy()/3; y < bounds.Min.Y+2*bounds.Dy()/3; y++ {
				cr, cg, cb, _ := img.At(x, y).RGBA()
				r, g, b, n = r+float64(cr>>8), g+float64(cg>>8), b+float64(cb>>8), n+1
			}
		}
		r, g, b = r/n, g/n, b/n
		if r > 180 && g > 180 && b > 180 {
			continue
		}
		person, best := 0, math.Inf(1)
		for k, c := range palette {
			if d := math.Abs(r-float64(c.R)) + math.Abs(g-float64(c.G)) + math.Abs(b-float64(c.B)); d < best {
				person, best = k, d
			}
		}
		vector := make([]float32, 8)
		vector[person] = 1
		// A little noise from the photo, never the same twice.
		vector[4+i%4] = float32(len(content)%7) * 0.01
		normalize(vector)
		x0 := float32(i) / 4
		faces = append(faces, photos.Face{
			Box:       [4]float32{x0, 0.17, x0 + 0.25, 0.83},
			Landmarks: [10]float32{x0 + 0.08, 0.4, x0 + 0.17, 0.4, x0 + 0.12, 0.5, x0 + 0.09, 0.6, x0 + 0.16, 0.6},
			Score:     0.95,
			Vector:    vector,
		})
	}
	return model, faces, nil
}

func normalize(vector []float32) {
	var norm float64
	for _, value := range vector {
		norm += float64(value) * float64(value)
	}
	for i := range vector {
		vector[i] /= float32(math.Sqrt(norm))
	}
}

var peopleCalibration = []photos.PeopleCalibration{
	{Kind: photos.OccurrenceFace, Model: "faces-1", Threshold: 0.9, MinCore: 3},
	{Kind: photos.OccurrenceFace, Model: "faces-2", Threshold: 0.9, MinCore: 3},
}

func newPeopleService(t *testing.T) (*photos.Service, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "photos")
	service, err := photos.Open(root, photos.Options{Now: newClock().Now, DisableCapacityReserve: true, People: peopleCalibration})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service, root
}

// organise runs thumbnails, then every AI job and clustering until nothing is
// left.
func organise(t *testing.T, service *photos.Service, worker photos.Embedder) {
	t.Helper()
	processAll(t, service)
	processAI(t, service, worker, &fakeGate{open: true})
}

func people(t *testing.T, service *photos.Service, p photos.Principal, hidden bool) []photos.Person {
	t.Helper()
	found, err := service.People(context.Background(), p, "", hidden)
	if err != nil {
		t.Fatalf("People() error = %v", err)
	}
	return found
}

func personPhotos(t *testing.T, service *photos.Service, p photos.Principal, personID string) []string {
	t.Helper()
	page, err := service.PersonPhotos(context.Background(), p, personID, "", 100)
	if err != nil {
		t.Fatalf("PersonPhotos() error = %v", err)
	}
	var names []string
	for _, asset := range page.Assets {
		names = append(names, asset.Name)
	}
	slices.Sort(names)
	return names
}

func TestFacesShareTheDetectionInputAndClusterIntoPeople(t *testing.T) {
	service, _ := newPeopleService(t)
	private, _ := libraries(t, service, alice)
	worker := newFacingEmbedder()
	// Red is in four photos, green in three, blue only twice: too few.
	importPhoto(t, service, alice, private.ID, "", "1.png", peoplePhoto(t, 1, 0))
	importPhoto(t, service, alice, private.ID, "", "2.png", peoplePhoto(t, 2, 0, 1))
	importPhoto(t, service, alice, private.ID, "", "3.png", peoplePhoto(t, 3, 1, -1, 0))
	importPhoto(t, service, alice, private.ID, "", "4.png", peoplePhoto(t, 4, 2, 0, 1))
	importPhoto(t, service, alice, private.ID, "", "5.png", peoplePhoto(t, 5, 2))
	organise(t, service, worker)

	if len(worker.faceInputs) != 5 || len(worker.inputs) != 5 {
		t.Fatalf("face scans %d, detections %d; want one of each per photo", len(worker.faceInputs), len(worker.inputs))
	}
	for i, input := range worker.faceInputs {
		config, _, err := image.DecodeConfig(bytes.NewReader(input))
		if err != nil || config != worker.inputs[i] {
			t.Fatalf("face input %d = %+v, %v; want the detection's input %+v", i, config, err, worker.inputs[i])
		}
	}
	found := people(t, service, alice, false)
	if len(found) != 2 {
		t.Fatalf("People() = %+v; want red and green", found)
	}
	red, green := found[0], found[1]
	if red.Photos != 4 || green.Photos != 3 || red.Kind != photos.PersonKindPerson || !red.CanChange || red.CoverID == "" {
		t.Fatalf("People() = %+v; want red in 4 photos, then green in 3", found)
	}
	if got := personPhotos(t, service, alice, red.ID); !slices.Equal(got, []string{"1.png", "2.png", "3.png", "4.png"}) {
		t.Fatalf("red's photos = %v", got)
	}
	if got := personPhotos(t, service, alice, green.ID); !slices.Equal(got, []string{"2.png", "3.png", "4.png"}) {
		t.Fatalf("green's photos = %v", got)
	}
	avatar, err := service.OccurrenceAvatar(context.Background(), alice, red.CoverID)
	if err != nil {
		t.Fatalf("OccurrenceAvatar() error = %v", err)
	}
	if config, _, err := image.DecodeConfig(bytes.NewReader(avatar)); err != nil || config.Width != config.Height {
		t.Fatalf("avatar = %+v, %v; want a square JPEG", config, err)
	}
	// Nobody else sees alice's private people, their photos or faces.
	if got := people(t, service, bob, false); len(got) != 0 {
		t.Fatalf("bob's People() = %+v", got)
	}
	if _, err := service.PersonPhotos(context.Background(), bob, red.ID, "", 10); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("bob's PersonPhotos() error = %v, want not found", err)
	}
	if _, err := service.OccurrenceAvatar(context.Background(), bob, red.CoverID); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("bob's OccurrenceAvatar() error = %v, want not found", err)
	}
}

func TestTwoFacesOfOnePhotoNeverShareAPerson(t *testing.T) {
	service, _ := newPeopleService(t)
	private, _ := libraries(t, service, alice)
	worker := newFacingEmbedder()
	// Two red faces in each photo: twins, or one person twice.
	for i := range 3 {
		importPhoto(t, service, alice, private.ID, "", string(rune('a'+i))+".png", peoplePhoto(t, uint8(i), 0, 0))
	}
	organise(t, service, worker)
	for _, person := range people(t, service, alice, true) {
		if person.Photos != len(personPhotos(t, service, alice, person.ID)) {
			t.Fatalf("person %+v counts photos twice", person)
		}
		asset, err := service.PersonPhotos(context.Background(), alice, person.ID, "", 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, photo := range asset.Assets {
			details, err := service.Get(context.Background(), alice, photo.ID)
			if err != nil {
				t.Fatal(err)
			}
			holding := 0
			for _, face := range details.People {
				if face.PersonID == person.ID {
					holding++
				}
			}
			if holding != 1 {
				t.Fatalf("%s holds %s %d times", photo.Name, person.ID, holding)
			}
		}
	}
}

func TestNamesMergesAndRemovalsSurviveARescan(t *testing.T) {
	ctx := context.Background()
	service, _ := newPeopleService(t)
	private, _ := libraries(t, service, alice)
	worker := newFacingEmbedder()
	var names []string
	for i := range 4 {
		name := string(rune('a'+i)) + ".png"
		names = append(names, name)
		importPhoto(t, service, alice, private.ID, "", name, peoplePhoto(t, uint8(i), 0))
	}
	organise(t, service, worker)
	found := people(t, service, alice, false)
	if len(found) != 1 {
		t.Fatalf("People() = %+v; want red", found)
	}
	red := found[0]
	name := "小明"
	if _, err := service.UpdatePerson(ctx, alice, red.ID, photos.PersonChange{Name: &name}); err != nil {
		t.Fatalf("UpdatePerson() error = %v", err)
	}
	page, err := service.PersonPhotos(ctx, alice, red.ID, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	var removed photos.Asset
	for _, asset := range page.Assets {
		if asset.Name == "d.png" {
			removed = asset
		}
	}
	if err := service.RemoveFromPerson(ctx, alice, red.ID, removed.ID); err != nil {
		t.Fatalf("RemoveFromPerson() error = %v", err)
	}
	// Clustering may not give the photo back.
	organise(t, service, worker)
	if got := personPhotos(t, service, alice, red.ID); !slices.Equal(got, names[:3]) {
		t.Fatalf("小明's photos after removal = %v", got)
	}

	// New face models rescan every photo; who is who carries over.
	worker.setFaceModel("faces-2")
	organise(t, service, worker)
	found = people(t, service, alice, false)
	if len(found) != 1 || found[0].ID != red.ID || found[0].Name != "小明" {
		t.Fatalf("People() after the rescan = %+v; want 小明 kept", found)
	}
	if got := personPhotos(t, service, alice, red.ID); !slices.Equal(got, names[:3]) {
		t.Fatalf("小明's photos after the rescan = %v", got)
	}
	details, err := service.Get(ctx, alice, removed.ID)
	if err != nil || len(details.People) != 1 || details.People[0].PersonID != "" {
		t.Fatalf("removed photo's people = %+v, %v; want its face with nobody", details.People, err)
	}

	// Saying who the face is puts it back, and naming a new person works.
	details, err = service.AssignOccurrence(ctx, alice, removed.ID, details.People[0].OccurrenceID, red.ID, "")
	if err != nil || details.People[0].Name != "小明" {
		t.Fatalf("AssignOccurrence() = %+v, %v", details.People, err)
	}
	if got := personPhotos(t, service, alice, red.ID); !slices.Equal(got, names) {
		t.Fatalf("小明's photos after assigning = %v", got)
	}
}

func TestMergingPeopleKeepsTheNameAndEveryPhoto(t *testing.T) {
	ctx := context.Background()
	service, _ := newPeopleService(t)
	private, _ := libraries(t, service, alice)
	worker := newFacingEmbedder()
	for i := range 3 {
		importPhoto(t, service, alice, private.ID, "", "red"+string(rune('a'+i))+".png", peoplePhoto(t, uint8(i), 0))
		importPhoto(t, service, alice, private.ID, "", "green"+string(rune('a'+i))+".png", peoplePhoto(t, uint8(i), 1))
	}
	organise(t, service, worker)
	found := people(t, service, alice, false)
	if len(found) != 2 {
		t.Fatalf("People() = %+v", found)
	}
	name := "奶奶"
	if _, err := service.UpdatePerson(ctx, alice, found[1].ID, photos.PersonChange{Name: &name}); err != nil {
		t.Fatal(err)
	}
	merged, err := service.MergePeople(ctx, alice, found[1].ID, found[0].ID)
	if err != nil || merged.Name != "奶奶" || merged.Photos != 6 {
		t.Fatalf("MergePeople() = %+v, %v; want 奶奶 in 6 photos", merged, err)
	}
	if got := people(t, service, alice, false); len(got) != 1 {
		t.Fatalf("People() after merging = %+v", got)
	}
	hide := true
	if _, err := service.UpdatePerson(ctx, alice, merged.ID, photos.PersonChange{Hidden: &hide}); err != nil {
		t.Fatal(err)
	}
	if got := people(t, service, alice, false); len(got) != 0 {
		t.Fatalf("People() lists a hidden person: %+v", got)
	}
	if got := people(t, service, alice, true); len(got) != 1 || !got[0].Hidden {
		t.Fatalf("People(hidden) = %+v", got)
	}
}

func TestEveryMemberMaintainsTheSharedLibrarysPeople(t *testing.T) {
	ctx := context.Background()
	service, _ := newPeopleService(t)
	aliceOwn, shared := libraries(t, service, alice)
	worker := newFacingEmbedder()
	for i := range 3 {
		importPhoto(t, service, alice, shared.ID, "", string(rune('a'+i))+".png", peoplePhoto(t, uint8(i), 2))
		importPhoto(t, service, alice, aliceOwn.ID, "", string(rune('a'+i))+".png", peoplePhoto(t, uint8(i+10), 3))
	}
	organise(t, service, worker)
	seen := people(t, service, bob, false)
	if len(seen) != 1 || seen[0].LibraryID != shared.ID || !seen[0].CanChange {
		t.Fatalf("bob's People() = %+v; want the shared library's person, changeable", seen)
	}
	name := "旺财"
	if _, err := service.UpdatePerson(ctx, bob, seen[0].ID, photos.PersonChange{Name: &name}); err != nil {
		t.Fatalf("bob's UpdatePerson() error = %v", err)
	}
	// An administrator viewing alice's private library sees her people but
	// may not change them.
	grant := photos.Principal{UserID: admin.UserID, Admin: true, Viewing: []photos.ViewingGrant{
		{GrantID: "grant:1", OwnerUserID: alice.UserID, ExpiresAt: newClock().Now().Add(1 << 40)}}}
	viewed, err := service.People(ctx, grant, aliceOwn.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	var hers *photos.Person
	for i := range viewed {
		if viewed[i].LibraryID == aliceOwn.ID {
			hers = &viewed[i]
		}
	}
	if hers == nil || hers.CanChange {
		t.Fatalf("viewing admin's People() = %+v; want alice's person, read-only", viewed)
	}
	if _, err := service.UpdatePerson(ctx, grant, hers.ID, photos.PersonChange{Name: &name}); !errors.Is(err, photos.ErrForbidden) {
		t.Fatalf("viewing admin's UpdatePerson() error = %v, want forbidden", err)
	}
	if _, err := service.UpdatePerson(ctx, bob, hers.ID, photos.PersonChange{Name: &name}); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("bob's UpdatePerson() on alice's person error = %v, want not found", err)
	}
}

func TestFacesWithoutACalibrationFindNobody(t *testing.T) {
	service, _, _ := newService(t)
	private, _ := libraries(t, service, alice)
	worker := newFacingEmbedder()
	for i := range 3 {
		importPhoto(t, service, alice, private.ID, "", string(rune('a'+i))+".png", peoplePhoto(t, uint8(i), 0))
	}
	organise(t, service, worker)
	if len(worker.faceInputs) != 3 {
		t.Fatalf("face scans = %d; want every photo scanned", len(worker.faceInputs))
	}
	if got := people(t, service, alice, true); len(got) != 0 {
		t.Fatalf("People() = %+v; want nobody without a calibration", got)
	}
}
