package photos

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"math"
	"slices"
	"sync"

	"github.com/zhongwater123/A-NAS/internal/photos/objects"
)

// Object detection (docs/architecture/photo-objects.md): the AI Worker's
// detector lists the objects in each original's upright, enlarged image. The
// boxes are derived data like vectors: keyed by content object, rebuilt when
// the detector changes and deleted with the object.

const (
	detectionDerivation = "objects/v1"
	// boxesVersion is the first byte of a stored box list.
	boxesVersion = 1
	// boxSize is a box's stored size: class, score and four corners.
	boxSize = 2 + 4 + 4*2
)

// validBoxes rejects what a detector cannot have meant: unknown classes,
// scores outside [0, 1] and corners outside the image or out of order.
func validBoxes(boxes []Box) bool {
	if len(boxes) > 1000 {
		return false
	}
	for _, box := range boxes {
		if box.Class < 0 || box.Class >= objects.Classes {
			return false
		}
		for _, value := range []float32{box.Score, box.X0, box.Y0, box.X1, box.Y1} {
			if math.IsNaN(float64(value)) || value < 0 || value > 1 {
				return false
			}
		}
		if box.X0 > box.X1 || box.Y0 > box.Y1 {
			return false
		}
	}
	return true
}

// encodeBoxes stores boxes compactly: corners in 65,535ths of the image are
// far finer than the detector's own precision.
func encodeBoxes(boxes []Box) []byte {
	encoded := make([]byte, 1, 1+boxSize*len(boxes))
	encoded[0] = boxesVersion
	corner := func(value float32) uint16 { return uint16(math.Round(float64(value) * math.MaxUint16)) }
	for _, box := range boxes {
		encoded = binary.LittleEndian.AppendUint16(encoded, uint16(box.Class))
		encoded = binary.LittleEndian.AppendUint32(encoded, math.Float32bits(box.Score))
		for _, value := range []float32{box.X0, box.Y0, box.X1, box.Y1} {
			encoded = binary.LittleEndian.AppendUint16(encoded, corner(value))
		}
	}
	return encoded
}

func decodeBoxes(encoded []byte) ([]Box, error) {
	if len(encoded) == 0 || encoded[0] != boxesVersion || (len(encoded)-1)%boxSize != 0 {
		return nil, errors.New("stored boxes are malformed")
	}
	boxes := make([]Box, 0, (len(encoded)-1)/boxSize)
	for at := 1; at < len(encoded); at += boxSize {
		corner := func(i int) float32 {
			return float32(binary.LittleEndian.Uint16(encoded[at+6+2*i:])) / math.MaxUint16
		}
		boxes = append(boxes, Box{
			Class: int(binary.LittleEndian.Uint16(encoded[at:])),
			Score: math.Float32frombits(binary.LittleEndian.Uint32(encoded[at+2:])),
			X0:    corner(0), Y0: corner(1), X1: corner(2), Y1: corner(3),
		})
	}
	return boxes, nil
}

// classScore is a class an object shows and the detector's best score for it.
type classScore struct {
	class int
	score float32
}

// bestScores reduces boxes to each class's best score, by class.
func bestScores(boxes []Box) []classScore {
	best := map[int]float32{}
	for _, box := range boxes {
		best[box.Class] = max(best[box.Class], box.Score)
	}
	scores := make([]classScore, 0, len(best))
	for class, score := range best {
		scores = append(scores, classScore{class, score})
	}
	slices.SortFunc(scores, func(a, b classScore) int { return a.class - b.class })
	return scores
}

// objectIndex keeps one detector model's best class scores in memory, keyed
// by content object, as vectorIndex keeps vectors. Search reads it for every
// query; 20,000 photos with a few dozen classes each take a few MB.
type objectIndex struct {
	mu     sync.RWMutex
	model  string
	scores map[string][]classScore
}

// read calls use with the scores of model, loading them first when needed.
// An empty model means no scores.
func (x *objectIndex) read(ctx context.Context, s *Service, model string, use func(map[string][]classScore)) error {
	if model == "" {
		use(nil)
		return nil
	}
	x.mu.RLock()
	if x.scores != nil && x.model == model {
		defer x.mu.RUnlock()
		use(x.scores)
		return nil
	}
	x.mu.RUnlock()

	x.mu.Lock()
	defer x.mu.Unlock()
	if x.scores == nil || x.model != model {
		scores, err := s.loadDetections(ctx, model)
		if err != nil {
			return err
		}
		x.model, x.scores = model, scores
	}
	use(x.scores)
	return nil
}

// put adds the scores of a detection by model; those of a model not loaded
// wait for the next load.
func (x *objectIndex) put(objectID, model string, scores []classScore) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.scores != nil && x.model == model {
		x.scores[objectID] = scores
	}
}

func (x *objectIndex) forget(objectIDs []string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	for _, id := range objectIDs {
		delete(x.scores, id)
	}
}

func (s *Service) loadDetections(ctx context.Context, model string) (map[string][]classScore, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT object_id, boxes FROM detections WHERE derivation = ? AND model = ?",
		detectionDerivation, model)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	scores := make(map[string][]classScore)
	for rows.Next() {
		var objectID string
		var encoded []byte
		if err := rows.Scan(&objectID, &encoded); err != nil {
			return nil, err
		}
		boxes, err := decodeBoxes(encoded)
		if err != nil {
			continue
		}
		scores[objectID] = bestScores(boxes)
	}
	return scores, errors.Join(rows.Err(), rows.Close())
}

// enqueueDetection schedules the detection of an object whose thumbnail has
// just been stored; the thumbnail proves the original decodes.
func (s *Service) enqueueDetection(ctx context.Context, tx *sql.Tx, objectID string) error {
	now := formatTime(s.now())
	_, err := tx.ExecContext(ctx, `
INSERT INTO jobs(object_id, derivation, state, not_before, created_at)
SELECT ?, ?, 'pending', ?, ?
WHERE NOT EXISTS (SELECT 1 FROM detections WHERE object_id = ? AND derivation = ?) AND `+notVideo("?")+`
ON CONFLICT(object_id, derivation) DO NOTHING`,
		objectID, detectionDerivation, now, now, objectID, detectionDerivation, objectID)
	return err
}

func (s *Service) completeDetection(ctx context.Context, job claimedJob, model string, boxes []Box) error {
	stored := false
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM jobs WHERE object_id = ? AND derivation = ?)",
			job.objectID, job.derivation).Scan(&exists); err != nil || !exists {
			// Purged while the Worker was busy.
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO detections(object_id, derivation, model, boxes, created_at) VALUES(?, ?, ?, ?, ?)
ON CONFLICT(object_id, derivation) DO UPDATE SET model = excluded.model, boxes = excluded.boxes, created_at = excluded.created_at`,
			job.objectID, job.derivation, model, encodeBoxes(boxes), formatTime(s.now())); err != nil {
			return err
		}
		if err := s.enqueuePets(ctx, tx, job.objectID, model, boxes); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM jobs WHERE object_id = ? AND derivation = ?", job.objectID, job.derivation)
		stored = err == nil
		return err
	})
	if err == nil && stored {
		s.detected.put(job.objectID, model, bestScores(boxes))
	}
	return err
}
