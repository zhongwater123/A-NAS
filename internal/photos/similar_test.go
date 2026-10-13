package photos_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/photos"
)

func similar(t *testing.T, service *photos.Service, p photos.Principal, assetID string, request photos.SimilarRequest) photos.SimilarPage {
	t.Helper()
	page, err := service.Similar(context.Background(), p, assetID, request)
	if err != nil {
		t.Fatalf("Similar(%s) error = %v", assetID, err)
	}
	return page
}

func TestSimilarPhotosAreClosestByImageAndShareObjects(t *testing.T) {
	service, worker := objectSearchService(t, "detector-1")
	private, shared := libraries(t, service, alice)
	source := importPhoto(t, service, alice, private.ID, "", "red.png", red)
	importPhoto(t, service, alice, private.ID, "", "dark red.png", solidPNG(160, 20, 20))
	importPhoto(t, service, alice, private.ID, "", "blue.png", blue)
	importPhoto(t, service, alice, shared.ID, "", "green.png", green)
	// A duplicate of the source is the same photo, not a similar one.
	importPhoto(t, service, alice, shared.ID, "", "red again.png", red)
	if before := similar(t, service, alice, source.ID, photos.SimilarRequest{}); before.Ready || len(before.Assets) != 0 {
		t.Fatalf("before local AI = %+v; want not ready and empty", before)
	}
	index(t, service, worker)

	page := similar(t, service, alice, source.ID, photos.SimilarRequest{})
	got := names(page.Assets)
	if !page.Ready || len(got) != 3 || got[0] != "dark red.png" || slices.Contains(got, "red again.png") {
		t.Fatalf("similar = %v (ready %v); want the other red first and never the duplicate", got, page.Ready)
	}

	// Pages continue where the last one ended.
	var paged []string
	cursor := ""
	for {
		page := similar(t, service, alice, source.ID, photos.SimilarRequest{Cursor: cursor, Limit: 1})
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

func TestSimilarPhotosStayInTheSearchScope(t *testing.T) {
	service, worker := objectSearchService(t, "detector-1")
	alicePrivate, _ := libraries(t, service, alice)
	bobPrivate, _ := libraries(t, service, bob)
	source := importPhoto(t, service, alice, alicePrivate.ID, "", "red.png", red)
	importPhoto(t, service, alice, alicePrivate.ID, "", "blue.png", blue)
	bobs := importPhoto(t, service, bob, bobPrivate.ID, "", "bob red.png", red)
	importPhoto(t, service, bob, bobPrivate.ID, "", "bob dark red.png", solidPNG(160, 20, 20))
	index(t, service, worker)

	if got := names(similar(t, service, alice, source.ID, photos.SimilarRequest{}).Assets); !slices.Equal(got, []string{"blue.png"}) {
		t.Fatalf("alice's similar = %v; bob's photos must never appear", got)
	}
	if _, err := service.Similar(context.Background(), alice, bobs.ID, photos.SimilarRequest{}); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("Similar(bob's photo) error = %v, want not found", err)
	}
	if _, err := service.Trash(context.Background(), alice, source.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Similar(context.Background(), alice, source.ID, photos.SimilarRequest{}); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("Similar(a trashed photo) error = %v, want not found", err)
	}
}
