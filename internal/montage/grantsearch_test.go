package montage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
)

// The photo library's grants are what a store searches with.
var _ WindowSearch = photosapi.Grants{}

// grantCall answers searchWindows as the photo library would, through the
// request's JSON, or refuses with code.
func grantCall(t *testing.T, code string, asked *photosapi.GrantRequest) photosapi.Grants {
	return photosapi.Grants{Call: func(_ context.Context, request photosapi.GrantRequest) (photosapi.GrantResponse, *os.File, error) {
		*asked = request
		if request.Op != photosapi.GrantSearchWindows {
			t.Errorf("op %q, want %q", request.Op, photosapi.GrantSearchWindows)
		}
		switch code {
		case "":
		case "down":
			return photosapi.GrantResponse{}, nil, errors.New("dial unix: no such file")
		default:
			return photosapi.GrantResponse{}, nil, &photosapi.GrantError{Code: code, Message: "refused"}
		}
		matches := make([][]photos.WindowMatch, len(request.Texts))
		for i := range matches {
			matches[i] = []photos.WindowMatch{{AssetID: "video-" + request.Texts[i], Window: 7 + i, Score: 0.5}}
		}
		// Across the socket the answer is JSON.
		raw, err := json.Marshal(photosapi.GrantResponse{OK: true, Matches: matches})
		if err != nil {
			t.Fatal(err)
		}
		var response photosapi.GrantResponse
		if err := json.Unmarshal(raw, &response); err != nil {
			t.Fatal(err)
		}
		return response, nil, nil
	}}
}

func TestAStoreSearchesWithThePhotoLibrarysGrants(t *testing.T) {
	var asked photosapi.GrantRequest
	store, err := Open(t.Context(), t.TempDir(), Options{Grants: grantCall(t, "", &asked)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if store.search == nil {
		t.Fatal("a store whose grants search windows has no search")
	}
	hits, err := store.search.SearchWindows(t.Context(), "grant-1", []string{"a", "b"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if asked.GrantID != "grant-1" || !slices.Equal(asked.Texts, []string{"a", "b"}) || asked.K != 3 {
		t.Fatalf("asked %+v", asked)
	}
	want := [][]photos.WindowMatch{{{AssetID: "video-a", Window: 7, Score: 0.5}}, {{AssetID: "video-b", Window: 8, Score: 0.5}}}
	if len(hits) != len(want) || !slices.Equal(hits[0], want[0]) || !slices.Equal(hits[1], want[1]) {
		t.Fatalf("hits %+v, want %+v", hits, want)
	}
}

func TestAGivenSearchIsKept(t *testing.T) {
	var asked photosapi.GrantRequest
	given := &editSearch{}
	store, err := Open(t.Context(), t.TempDir(), Options{Grants: grantCall(t, "", &asked), Search: given})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if store.search != given {
		t.Fatalf("search %T, want the one given", store.search)
	}
}

func TestTheLibrarysRefusalsAreTheMontageCentersErrors(t *testing.T) {
	for _, c := range []struct {
		code string
		want error
	}{
		{photosapi.GrantModelMismatch, ErrSearchModel},
		{photosapi.GrantAIUnavailable, ErrSearchUnavailable},
		{photosapi.GrantUnavailable, ErrSearchUnavailable},
		{photosapi.GrantOwnerInactive, ErrSearchUnavailable},
		{"down", ErrSearchUnavailable},
	} {
		var asked photosapi.GrantRequest
		_, err := GrantSearch{Grants: grantCall(t, c.code, &asked)}.SearchWindows(t.Context(), "grant-1", []string{"a"}, 3)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.code, err, c.want)
		}
	}
	// The rest are what a request's handler answers, not errors of the
	// montage center's own (500): a text the library does not search for is
	// the request's fault, a revoked grant the project's, an ended session
	// the member's.
	for code, want := range map[string]error{photosapi.GrantInvalid: ErrInvalid, photosapi.GrantNotFound: ErrGrantRevoked,
		photosapi.GrantUnauthorized: ErrSessionEnded, "something_new": ErrPhotosUnavailable} {
		var asked photosapi.GrantRequest
		_, err := GrantSearch{Grants: grantCall(t, code, &asked)}.SearchWindows(t.Context(), "grant-1", []string{"a"}, 3)
		if !errors.Is(err, want) || errors.Is(err, ErrSearchUnavailable) || errors.Is(err, ErrSearchModel) {
			t.Errorf("%s: %v, want %v", code, err, want)
		}
	}
}
