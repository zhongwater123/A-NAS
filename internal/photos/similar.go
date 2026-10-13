package photos

import (
	"context"
	"database/sql"
	"errors"
	"slices"
)

// Similar photos (docs/architecture/photo-objects.md#以图搜图): the photos
// whose image vectors are closest to one photo's, with a small bonus for the
// objects the detector found in both. On COCO the bonus raised the precision
// of the first ten from 78% to 82% (ai/eval/image_search.py); it stays small
// because similarity there was judged by objects, which the detector sees
// best. No AI Worker is needed: the vectors and boxes are stored.

const (
	// similarObjectWeight scales the overlap of detected objects.
	similarObjectWeight = 0.1
	// similarObjectScore is the detector score from which an object counts.
	similarObjectScore = 0.3
)

// SimilarRequest asks for a page of the photos like one photo, or like the
// image of an image search.
type SimilarRequest struct {
	// Viewing names a member's private library the caller is viewing through
	// an unexpired grant, to look in it as well.
	Viewing string
	Cursor  string
	Limit   int
}

// SimilarPage is one page of similar photos, closest first.
type SimilarPage struct {
	Assets []Asset
	// Next continues the results; empty on the last page.
	Next string
	// Ready is false while the photo has no vector yet; there are no
	// results then.
	Ready bool
}

// Similar returns the photos in the search's scope most like one photo of
// it, at most maxSemanticResults of them, never its copies or duplicates.
// A photo outside the scope or in the trash is not found, as for a missing
// one.
func (s *Service) Similar(ctx context.Context, p Principal, assetID string, request SimilarRequest) (SimilarPage, error) {
	after, err := decodeSearchCursor(request.Cursor)
	if err != nil {
		return SimilarPage{}, err
	}
	scope, err := s.searchScope(ctx, p, request.Viewing)
	if err != nil {
		return SimilarPage{}, err
	}
	source, err := s.visibleAsset(ctx, s.db, p, assetID)
	if err != nil {
		return SimilarPage{}, err
	}
	if source.Trash != nil || !slices.Contains(scope, any(source.LibraryID)) {
		return SimilarPage{}, ErrNotFound
	}
	page := SimilarPage{Assets: []Asset{}}
	var model string
	err = s.db.QueryRowContext(ctx, "SELECT model FROM embeddings WHERE object_id = ? AND derivation = ?",
		source.objectID, embeddingDerivation).Scan(&model)
	if errors.Is(err, sql.ErrNoRows) {
		return page, nil
	}
	if err != nil {
		return SimilarPage{}, err
	}
	candidates, err := s.searchCandidates(ctx, scope)
	if err != nil {
		return SimilarPage{}, err
	}
	var hits []searchHit
	var vectorErr error
	err = s.detected.read(ctx, s, s.objectCalibration.Model, func(found map[string][]classScore) {
		vectorErr = s.index.read(ctx, s, model, func(vectors map[string][]float32) {
			vector, ok := vectors[source.objectID]
			if !ok {
				return
			}
			page.Ready = true
			hits = likeHits(candidates, vectors, found, vector, found[source.objectID], source.objectID)
		})
	})
	if err = errors.Join(err, vectorErr); err != nil {
		return SimilarPage{}, err
	}
	page.Assets, page.Next, err = s.hitPage(ctx, p, hits, after, request.Limit)
	if err != nil {
		return SimilarPage{}, err
	}
	return page, nil
}

// likeHits ranks the candidates with a vector by how much they look like an
// image with this vector and these objects, closest first and at most
// maxSemanticResults of them. Photos of the content object exclude, the
// image's own, are left out.
func likeHits(candidates []candidate, vectors map[string][]float32, found map[string][]classScore,
	vector []float32, objects []classScore, exclude string) []searchHit {
	var hits []searchHit
	for _, c := range candidates {
		other, ok := vectors[c.objectID]
		if !ok || c.objectID == exclude {
			continue
		}
		score := dot(vector, other) + similarObjectWeight*objectOverlap(objects, found[c.objectID])
		hits = append(hits, searchHit{c.id, score})
	}
	sortHits(hits)
	return hits[:min(len(hits), maxSemanticResults)]
}

// objectOverlap is the weighted Jaccard index of the objects two photos
// show: the shared best scores over all of them, counting objects from
// similarObjectScore. Both lists are ordered by class.
func objectOverlap(a, b []classScore) float64 {
	var shared, union float64
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		var x, y float32
		switch {
		case j == len(b) || (i < len(a) && a[i].class < b[j].class):
			x = a[i].score
			i++
		case i == len(a) || b[j].class < a[i].class:
			y = b[j].score
			j++
		default:
			x, y = a[i].score, b[j].score
			i++
			j++
		}
		if x < similarObjectScore {
			x = 0
		}
		if y < similarObjectScore {
			y = 0
		}
		shared += float64(min(x, y))
		union += float64(max(x, y))
	}
	if union == 0 {
		return 0
	}
	return shared / union
}
