package photos_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/photos"
)

func searchByImage(t *testing.T, service *photos.Service, p photos.Principal, image []byte) string {
	t.Helper()
	id, err := service.SearchByImage(context.Background(), p, bytes.NewReader(image))
	if err != nil {
		t.Fatalf("SearchByImage() error = %v", err)
	}
	return id
}

func imageResults(t *testing.T, service *photos.Service, p photos.Principal, id string, request photos.SimilarRequest) photos.Page {
	t.Helper()
	page, err := service.ImageSearchResults(context.Background(), p, id, request)
	if err != nil {
		t.Fatalf("ImageSearchResults() error = %v", err)
	}
	return page
}

// Red photos show a phone to colorDetector, so a photo a little less red
// than another but with a phone in it comes first, as for similar photos.
var (
	reddishWithPhone = solidPNG(150, 70, 70)
	reddishPlain     = solidPNG(110, 45, 45)
)

func TestAnImageFindsThePhotosLikeItByImageAndObjects(t *testing.T) {
	service, worker := objectSearchService(t, "detector-1")
	private, shared := libraries(t, service, alice)
	importPhoto(t, service, alice, private.ID, "", "phone.png", reddishWithPhone)
	importPhoto(t, service, alice, private.ID, "", "plain.png", reddishPlain)
	importPhoto(t, service, alice, private.ID, "", "blue.png", blue)
	importPhoto(t, service, alice, shared.ID, "", "red.png", red)
	index(t, service, worker)

	id := searchByImage(t, service, alice, red)
	got := names(imageResults(t, service, alice, id, photos.SimilarRequest{}).Assets)
	// The same picture in the library is no copy of the image and comes
	// first; it is not left out as a similar photo's own copies are.
	if want := []string{"red.png", "phone.png", "plain.png", "blue.png"}; !slices.Equal(got, want) {
		t.Fatalf("results = %v, want %v", got, want)
	}

	// Pages continue where the last one ended.
	var paged []string
	cursor := ""
	for {
		page := imageResults(t, service, alice, id, photos.SimilarRequest{Cursor: cursor, Limit: 1})
		paged = append(paged, names(page.Assets)...)
		if page.Next == "" {
			break
		}
		cursor = page.Next
	}
	if !slices.Equal(paged, got) {
		t.Fatalf("paged %v, want %v", paged, got)
	}
}

func TestAnImageIsMatchedByItsVectorAloneWithoutTheCalibratedDetector(t *testing.T) {
	service, worker := objectSearchService(t, "another-detector")
	private, _ := libraries(t, service, alice)
	importPhoto(t, service, alice, private.ID, "", "phone.png", reddishWithPhone)
	importPhoto(t, service, alice, private.ID, "", "plain.png", reddishPlain)
	index(t, service, worker)

	id := searchByImage(t, service, alice, red)
	if got := names(imageResults(t, service, alice, id, photos.SimilarRequest{}).Assets); !slices.Equal(got, []string{"plain.png", "phone.png"}) {
		t.Fatalf("results = %v; the redder photo must come first without objects", got)
	}
}

func TestAnImageSearchStaysWithItsMemberAndTheirScope(t *testing.T) {
	service, worker := objectSearchService(t, "detector-1")
	alicePrivate, shared := libraries(t, service, alice)
	bobPrivate, _ := libraries(t, service, bob)
	importPhoto(t, service, alice, alicePrivate.ID, "", "alice.png", red)
	importPhoto(t, service, bob, shared.ID, "", "shared.png", blue)
	importPhoto(t, service, bob, bobPrivate.ID, "", "bob.png", red)
	index(t, service, worker)

	id := searchByImage(t, service, alice, red)
	if got := names(imageResults(t, service, alice, id, photos.SimilarRequest{}).Assets); !slices.Equal(got, []string{"alice.png", "shared.png"}) {
		t.Fatalf("alice's results = %v; bob's private photos must never appear", got)
	}
	if _, err := service.ImageSearchResults(context.Background(), bob, id, photos.SimilarRequest{}); !errors.Is(err, photos.ErrImageSearchExpired) {
		t.Fatalf("bob reading alice's image search error = %v, want expired", err)
	}
	if _, err := service.ImageSearchResults(context.Background(), alice, id, photos.SimilarRequest{Viewing: bobPrivate.ID}); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("results viewing bob's library without a grant error = %v, want not found", err)
	}
	if _, err := service.SearchByImage(context.Background(), photos.Principal{}, bytes.NewReader(red)); !errors.Is(err, photos.ErrForbidden) {
		t.Fatalf("SearchByImage() without a caller error = %v, want forbidden", err)
	}
}

func TestImageSearchesAreKeptForAWhileAfterTheirLastUse(t *testing.T) {
	service, embedder, clock := searchService(t)
	private, _ := libraries(t, service, alice)
	importPhoto(t, service, alice, private.ID, "", "red.png", red)
	index(t, service, embedder)

	id := searchByImage(t, service, alice, red)
	for range 3 {
		clock.Advance(29 * time.Minute)
		if got := names(imageResults(t, service, alice, id, photos.SimilarRequest{}).Assets); !slices.Equal(got, []string{"red.png"}) {
			t.Fatalf("results after a while = %v", got)
		}
	}
	clock.Advance(31 * time.Minute)
	if _, err := service.ImageSearchResults(context.Background(), alice, id, photos.SimilarRequest{}); !errors.Is(err, photos.ErrImageSearchExpired) {
		t.Fatalf("results after 31 idle minutes error = %v, want expired", err)
	}

	// Only so many are kept: the least recently used goes first.
	first := searchByImage(t, service, alice, red)
	clock.Advance(time.Second)
	second := searchByImage(t, service, alice, blue)
	for range 63 {
		clock.Advance(time.Second)
		searchByImage(t, service, alice, green)
	}
	if _, err := service.ImageSearchResults(context.Background(), alice, first, photos.SimilarRequest{}); !errors.Is(err, photos.ErrImageSearchExpired) {
		t.Fatalf("the least recently used of 65 searches error = %v, want expired", err)
	}
	imageResults(t, service, alice, second, photos.SimilarRequest{})
}

func TestSearchByImageRefusesWhatItCannotRead(t *testing.T) {
	service, embedder, _ := searchService(t)
	ctx := context.Background()
	for name, content := range map[string][]byte{
		"text":         []byte("not an image at all"),
		"GIF":          []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;"),
		"broken PNG":   red[:40],
		"JPEG as junk": append([]byte{0xff, 0xd8, 0xff}, bytes.Repeat([]byte{0}, 64)...),
	} {
		if _, err := service.SearchByImage(ctx, alice, bytes.NewReader(content)); !errors.Is(err, photos.ErrUnsupportedType) {
			t.Errorf("SearchByImage(%s) error = %v, want unsupported type", name, err)
		}
	}
	if _, err := service.SearchByImage(ctx, alice, bytes.NewReader(pngHeader(10_000, 10_000))); !errors.Is(err, photos.ErrTooLarge) {
		t.Errorf("SearchByImage(100 megapixels) error = %v, want too large", err)
	}
	endless := io.LimitReader(io.MultiReader(bytes.NewReader(red), zeros{}), photos.MaxSearchImageBytes+1)
	if _, err := service.SearchByImage(ctx, alice, endless); !errors.Is(err, photos.ErrTooLarge) {
		t.Errorf("SearchByImage(over the byte limit) error = %v, want too large", err)
	}

	embedder.setUnavailable(true)
	if _, err := service.SearchByImage(ctx, alice, bytes.NewReader(red)); !errors.Is(err, photos.ErrAIUnavailable) {
		t.Errorf("SearchByImage() without the Worker error = %v, want AI unavailable", err)
	}
	withoutAI, err := photos.Open(filepath.Join(t.TempDir(), "photos"), photos.Options{DisableCapacityReserve: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = withoutAI.Close() })
	if _, err := withoutAI.SearchByImage(ctx, alice, bytes.NewReader(red)); !errors.Is(err, photos.ErrAIUnavailable) {
		t.Errorf("SearchByImage() without local AI error = %v, want AI unavailable", err)
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
