package photos_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/photos"
)

// colorEmbedder is an AI Worker that sees only colour: an image's vector is
// its mean colour, and a query naming red, green or blue points at that
// colour. Search results are then predictable.
type colorEmbedder struct {
	mu          sync.Mutex
	unavailable bool
}

func (c *colorEmbedder) Info(context.Context) (photos.EmbedderInfo, error) {
	if c.isUnavailable() {
		return photos.EmbedderInfo{}, photos.ErrAIUnavailable
	}
	return photos.EmbedderInfo{Model: "colour-1", Dimensions: 3}, nil
}

func (c *colorEmbedder) EmbedImage(_ context.Context, file *os.File) ([]float32, error) {
	img, err := jpeg.Decode(file)
	if err != nil {
		return nil, photos.ErrAIRejected
	}
	var r, g, b float64
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			cr, cg, cb, _ := img.At(x, y).RGBA()
			r, g, b = r+float64(cr), g+float64(cg), b+float64(cb)
		}
	}
	return unit(r, g, b), nil
}

func (c *colorEmbedder) EmbedQuery(_ context.Context, text string) ([]float32, error) {
	if c.isUnavailable() {
		return nil, photos.ErrAIUnavailable
	}
	switch {
	case strings.Contains(text, "红"):
		return unit(1, 0, 0), nil
	case strings.Contains(text, "绿"):
		return unit(0, 1, 0), nil
	case strings.Contains(text, "蓝"):
		return unit(0, 0, 1), nil
	}
	return unit(1, 1, 1), nil
}

func (c *colorEmbedder) isUnavailable() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.unavailable
}

func (c *colorEmbedder) setUnavailable(unavailable bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.unavailable = unavailable
}

func unit(r, g, b float64) []float32 {
	norm := math.Sqrt(r*r + g*g + b*b)
	return []float32{float32(r / norm), float32(g / norm), float32(b / norm)}
}

func solidPNG(r, g, b uint8) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 8, 6))
	for x := range 8 {
		for y := range 6 {
			img.Set(x, y, color.RGBA{R: r, G: g, B: b, A: 0xff})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		panic(err)
	}
	return buffer.Bytes()
}

var (
	red   = solidPNG(220, 30, 30)
	green = solidPNG(30, 200, 30)
	blue  = solidPNG(20, 20, 220)
)

func searchService(t *testing.T) (*photos.Service, *colorEmbedder, *clock) {
	t.Helper()
	c := newClock()
	embedder := &colorEmbedder{}
	service, err := photos.Open(filepath.Join(t.TempDir(), "photos"), photos.Options{Now: c.Now, DisableCapacityReserve: true, AI: embedder})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service, embedder, c
}

// index renders the thumbnails and vectors of everything imported so far.
func index(t *testing.T, service *photos.Service, embedder photos.Embedder) {
	t.Helper()
	processAll(t, service)
	processAI(t, service, embedder, &fakeGate{open: true})
}

func search(t *testing.T, service *photos.Service, p photos.Principal, query string) photos.SearchPage {
	t.Helper()
	page, err := service.Search(context.Background(), p, photos.SearchRequest{Query: query})
	if err != nil {
		t.Fatalf("Search(%q) error = %v", query, err)
	}
	return page
}

func names(assets []photos.Asset) []string {
	result := make([]string, len(assets))
	for i, asset := range assets {
		result[i] = asset.Name
	}
	return result
}

func TestSearchRanksTheCallersPhotosByMeaning(t *testing.T) {
	service, embedder, _ := searchService(t)
	alicePrivate, shared := libraries(t, service, namedAlice)
	bobPrivate, _ := libraries(t, service, namedBob)
	importPhoto(t, service, namedAlice, alicePrivate.ID, "", "a.png", red)
	importPhoto(t, service, namedAlice, alicePrivate.ID, "", "b.png", blue)
	importPhoto(t, service, namedBob, shared.ID, "", "c.png", green)
	importPhoto(t, service, namedBob, bobPrivate.ID, "", "bobs red.png", solidPNG(230, 20, 20))
	trashed := importPhoto(t, service, namedAlice, alicePrivate.ID, "", "d.png", solidPNG(200, 40, 40))
	if _, err := service.Trash(context.Background(), namedAlice, trashed.ID); err != nil {
		t.Fatal(err)
	}
	index(t, service, embedder)

	page := search(t, service, namedAlice, "红色的花")
	if !page.Semantic {
		t.Fatal("Semantic = false with the Worker available")
	}
	// Bob's private photo and the trashed one never appear, however close.
	if got := names(page.Assets); !slices.Equal(got, []string{"a.png", "c.png", "b.png"}) {
		t.Fatalf("results = %v, want Alice's red photo first and only what she can see", got)
	}
	if got := names(search(t, service, namedAlice, "蓝天").Assets); got[0] != "b.png" {
		t.Fatalf("blue query ranks %v", got)
	}
	if got := names(search(t, service, namedBob, "红色").Assets); !slices.Equal(got, []string{"bobs red.png", "c.png"}) {
		t.Fatalf("Bob's results = %v, want his own and the shared photo only", got)
	}
}

func TestSearchPutsNameMatchesFirstAndFallsBackToNames(t *testing.T) {
	service, embedder, _ := searchService(t)
	private, _ := libraries(t, service, namedAlice)
	importPhoto(t, service, namedAlice, private.ID, "", "red.png", red)
	importPhoto(t, service, namedAlice, private.ID, "", "蓝色的车.png", green)
	importPhoto(t, service, namedAlice, private.ID, "", "sea.png", blue)
	index(t, service, embedder)

	if got := names(search(t, service, namedAlice, "蓝色").Assets); !slices.Equal(got, []string{"蓝色的车.png", "sea.png", "red.png"}) {
		t.Fatalf("results = %v, want the name match before the closest colour", got)
	}
	embedder.setUnavailable(true)
	page := search(t, service, namedAlice, "蓝色")
	if page.Semantic || !slices.Equal(names(page.Assets), []string{"蓝色的车.png"}) {
		t.Fatalf("without the Worker: semantic=%v results=%v, want names only", page.Semantic, names(page.Assets))
	}
	if page := search(t, service, namedAlice, "RED"); !slices.Equal(names(page.Assets), []string{"red.png"}) {
		t.Fatalf("name matching ignores case: %v", names(page.Assets))
	}
}

func TestSearchPagesWithoutRepeatsAndRejectsBadInput(t *testing.T) {
	service, embedder, _ := searchService(t)
	private, _ := libraries(t, service, namedAlice)
	for i, content := range [][]byte{red, green, blue, solidPNG(10, 120, 240), solidPNG(240, 120, 10)} {
		importPhoto(t, service, namedAlice, private.ID, "", string(rune('a'+i))+".png", content)
	}
	index(t, service, embedder)
	ctx := context.Background()

	all := names(search(t, service, namedAlice, "绿").Assets)
	var paged []string
	cursor := ""
	for range 10 {
		page, err := service.Search(ctx, namedAlice, photos.SearchRequest{Query: "绿", Cursor: cursor, Limit: 2})
		if err != nil {
			t.Fatalf("Search() page error = %v", err)
		}
		paged = append(paged, names(page.Assets)...)
		if cursor = page.Next; cursor == "" {
			break
		}
	}
	if !slices.Equal(paged, all) || len(all) != 5 {
		t.Fatalf("pages = %v, want %v", paged, all)
	}
	for _, query := range []string{"", "   ", strings.Repeat("猫", photos.MaxQueryRunes+1)} {
		if _, err := service.Search(ctx, namedAlice, photos.SearchRequest{Query: query}); !errors.Is(err, photos.ErrInvalidQuery) {
			t.Fatalf("Search(%d runes) error = %v, want ErrInvalidQuery", len([]rune(query)), err)
		}
	}
	if _, err := service.Search(ctx, namedAlice, photos.SearchRequest{Query: "绿", Cursor: "not a cursor"}); !errors.Is(err, photos.ErrInvalidCursor) {
		t.Fatalf("Search() with a bad cursor error = %v", err)
	}
	if _, err := service.Search(ctx, photos.Principal{}, photos.SearchRequest{Query: "绿"}); !errors.Is(err, photos.ErrForbidden) {
		t.Fatalf("Search() without a principal error = %v", err)
	}
}

func TestSearchFollowsNewVectorsAndPurges(t *testing.T) {
	service, embedder, _ := searchService(t)
	ctx := context.Background()
	alicePrivate, _ := libraries(t, service, namedAlice)
	first := importPhoto(t, service, namedAlice, alicePrivate.ID, "", "first.png", red)
	index(t, service, embedder)
	if got := names(search(t, service, namedAlice, "红").Assets); !slices.Equal(got, []string{"first.png"}) {
		t.Fatalf("results = %v", got)
	}

	// The index is loaded now; a photo indexed later still shows up.
	importPhoto(t, service, namedAlice, alicePrivate.ID, "", "second.png", solidPNG(250, 10, 10))
	index(t, service, embedder)
	if got := names(search(t, service, namedAlice, "红").Assets); len(got) != 2 {
		t.Fatalf("results after a new vector = %v", got)
	}
	if _, err := service.Trash(ctx, namedAlice, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.Purge(ctx, namedAlice, first.ID); err != nil {
		t.Fatal(err)
	}
	if got := names(search(t, service, namedAlice, "红").Assets); !slices.Equal(got, []string{"second.png"}) {
		t.Fatalf("results after a purge = %v", got)
	}

}

// An administrator searches a member's private library only while viewing it,
// and only when asking for it; ordinary searches never mix it in.
func TestSearchAddsAViewedLibraryOnlyOnRequest(t *testing.T) {
	service, embedder, c := searchService(t)
	ctx := context.Background()
	alicePrivate, shared := libraries(t, service, namedAlice)
	importPhoto(t, service, namedAlice, alicePrivate.ID, "", "alice.png", red)
	importPhoto(t, service, namedBob, shared.ID, "", "shared.png", solidPNG(200, 50, 50))
	index(t, service, embedder)
	admin := photos.Principal{UserID: "user:admin", Admin: true, Viewing: []photos.ViewingGrant{
		{GrantID: "grant:1", OwnerUserID: namedAlice.UserID, ExpiresAt: c.Now().Add(time.Hour)},
	}}
	viewingAlice := func(p photos.Principal) ([]string, error) {
		page, err := service.Search(ctx, p, photos.SearchRequest{Query: "红", Viewing: alicePrivate.ID})
		return names(page.Assets), err
	}

	if got := names(search(t, service, admin, "红").Assets); !slices.Equal(got, []string{"shared.png"}) {
		t.Fatalf("an ordinary search by a viewing administrator found %v", got)
	}
	if got, err := viewingAlice(admin); err != nil || !slices.Equal(got, []string{"alice.png", "shared.png"}) {
		t.Fatalf("searching the viewed library = %v, %v", got, err)
	}
	if _, err := viewingAlice(namedBob); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("a member naming another member's library error = %v, want ErrNotFound", err)
	}
	if got, err := viewingAlice(namedAlice); err != nil || !slices.Equal(got, []string{"alice.png", "shared.png"}) {
		t.Fatalf("the owner naming her own library = %v, %v", got, err)
	}
	c.Advance(2 * time.Hour)
	if _, err := viewingAlice(admin); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("an expired grant error = %v, want ErrNotFound", err)
	}
}
