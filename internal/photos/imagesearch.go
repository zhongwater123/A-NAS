package photos

import (
	"bytes"
	"context"
	"errors"
	"image"
	"io"
	"sync"
	"time"

	"github.com/disintegration/imaging"

	"github.com/zhongwater123/A-NAS/internal/decodepanic"
)

// Image search finds the photos like an image that is not in the library,
// such as one on the member's computer or phone
// (docs/architecture/photo-objects.md#用图库外的图片搜索). The image is
// rendered as a photo's thumbnail and detector input would be, the AI Worker
// encodes it and looks for objects in it, and the photos in the search's
// scope are ranked as for similar photos.
// The image itself is never stored, not even in staging: it stays in memory
// for the request and reaches the Worker as a sealed in-memory file. What
// search keeps is its vector and the objects found, until imageSearchIdle
// after its last page, so that later pages need not encode it again.

// ErrImageSearchExpired means an image search is not kept any more, or never
// was; sending the image again starts a new one.
var ErrImageSearchExpired = errors.New("the image search is no longer kept")

const (
	// MaxSearchImageBytes bounds an image to search by. The Web client sends
	// it scaled to the detector's input size, far below this.
	MaxSearchImageBytes = 32 << 20
	// maxSearchImagePixels bounds what such an image may decode to, well
	// below maxImagePixels: it is decoded while someone waits, in the
	// service's own memory.
	maxSearchImagePixels = 1 << 26
	// imageSearchIdle is how long an image search is kept after its last
	// use, and maxImageSearches how many are kept at most; the least
	// recently used goes first.
	imageSearchIdle  = 30 * time.Minute
	maxImageSearches = 64
)

// imageSearch is what search keeps of an image: its vector and the model
// that made it, and the best score per class of the objects in it, from the
// detector whose results search uses.
type imageSearch struct {
	owner    string
	model    string
	vector   []float32
	objects  []classScore
	lastUsed time.Time
}

type imageSearches struct {
	mu   sync.Mutex
	byID map[string]*imageSearch
	// encoding lets one image be decoded and encoded at a time: the Worker
	// serves one request at a time anyway, and a decoded image can take a
	// few hundred MB.
	encoding chan struct{}
}

// SearchByImage encodes an image to search the library with and returns the
// ID its results are listed under (ImageSearchResults). The image must be a
// JPEG or PNG of at most MaxSearchImageBytes and maxSearchImagePixels;
// ErrAIUnavailable means the AI Worker cannot encode it now.
func (s *Service) SearchByImage(ctx context.Context, p Principal, content io.Reader) (string, error) {
	if !p.valid() {
		return "", ErrForbidden
	}
	data, err := io.ReadAll(io.LimitReader(content, MaxSearchImageBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > MaxSearchImageBytes {
		return "", ErrTooLarge
	}
	select {
	case s.searches.encoding <- struct{}{}:
		defer func() { <-s.searches.encoding }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	thumbnail, detectorInput, err := renderSearchImage(data)
	if err != nil {
		return "", err
	}
	if s.embedder == nil {
		return "", ErrAIUnavailable
	}
	info, err := s.embedder.Info(ctx)
	if err != nil {
		return "", err
	}
	s.noticeModel(info.Model)
	vector, err := s.embedSearchImage(ctx, thumbnail)
	if err != nil {
		return "", err
	}
	if !validVector(vector, info.Dimensions) {
		return "", errors.New("the AI worker returned an invalid vector")
	}
	search := &imageSearch{owner: p.UserID, model: info.Model, vector: vector, objects: s.searchObjects(ctx, info, detectorInput)}
	id := s.randomHex()
	s.searches.add(id, search, s.now())
	return id, nil
}

// ImageSearchResults returns a page of the photos in the search's scope most
// like the image of an image search, closest first, ranked as similar photos
// are. Only the member who started the search can read it; for anyone else,
// as once it is no longer kept, it is ErrImageSearchExpired.
func (s *Service) ImageSearchResults(ctx context.Context, p Principal, searchID string, request SimilarRequest) (Page, error) {
	after, err := decodeSearchCursor(request.Cursor)
	if err != nil {
		return Page{}, err
	}
	scope, err := s.searchScope(ctx, p, request.Viewing)
	if err != nil {
		return Page{}, err
	}
	search, ok := s.searches.get(searchID, p.UserID, s.now())
	if !ok {
		return Page{}, ErrImageSearchExpired
	}
	candidates, err := s.searchCandidates(ctx, scope)
	if err != nil {
		return Page{}, err
	}
	var hits []searchHit
	var vectorErr error
	err = s.detected.read(ctx, s, s.objectCalibration.Model, func(found map[string][]classScore) {
		vectorErr = s.index.read(ctx, s, search.model, func(vectors map[string][]float32) {
			hits = likeHits(candidates, vectors, found, search.vector, search.objects, "")
		})
	})
	if err = errors.Join(err, vectorErr); err != nil {
		return Page{}, err
	}
	assets, next, err := s.hitPage(ctx, p, hits, after, request.Limit)
	if err != nil {
		return Page{}, err
	}
	return Page{Assets: assets, Next: next}, nil
}

// renderSearchImage renders an image to search by as the library renders a
// photo for local AI: the upright thumbnail its vector comes from and the
// detector's larger upright input, from one decode. A decoder panic counts
// as an undecodable image, as for thumbnails.
func renderSearchImage(data []byte) (thumbnail, detectorInput []byte, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			decodepanic.Log("photo search image", recovered)
			thumbnail, detectorInput, err = nil, nil, ErrUnsupportedType
		}
	}()
	mediaType := sniffMediaType(data)
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if mediaType == "" || err != nil || "image/"+format != mediaType || config.Width <= 0 || config.Height <= 0 {
		return nil, nil, ErrUnsupportedType
	}
	if int64(config.Width)*int64(config.Height) > maxSearchImagePixels {
		return nil, nil, ErrTooLarge
	}
	orientation, _ := readEXIF(bytes.NewReader(data), time.UTC)
	src, err := imaging.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, nil, ErrUnsupportedType
	}
	if thumbnail, _, _, err = uprightJPEG(src, orientation, thumbnailEdge, thumbnailQuality); err != nil {
		return nil, nil, err
	}
	if detectorInput, _, _, err = uprightJPEG(src, orientation, aiInputEdge, aiInputQuality); err != nil {
		return nil, nil, err
	}
	return thumbnail, detectorInput, nil
}

// embedSearchImage asks the Worker for the vector of a rendered thumbnail.
func (s *Service) embedSearchImage(ctx context.Context, thumbnail []byte) ([]float32, error) {
	file, err := memoryFile(thumbnail)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	vector, err := s.embedder.EmbedImage(ctx, file)
	if errors.Is(err, ErrAIRejected) {
		return nil, ErrUnsupportedType
	}
	return vector, err
}

// searchObjects returns the best score per class of the objects in an image
// to search by, when the Worker's detector is the one whose results search
// uses. Without it, or when detection fails, the image is matched by its
// vector alone, as a photo still waiting for the detector is.
func (s *Service) searchObjects(ctx context.Context, info EmbedderInfo, input []byte) []classScore {
	detector, ok := s.embedder.(Detector)
	if !ok || info.Detector == "" || info.Detector != s.objectCalibration.Model {
		return nil
	}
	file, err := memoryFile(input)
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()
	model, boxes, err := detector.DetectObjects(ctx, file)
	// A Worker that took the request keeps the detector loaded; the AI
	// runner releases it on its next round, since this request kept the gate
	// closed (releaseModels). One that could not be reached holds nothing.
	if !errors.Is(err, ErrAIUnavailable) {
		s.aiModelMu.Lock()
		s.detectorLoaded = true
		s.aiModelMu.Unlock()
	}
	if err != nil || model != s.objectCalibration.Model || !validBoxes(boxes) {
		return nil
	}
	return bestScores(boxes)
}

// add keeps a search under id, first dropping the expired ones and, when
// full, the least recently used.
func (x *imageSearches) add(id string, search *imageSearch, now time.Time) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.byID == nil {
		x.byID = make(map[string]*imageSearch)
	}
	oldest := ""
	for key, kept := range x.byID {
		if now.Sub(kept.lastUsed) >= imageSearchIdle {
			delete(x.byID, key)
		} else if oldest == "" || kept.lastUsed.Before(x.byID[oldest].lastUsed) {
			oldest = key
		}
	}
	if len(x.byID) >= maxImageSearches {
		delete(x.byID, oldest)
	}
	search.lastUsed = now
	x.byID[id] = search
}

// get returns the search kept under id for its owner and marks it used.
func (x *imageSearches) get(id, owner string, now time.Time) (*imageSearch, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	search, ok := x.byID[id]
	if !ok || search.owner != owner || now.Sub(search.lastUsed) >= imageSearchIdle {
		return nil, false
	}
	search.lastUsed = now
	return search, true
}
