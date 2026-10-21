package photos

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"image"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// scaleEmbedder answers at once with random unit vectors of the product's
// size, so that the timings below are the photo service's own work: the
// Worker's encoding of the query comes on top on a device.
type scaleEmbedder struct{ random *rand.Rand }

const scaleVectorModel = "scale-vector"

func (e scaleEmbedder) Info(context.Context) (EmbedderInfo, error) {
	return EmbedderInfo{Model: scaleVectorModel, Dimensions: 768}, nil
}
func (e scaleEmbedder) EmbedImage(context.Context, *os.File) ([]float32, error) {
	return e.vector(), nil
}
func (e scaleEmbedder) EmbedQuery(context.Context, string) ([]float32, error) {
	return e.vector(), nil
}
func (e scaleEmbedder) vector() []float32 {
	vector := make([]float32, 768)
	var norm float64
	for i := range vector {
		value := e.random.NormFloat64()
		vector[i] = float32(value)
		norm += value * value
	}
	for i := range vector {
		vector[i] /= float32(math.Sqrt(norm))
	}
	return vector
}

// TestScaleSearch times search at 20,000 photos, all in one library the
// caller searches, each with an image vector and detections as the product
// stores them: a sentence, a thing the detector finds reliably, similar
// photos and an image search, with the indexes loaded and right after a
// start. It runs only with ANAS_PHOTO_SCALE=1; device numbers come from the
// Experimental NAS (issue #209, B1).
func TestScaleSearch(t *testing.T) {
	if os.Getenv("ANAS_PHOTO_SCALE") == "" {
		t.Skip("set ANAS_PHOTO_SCALE=1 to time search at 20,000 photos")
	}
	const (
		photos = 20000
		budget = 250 * time.Millisecond
	)
	ctx := context.Background()
	var clock atomic.Int64
	clock.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Add(int64(time.Minute))).UTC() }
	random := rand.New(rand.NewPCG(7, 11))
	service, err := Open(filepath.Join(t.TempDir(), "photos"), Options{Now: now, DisableCapacityReserve: true, AI: scaleEmbedder{random}})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	alice := Principal{UserID: "user:alice", Username: "alice"}
	libraries, err := service.Libraries(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	var shared Library
	for _, lib := range libraries {
		if lib.Kind == LibraryKindShared {
			shared = lib
		}
	}

	seeded := time.Now()
	var sample Asset
	for i := range photos {
		asset, err := service.Import(ctx, alice, ImportRequest{LibraryID: shared.ID, Name: fmt.Sprintf("IMG_%05d.png", i), Content: bytes.NewReader(syntheticPNG(t, i))})
		if err != nil {
			t.Fatalf("import %d: %v", i, err)
		}
		if i == photos/2 {
			sample = asset
		}
	}
	// Vectors and detections as the AI runner stores them. One photo in 20
	// shows a cat clearly, so a search for 猫 has a detected group.
	cat := service.matcher.Mentions("猫")
	if len(cat) != 1 || len(cat[0].Classes) == 0 {
		t.Fatalf("猫 names no detector class: %+v", cat)
	}
	objectIDs, err := service.strings(ctx, "SELECT object_id FROM assets")
	if err != nil {
		t.Fatal(err)
	}
	err = service.withTx(ctx, func(tx *sql.Tx) error {
		stamp := formatTime(now())
		for n, objectID := range objectIDs {
			if _, err := tx.ExecContext(ctx, "INSERT INTO embeddings(object_id, derivation, model, vector, created_at) VALUES(?, ?, ?, ?, ?)",
				objectID, embeddingDerivation, scaleVectorModel, encodeVector(scaleEmbedder{random}.vector()), stamp); err != nil {
				return err
			}
			boxes := make([]Box, 20+random.IntN(81))
			for i := range boxes {
				x, y := random.Float32()*0.8, random.Float32()*0.8
				boxes[i] = Box{Class: random.IntN(365), Score: 0.05 + random.Float32()*0.6, X0: x, Y0: y, X1: x + 0.1, Y1: y + 0.1}
			}
			if n%20 == 0 {
				boxes[0] = Box{Class: cat[0].Classes[0], Score: 0.9, X0: 0.2, Y0: 0.2, X1: 0.6, Y1: 0.6}
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO detections(object_id, derivation, model, boxes, created_at) VALUES(?, ?, ?, ?, ?)",
				objectID, detectionDerivation, service.objectCalibration.Model, encodeBoxes(boxes), stamp); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("seeded %d photos with vectors and detections in %s", len(objectIDs), time.Since(seeded).Round(time.Second))

	search := func(query string) func() error {
		return func() error {
			page, err := service.Search(ctx, alice, SearchRequest{Query: query})
			if err == nil && len(page.Assets) == 0 {
				err = fmt.Errorf("%s found nothing", query)
			}
			return err
		}
	}
	// Right after a start the vectors and detections are read from the
	// Catalog by the first search.
	cold, coldWorst := measure(t, 3, func() error {
		service.index = vectorIndex{}
		service.detected = objectIndex{}
		return search("傍晚海边散步的一家人")()
	})
	t.Logf("%-36s median %9s  max %9s", "first search after a start", cold.Round(time.Millisecond), coldWorst.Round(time.Millisecond))

	imageSearch, err := service.SearchByImage(ctx, alice, bytes.NewReader(syntheticPNG(t, photos+1)))
	if err != nil {
		t.Fatal(err)
	}
	operations := []struct {
		name string
		run  func() error
	}{
		{"search a sentence", search("傍晚海边散步的一家人")},
		{"search a thing (猫)", search("猫")},
		{"similar photos", func() error { _, err := service.Similar(ctx, alice, sample.ID, SimilarRequest{}); return err }},
		{"image search results", func() error {
			_, err := service.ImageSearchResults(ctx, alice, imageSearch, SimilarRequest{})
			return err
		}},
		{"timeline first page", func() error { _, err := service.Timeline(ctx, alice, shared.ID, "", 120); return err }},
	}
	var slow []string
	for _, operation := range operations {
		median, worst := measure(t, 30, operation.run)
		t.Logf("%-36s median %9s  max %9s", operation.name, median.Round(100*time.Microsecond), worst.Round(100*time.Microsecond))
		if median > budget {
			slow = append(slow, operation.name)
		}
	}
	if len(slow) > 0 {
		t.Errorf("over the %s budget at %d photos: %v", budget, photos, slow)
	}
}

// TestDerivedSizePerPhoto reports what one photo adds to the data volume
// besides its original (issue #209, B2): the thumbnail, rendered here from
// real photos in ANAS_PHOTO_SAMPLES the way the media jobs render it, and
// the image vector and detections, whose sizes follow from their formats.
// Faces and pets are measured with their own evaluations.
func TestDerivedSizePerPhoto(t *testing.T) {
	dir := os.Getenv("ANAS_PHOTO_SAMPLES")
	if dir == "" {
		t.Skip("set ANAS_PHOTO_SAMPLES to a folder of JPEG photos")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var thumbnails, originals []int
	var pixels float64
	for _, entry := range entries {
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".jpg") {
			continue
		}
		file, err := os.Open(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		orientation, _ := readEXIF(file, time.UTC)
		if _, err := file.Seek(0, 0); err != nil {
			t.Fatal(err)
		}
		thumbnail, _, _, err := renderThumbnail(file, orientation)
		info, statErr := file.Stat()
		_ = file.Close()
		if err != nil || statErr != nil {
			t.Fatalf("%s: %v %v", entry.Name(), err, statErr)
		}
		config, _, err := imageConfig(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		pixels += float64(config.Width * config.Height)
		thumbnails = append(thumbnails, len(thumbnail))
		originals = append(originals, int(info.Size()))
	}
	if len(thumbnails) == 0 {
		t.Fatal("no JPEG photos in ANAS_PHOTO_SAMPLES")
	}
	slices.Sort(thumbnails)
	average := func(values []int) float64 {
		sum := 0
		for _, value := range values {
			sum += value
		}
		return float64(sum) / float64(len(values))
	}
	vector := len(encodeVector(make([]float32, 768)))
	detections := len(encodeBoxes(make([]Box, 100)))
	t.Logf("%d photos, %.1f MP and %.0f KiB on average", len(thumbnails), pixels/float64(len(thumbnails))/1e6, average(originals)/1024)
	t.Logf("thumbnail: average %.1f KiB, median %.1f KiB, 95th percentile %.1f KiB",
		average(thumbnails)/1024, float64(thumbnails[len(thumbnails)/2])/1024, float64(thumbnails[len(thumbnails)*95/100])/1024)
	t.Logf("image vector %d bytes; detections at most %d bytes (100 boxes)", vector, detections)
}

// imageConfig reads an image's dimensions without decoding it.
func imageConfig(path string) (image.Config, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return image.Config{}, "", err
	}
	defer file.Close()
	return image.DecodeConfig(file)
}
