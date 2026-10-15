package photos

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"os"
	"slices"
	"strconv"
)

// Pets (docs/architecture/photo-people.md#宠物): the object detector already
// finds the dogs in each original's enlarged input; the AI Worker's animal
// model embeds each one so that a library's dogs cluster into individuals,
// as faces cluster into people. Cats are not told apart: no model kept look-
// alike cats apart in the evaluation, so they stay AI 聚合's one 猫 group
// (docs/research/photo-people-evaluation.md#宠物).
//
// A pet scan is queued when a detection finds a dog, or finds none where an
// earlier one did, and reads the dogs from that detection. Like face scans
// they are derived data with an avatar sheet, keyed by content object.

const (
	petsDerivation = "pets/v1"
	// dogClass is the detector's dog; a box counts as a dog from the score
	// at which search calls it 检测到, and only for the detector that score
	// was measured for.
	dogClass = 93
	// maxPets bounds the dogs one photo sends, best first; the Worker takes
	// at most as many.
	maxPets = 16
	// petAvatarMargin is how much wider than a dog's box its avatar is; the
	// box already holds the whole animal.
	petAvatarMargin = 1.1
)

// AnimalEmbedder is the AI Worker's animal model. An Embedder that is also an
// AnimalEmbedder gets pet scans while its Info names an animal model.
type AnimalEmbedder interface {
	// EmbedAnimals returns the model's name and the normalized vector of the
	// animal at each box of an upright image, in order. Cancelling ctx stops
	// the work in the Worker.
	EmbedAnimals(ctx context.Context, image *os.File, boxes [][4]float32) (string, [][]float32, error)
}

// dogsIn lists the dogs among a detection's boxes, best first.
func (s *Service) dogsIn(model string, boxes []Box) []Box {
	word, ok := s.objectCalibration.Words[strconv.Itoa(dogClass)]
	if model == "" || model != s.objectCalibration.Model || !ok || word.TierScore <= 0 {
		return nil
	}
	var dogs []Box
	for _, box := range boxes {
		if box.Class == dogClass && float64(box.Score) >= word.TierScore && box.X1 > box.X0 && box.Y1 > box.Y0 {
			dogs = append(dogs, box)
		}
	}
	slices.SortStableFunc(dogs, func(a, b Box) int { return cmp.Compare(b.Score, a.Score) })
	if len(dogs) > maxPets {
		dogs = dogs[:maxPets]
	}
	return dogs
}

// enqueuePets queues the pet scan of an object whose detection just found
// dogs, or found none where an earlier scan stored some, while a library with
// people on holds it.
func (s *Service) enqueuePets(ctx context.Context, tx *sql.Tx, objectID, model string, boxes []Box) error {
	if len(s.dogsIn(model, boxes)) == 0 {
		var scanned bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM occurrences WHERE object_id = ? AND derivation = ?)",
			objectID, petsDerivation).Scan(&scanned); err != nil || !scanned {
			return err
		}
	}
	now := formatTime(s.now())
	_, err := tx.ExecContext(ctx, `
INSERT INTO jobs(object_id, derivation, state, not_before, created_at) SELECT ?, ?, 'pending', ?, ?
WHERE `+inPeopleLibrary("?")+`
ON CONFLICT(object_id, derivation) DO NOTHING`, objectID, petsDerivation, now, now, objectID)
	return err
}

// enqueueMissingPets queues the scan of every object whose detection by
// model shows a dog and that has no pet scan yet: originals detected before
// pets were told apart, or while the Worker had no animal model.
func (s *Service) enqueueMissingPets(ctx context.Context, model string) error {
	rows, err := s.db.QueryContext(ctx, `
SELECT d.object_id, d.boxes FROM detections d
WHERE d.derivation = ? AND d.model = ? AND `+inPeopleLibrary("d.object_id")+`
  AND NOT EXISTS (SELECT 1 FROM scans p WHERE p.object_id = d.object_id AND p.derivation = ?)
  AND NOT EXISTS (SELECT 1 FROM jobs j WHERE j.object_id = d.object_id AND j.derivation = ?)`,
		detectionDerivation, model, petsDerivation, petsDerivation)
	if err != nil {
		return err
	}
	var waiting []string
	for rows.Next() {
		var objectID string
		var encoded []byte
		if err := rows.Scan(&objectID, &encoded); err != nil {
			_ = rows.Close()
			return err
		}
		if boxes, err := decodeBoxes(encoded); err == nil && len(s.dogsIn(model, boxes)) > 0 {
			waiting = append(waiting, objectID)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil || len(waiting) == 0 {
		return err
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		now := formatTime(s.now())
		for _, objectID := range waiting {
			if _, err := tx.ExecContext(ctx, `
INSERT INTO jobs(object_id, derivation, state, not_before, created_at) VALUES(?, ?, 'pending', ?, ?)
ON CONFLICT(object_id, derivation) DO NOTHING`, objectID, petsDerivation, now, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// storedDetection returns an object's stored detection: its model and boxes,
// or nothing before it has one.
func (s *Service) storedDetection(ctx context.Context, objectID string) (string, []Box, error) {
	var model string
	var encoded []byte
	err := s.db.QueryRowContext(ctx, "SELECT model, boxes FROM detections WHERE object_id = ? AND derivation = ?",
		objectID, detectionDerivation).Scan(&model, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	boxes, err := decodeBoxes(encoded)
	return model, boxes, err
}

// storedDogs lists the dogs of an object's stored detection.
func (s *Service) storedDogs(ctx context.Context, objectID string) ([]Box, error) {
	model, boxes, err := s.storedDetection(ctx, objectID)
	return s.dogsIn(model, boxes), err
}

// finishPets stores a pet scan of dogs or records why it failed.
func (s *Service) finishPets(ctx context.Context, job claimedJob, model string, dogs []Box, vectors [][]float32, err error, input *os.File) error {
	switch {
	case errors.Is(err, ErrAIRejected):
		return s.failJob(ctx, job, jobErrorUndecodable)
	case err != nil:
		return s.failJob(ctx, job, jobErrorIO)
	case len(vectors) != len(dogs):
		return s.failJob(ctx, job, jobErrorInvalidResult)
	}
	result := scan{derivation: petsDerivation, kind: OccurrenceDog, model: model, margin: petAvatarMargin}
	dimensions := -1
	for i, dog := range dogs {
		if dimensions < 0 {
			dimensions = len(vectors[i])
		}
		if !validUnit(vectors[i], dimensions) {
			return s.failJob(ctx, job, jobErrorInvalidResult)
		}
		result.found = append(result.found, found{box: [4]float32{dog.X0, dog.Y0, dog.X1, dog.Y1}, score: dog.Score,
			vector: vectors[i]})
	}
	return s.completeScan(ctx, job, result, input)
}
