package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
	"github.com/zhongwater123/A-NAS/internal/videocutcompute"
)

func TestTimelineUsesEveryPlannedSlotAndOnlyAvailableFootage(t *testing.T) {
	run := &jobRun{
		analysis: analysisResult{Videos: []analysedVideo{{AssetID: "video", Duration: 12}}},
		catalog:  catalogResult{Cards: map[string]videocutcompute.CatalogCard{"card": {Video: "video", End: 12}, "filtered": {Video: "video", End: 12}}, Catalog: []string{"card"}},
		excerpt:  Excerpt{Frames: 120},
		planned:  json.RawMessage(`{"slots":[[[[0,2],[2,4]]]]}`),
	}
	base := []Clip{{Video: "video", Card: "card", Slot: [2]float64{0, 2}, Share: 2, End: 2}, {Video: "video", Card: "card", Slot: [2]float64{2, 4}, Start: 2, End: 4, Share: 2}}
	check := func(clips []Clip) error {
		answer := videocutcompute.FillAnswer{}
		for _, clip := range clips {
			raw, err := json.Marshal(clip)
			if err != nil {
				t.Fatal(err)
			}
			answer.Clips = append(answer.Clips, raw)
		}
		return validateTimeline(run, answer)
	}
	if err := check(base); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func([]Clip)
	}{
		{"repeated slot", func(c []Clip) { c[1].Slot = c[0].Slot }},
		{"slot order", func(c []Clip) { c[1], c[0] = c[0], c[1] }},
		{"unplanned lengths", func(c []Clip) { c[0].Slot[1], c[1].Slot[0] = 1, 1; c[0].Share, c[1].Share = 1, 3; c[1].End = 5 }},
		{"wrong frames per slot", func(c []Clip) { c[0].Share, c[1].Share = 1, 3; c[1].End = 5 }},
		{"filtered card", func(c []Clip) { c[0].Card = "filtered" }},
		{"source too short", func(c []Clip) { c[0].End = 1.5 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clips := slices.Clone(base)
			tc.mutate(clips)
			if err := check(clips); err == nil {
				t.Fatal("invalid timeline was accepted")
			}
		})
	}
}

func TestFilmNameFitsPhotoLibraryRules(t *testing.T) {
	for _, tc := range []struct{ project, want string }{
		{" ..上海/杭州\\旅行\x7f", "上海_杭州_旅行_ v2.mp4"},
		{" ... ", "剪辑 v2.mp4"},
		{strings.Repeat("🐟", 64), strings.Repeat("🐟", 62) + " v2.mp4"},
	} {
		got := filmName(tc.project, 2)
		if got != tc.want || len(got) > 255 || !utf8.ValidString(got) {
			t.Errorf("filmName(%q) = %q", tc.project, got)
		}
	}
}

type boundedJobGrants struct {
	Grants
	batches []int
}

func (g *boundedJobGrants) Lookup(_ context.Context, _ string, ids []string) ([]photos.GrantedAsset, error) {
	g.batches = append(g.batches, len(ids))
	var found []photos.GrantedAsset
	for _, id := range ids {
		found = append(found, photos.GrantedAsset{AssetID: id})
	}
	return found, nil
}
func (g *boundedJobGrants) Analyze(_ context.Context, _ string, ids []string, _ bool) error {
	g.batches = append(g.batches, len(ids))
	return nil
}

func TestJobGrantRequestsRespectPhotoBatchBound(t *testing.T) {
	grants := &boundedJobGrants{}
	s := &Store{grants: grants}
	ids := make([]string, photos.MaxGrantAssets+1)
	for i := range ids {
		ids[i] = fmt.Sprint(i)
	}
	found, err := s.lookupAssets(t.Context(), "grant", ids)
	if err != nil || len(found) != len(ids) || found[len(ids)-1].AssetID != ids[len(ids)-1] {
		t.Fatalf("lookup returned %d assets, %v", len(found), err)
	}
	if err := s.analyzeAssets(t.Context(), "grant", ids, true); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(grants.batches, []int{photos.MaxGrantAssets, 1, photos.MaxGrantAssets, 1}) {
		t.Fatal(grants.batches)
	}
	var wait *stepWait
	if !errors.As(jobGrantError(&photosapi.GrantError{Code: photosapi.GrantOwnerInactive}, ids), &wait) || wait.reason != WaitPhotos {
		t.Fatal("inactive owner must wait for the photo library")
	}
}

// everyCard is a compute Worker whose catalog keeps every card.
type everyCard struct{ Compute }

func (everyCard) Catalog(_ context.Context, request videocutcompute.CatalogRequest) ([]string, error) {
	ids := make([]string, len(request.Cards))
	for i, card := range request.Cards {
		ids[i] = card.ID
	}
	return ids, nil
}

// The fill sends the vector of every window beneath the catalog's cards in
// one request, which the compute Worker bounds: a catalog over the bound
// fails here with its reason, before anything is planned or read again.
func TestACatalogWhoseWindowsOutgrowTheComputeWorkerFailsTheJob(t *testing.T) {
	s := &Store{jobCompute: everyCard{}}
	catalog := func(dimensions int) error {
		run := &jobRun{analysis: analysisResult{Videos: []analysedVideo{{AssetID: "video", Dimensions: dimensions,
			Windows: []photos.Window{{StartMs: 0, EndMs: 2000}, {Index: 1, StartMs: 2000, EndMs: 4000}, {Index: 2, StartMs: 4000, EndMs: 6000}},
			Cards:   []videocutcompute.CatalogCard{{ID: "card", Video: "video", Start: 0, End: 4}}}}}}
		_, err := s.makeCatalog(t.Context(), run)
		return err
	}
	// Two of the three windows lie beneath the card.
	most := videocutcompute.MaxComputeArrayBytes / 4 / 2
	if err := catalog(most); err != nil {
		t.Fatalf("a catalog that just fits = %v", err)
	}
	var bad *stepFailed
	if err := catalog(most + 1); !errors.As(err, &bad) {
		t.Fatalf("a catalog one value over the bound = %v", err)
	}
}
