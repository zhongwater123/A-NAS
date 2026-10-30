package photos_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/photos"
)

// The family center asks which of a channel's photos a reader can see; the
// answer is the photo Policy's, whoever asks.
func TestLookupAnswersByTheReadersPolicy(t *testing.T) {
	service, _, _ := newService(t)
	ctx := context.Background()
	alicePrivate, shared := libraries(t, service, alice)
	inShared := importPhoto(t, service, alice, shared.ID, "", "shared.png", pngBytes(t, 1))
	inPrivate := importPhoto(t, service, alice, alicePrivate.ID, "", "private.png", pngBytes(t, 2))
	trashed := importPhoto(t, service, alice, shared.ID, "", "trashed.png", pngBytes(t, 3))
	if _, err := service.Trash(ctx, alice, trashed.ID); err != nil {
		t.Fatal(err)
	}
	ids := []string{inShared.ID, inPrivate.ID, trashed.ID, "photo:missing", inShared.ID}

	found := lookup(t, service, alice, ids)
	if len(found) != 2 || !found[inShared.ID] || found[inPrivate.ID] {
		t.Fatalf("alice sees %v, want the shared photo as shared and her private one as not", found)
	}
	if found := lookup(t, service, bob, ids); len(found) != 1 || !found[inShared.ID] {
		t.Fatalf("bob sees %v, want only the shared photo", found)
	}
	if _, err := service.Lookup(ctx, photos.Principal{}, ids); !errors.Is(err, photos.ErrForbidden) {
		t.Fatalf("a lookup without a caller = %v", err)
	}
	many := make([]string, photos.MaxLookup+1)
	for i := range many {
		many[i] = fmt.Sprintf("photo:%d", i)
	}
	if _, err := service.Lookup(ctx, alice, many); !errors.Is(err, photos.ErrTooManyIDs) {
		t.Fatalf("a lookup of %d photos = %v", len(many), err)
	}
}

// lookup returns, for each photo p sees, whether it is in the shared library.
func lookup(t *testing.T, service *photos.Service, p photos.Principal, ids []string) map[string]bool {
	t.Helper()
	items, err := service.Lookup(context.Background(), p, ids)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, item := range items {
		found[item.Asset.ID] = item.Shared
	}
	return found
}
