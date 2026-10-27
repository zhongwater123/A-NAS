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
// finds the cats and dogs in each original's enlarged input; the AI Worker's
// animal model embeds each one so that a library's cats and dogs cluster into
// individuals, as faces cluster into people. Cats and dogs go the same way and
// cluster apart, each kind with its own calibration.
//
// A pet scan is queued when a detection finds a cat or a dog, or finds none
// where an earlier one did, and reads them from that detection. Like face
// scans they are derived data with an avatar sheet, keyed by content object.

const (
	petsDerivation = "pets/v1"
	// maxPets bounds the cats and dogs one photo sends, best first; the
	// Worker takes at most as many.
	maxPets = 16
	// petAvatarMargin is how much wider than a pet's box its avatar is; the
	// box already holds the whole animal.
	petAvatarMargin = 1.1
)

// petKinds are the detector's classes for pets and the occurrence kind each
// becomes. A box counts as a pet from the score at which search calls it
// 检测到, and only for the detector that score was measured for.
var petKinds = map[int]string{dogClass: OccurrenceDog, catClass: OccurrenceCat}

// AnimalEmbedder is the AI Worker's animal model. An Embedder that is also an
// AnimalEmbedder gets pet scans while its Info names an animal model.
type AnimalEmbedder interface {
	// EmbedAnimals returns the model's name and the normalized vector of the
	// animal at each box of an upright image, in order. Cancelling ctx stops
	// the work in the Worker.
	EmbedAnimals(ctx context.Context, image *os.File, boxes [][4]float32) (string, [][]float32, error)
}

// petsIn lists the cats and dogs among a detection's boxes, best first.
func (s *Service) petsIn(model string, boxes []Box) []Box {
	if model == "" || model != s.objectCalibration.Model {
		return nil
	}
	var pets []Box
	for _, box := range boxes {
		if _, pet := petKinds[box.Class]; !pet || box.X1 <= box.X0 || box.Y1 <= box.Y0 {
			continue
		}
		word, ok := s.objectCalibration.Words[strconv.Itoa(box.Class)]
		if ok && word.TierScore > 0 && float64(box.Score) >= word.TierScore {
			pets = append(pets, box)
		}
	}
	slices.SortStableFunc(pets, func(a, b Box) int { return cmp.Compare(b.Score, a.Score) })
	if len(pets) > maxPets {
		pets = pets[:maxPets]
	}
	return pets
}

// enqueuePets queues the pet scan of an object whose detection just found
// cats or dogs, or found none where an earlier scan stored some, while a
// library with people on holds it.
func (s *Service) enqueuePets(ctx context.Context, tx *sql.Tx, objectID, model string, boxes []Box) error {
	if len(s.petsIn(model, boxes)) == 0 {
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
// model shows a cat or a dog and that has no pet scan yet: originals
// detected before pets were told apart, or while the Worker had no animal
// model.
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
	defer rows.Close()
	var waiting []string
	for rows.Next() {
		var objectID string
		var encoded []byte
		if err := rows.Scan(&objectID, &encoded); err != nil {
			return err
		}
		if boxes, err := decodeBoxes(encoded); err == nil && len(s.petsIn(model, boxes)) > 0 {
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

// storedPets lists the cats and dogs of an object's stored detection.
func (s *Service) storedPets(ctx context.Context, objectID string) ([]Box, error) {
	model, boxes, err := s.storedDetection(ctx, objectID)
	return s.petsIn(model, boxes), err
}

// finishPets stores a pet scan of cats and dogs or records why it failed.
func (s *Service) finishPets(ctx context.Context, job claimedJob, model string, pets []Box, vectors [][]float32, err error, input *os.File) error {
	switch {
	case errors.Is(err, ErrAIRejected):
		return s.failJob(ctx, job, jobErrorUndecodable)
	case err != nil:
		return s.failJob(ctx, job, jobErrorIO)
	case len(vectors) != len(pets):
		return s.failJob(ctx, job, jobErrorInvalidResult)
	}
	result := scan{derivation: petsDerivation, model: model, margin: petAvatarMargin}
	dimensions := -1
	for i, pet := range pets {
		if dimensions < 0 {
			dimensions = len(vectors[i])
		}
		if !validUnit(vectors[i], dimensions) {
			return s.failJob(ctx, job, jobErrorInvalidResult)
		}
		result.found = append(result.found, found{kind: petKinds[pet.Class], box: [4]float32{pet.X0, pet.Y0, pet.X1, pet.Y1},
			score: pet.Score, vector: vectors[i]})
	}
	return s.completeScan(ctx, job, result, input)
}
