package photos

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"io/fs"
	"math"
	"os"

	"github.com/disintegration/imaging"
)

// Faces (docs/architecture/photo-people.md): the AI Worker's face models list
// the faces in each original's upright, enlarged image, the same input the
// object detector reads. Like detections they are derived data, keyed by
// content object and rebuilt when the models change; unlike detections each
// face is a row of its own, an occurrence, because people point at it.
//
// Each scan also renders an avatar sheet: a square crop around every face,
// laid out on a grid in one JPEG stored as the object's faces/v1 derived file,
// so that people lists show faces without decoding originals. Pets
// (pets.go) are scanned the same way.

const (
	facesDerivation = "faces/v1"
	// maxFaces bounds what one photo keeps; the Worker keeps at most as many.
	maxFaces = 64
	// avatarEdge is each crop's side in the avatar sheet, avatarColumns the
	// sheet's width in crops, and avatarMargin how much wider than the face
	// box a crop is, so that it shows the head rather than just the face.
	avatarEdge    = 160
	avatarColumns = 8
	avatarMargin  = 1.6
	avatarQuality = 85
	// occurrenceVersion is the first byte of a stored box or landmarks.
	occurrenceVersion = 1
)

// YuNet finds the faces of dogs and cats too: on the local stack a dog in
// four MPDD photos and the family dog of Gallagher's album became people. A
// face whose centre lies on a dog or cat the object detector sees at
// animalFaceScore or more, and on no person it sees at personFaceScore or
// more, is an animal's and is not kept. Of the stack's 777 faces the rule
// dropped those seven and no person's.
const (
	personClass     = 1
	dogClass        = 93
	catClass        = 140
	animalFaceScore = 0.3
	personFaceScore = 0.5
)

// withoutAnimalFaces leaves out the faces that boxes show to be animals'.
func withoutAnimalFaces(faces []Face, boxes []Box) []Face {
	on := func(face Face, class int, score float64) bool {
		x, y := (face.Box[0]+face.Box[2])/2, (face.Box[1]+face.Box[3])/2
		for _, box := range boxes {
			if box.Class == class && float64(box.Score) >= score && box.X0 <= x && x <= box.X1 && box.Y0 <= y && y <= box.Y1 {
				return true
			}
		}
		return false
	}
	kept := faces[:0:0]
	for _, face := range faces {
		animal := on(face, dogClass, animalFaceScore) || on(face, catClass, animalFaceScore)
		if !animal || on(face, personClass, personFaceScore) {
			kept = append(kept, face)
		}
	}
	return kept
}

// Occurrence kinds.
const (
	OccurrenceFace = "face"
	OccurrenceCat  = "cat"
	OccurrenceDog  = "dog"
)

// validFaces rejects what face models cannot have meant: coordinates outside
// the image, scores outside [0, 1] and vectors that are not unit vectors of
// one length.
func validFaces(faces []Face) bool {
	if len(faces) > maxFaces {
		return false
	}
	dimensions := -1
	for _, face := range faces {
		values := append(face.Box[:], face.Landmarks[:]...)
		values = append(values, face.Score)
		for _, value := range values {
			if math.IsNaN(float64(value)) || value < 0 || value > 1 {
				return false
			}
		}
		if face.Box[0] > face.Box[2] || face.Box[1] > face.Box[3] {
			return false
		}
		if dimensions < 0 {
			dimensions = len(face.Vector)
		}
		if !validUnit(face.Vector, dimensions) {
			return false
		}
	}
	return true
}

// validUnit reports whether vector is a unit vector of dimensions values.
func validUnit(vector []float32, dimensions int) bool {
	if !validVector(vector, dimensions) {
		return false
	}
	var norm float64
	for _, value := range vector {
		norm += float64(value) * float64(value)
	}
	return math.Abs(norm-1) <= 0.01
}

// encodeFractions stores coordinates in 65,535ths of the image.
func encodeFractions(values []float32) []byte {
	encoded := make([]byte, 1, 1+2*len(values))
	encoded[0] = occurrenceVersion
	for _, value := range values {
		encoded = binary.LittleEndian.AppendUint16(encoded, uint16(math.Round(float64(value)*math.MaxUint16)))
	}
	return encoded
}

func decodeFractions(encoded []byte, count int) ([]float32, error) {
	if len(encoded) != 1+2*count || encoded[0] != occurrenceVersion {
		return nil, errors.New("stored coordinates are malformed")
	}
	values := make([]float32, count)
	for i := range values {
		values[i] = float32(binary.LittleEndian.Uint16(encoded[1+2*i:])) / math.MaxUint16
	}
	return values, nil
}

// enqueueFaces schedules the face scan of an object that has its thumbnail
// and no scan, while a library with people on holds it.
func (s *Service) enqueueFaces(ctx context.Context, tx *sql.Tx, objectID string) error {
	now := formatTime(s.now())
	_, err := tx.ExecContext(ctx, `
INSERT INTO jobs(object_id, derivation, state, not_before, created_at)
SELECT ?, ?, 'pending', ?, ?
WHERE NOT EXISTS (SELECT 1 FROM scans WHERE object_id = ? AND derivation = ?)
  AND EXISTS (SELECT 1 FROM derived_files WHERE object_id = ? AND derivation = ?)
  AND `+inPeopleLibrary("?")+` AND `+notVideo("?")+`
ON CONFLICT(object_id, derivation) DO NOTHING`,
		objectID, facesDerivation, now, now, objectID, facesDerivation, objectID, thumbnailDerivation, objectID, objectID)
	return err
}

// found is one face or animal a scan found, as an occurrence stores it.
type found struct {
	kind string
	box  [4]float32
	// landmarks are a face's five points; animals have none.
	landmarks []float32
	score     float32
	vector    []float32
}

// scan is what one face or pet scan of an object found.
type scan struct {
	derivation string
	model      string
	found      []found
	// margin is how much wider than the box an avatar is.
	margin float64
}

// storedOccurrence is an occurrence as the Catalog keeps it.
type storedOccurrence struct {
	id       string
	objectID string
	ordinal  int
	kind     string
	model    string
	box      [4]float32
	vector   []float32
}

// completeFaces stores what the face models found in an object.
func (s *Service) completeFaces(ctx context.Context, job claimedJob, model string, faces []Face, input *os.File) error {
	result := scan{derivation: facesDerivation, model: model, margin: avatarMargin}
	for _, face := range faces {
		result.found = append(result.found, found{kind: OccurrenceFace, box: face.Box, landmarks: face.Landmarks[:],
			score: face.Score, vector: face.Vector})
	}
	return s.completeScan(ctx, job, result, input)
}

// completeScan stores what a scan found in an object and the object's avatar
// sheet, rendered from input, and retires the job. A rescan replaces the
// object's occurrences of that scan; what people the old ones belonged to,
// and what the user decided about them, moves to the new one in the same
// place (docs/architecture/photo-people.md#人物与用户数据).
func (s *Service) completeScan(ctx context.Context, job claimedJob, result scan, input *os.File) error {
	var sheet []byte
	if len(result.found) > 0 {
		var err error
		if sheet, err = avatarSheet(ctx, input, result.found, result.margin); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			return s.failJob(ctx, job, jobErrorUndecodable)
		}
	}
	s.commitMu.Lock()
	defer s.commitMu.Unlock()
	var exists bool
	if err := s.db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM jobs WHERE object_id = ? AND derivation = ?)",
		job.objectID, job.derivation).Scan(&exists); err != nil || !exists {
		// Purged while the Worker was busy.
		return err
	}
	target := derivedPath(result.derivation, job.objectID)
	if sheet != nil {
		if err := s.writeDerived(target, sheet); err != nil {
			return errors.Join(err, s.failJob(ctx, job, jobErrorIO))
		}
	}
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		now := formatTime(s.now())
		old, err := s.occurrencesOf(ctx, tx, job.objectID, result.derivation)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO scans(object_id, derivation, model, created_at) VALUES(?, ?, ?, ?)
ON CONFLICT(object_id, derivation) DO UPDATE SET model = excluded.model, created_at = excluded.created_at`,
			job.objectID, result.derivation, result.model, now); err != nil {
			return err
		}
		// Ordinals of the old occurrences are taken until they are deleted.
		if _, err := tx.ExecContext(ctx, "UPDATE occurrences SET ordinal = -1 - ordinal WHERE object_id = ? AND derivation = ?",
			job.objectID, result.derivation); err != nil {
			return err
		}
		added := make([]storedOccurrence, 0, len(result.found))
		for ordinal, item := range result.found {
			occurrence := storedOccurrence{id: s.randomID(item.kind), objectID: job.objectID, ordinal: ordinal,
				kind: item.kind, model: result.model, box: item.box, vector: item.vector}
			var landmarks []byte
			if item.landmarks != nil {
				landmarks = encodeFractions(item.landmarks)
			}
			if _, err := tx.ExecContext(ctx, `
INSERT INTO occurrences(id, object_id, derivation, ordinal, kind, model, box, landmarks, score, vector, created_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				occurrence.id, job.objectID, result.derivation, ordinal, item.kind, result.model,
				encodeFractions(item.box[:]), landmarks, item.score, encodeVector(item.vector), now); err != nil {
				return err
			}
			added = append(added, occurrence)
		}
		if err := moveDecisions(ctx, tx, old, added); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM occurrences WHERE object_id = ? AND derivation = ? AND ordinal < 0",
			job.objectID, result.derivation); err != nil {
			return err
		}
		if sheet != nil {
			if _, err := tx.ExecContext(ctx, `
INSERT INTO derived_files(object_id, derivation, media_type, width, height, size_bytes, created_at)
VALUES(?, ?, 'image/jpeg', ?, ?, ?, ?)
ON CONFLICT(object_id, derivation) DO UPDATE SET width = excluded.width, height = excluded.height,
    size_bytes = excluded.size_bytes, created_at = excluded.created_at`,
				job.objectID, result.derivation, avatarColumns*avatarEdge, sheetRows(len(result.found))*avatarEdge,
				len(sheet), now); err != nil {
				return err
			}
		} else if _, err := tx.ExecContext(ctx, "DELETE FROM derived_files WHERE object_id = ? AND derivation = ?",
			job.objectID, result.derivation); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM jobs WHERE object_id = ? AND derivation = ?", job.objectID, job.derivation)
		return err
	})
	if err != nil {
		return err
	}
	if sheet == nil {
		// A rescan that found nothing leaves no sheet behind.
		if err := s.root.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	s.aiModelMu.Lock()
	s.scanned++
	s.aiModelMu.Unlock()
	return nil
}

// clusterEvery is how many face or pet scans a long run stores before people
// cluster again; otherwise they cluster once the scans are done.
const clusterEvery = 50

func (s *Service) scansSinceClustering() int {
	s.aiModelMu.Lock()
	defer s.aiModelMu.Unlock()
	return s.scanned
}

func sheetRows(faces int) int { return (faces + avatarColumns - 1) / avatarColumns }

// moveDecisions hands what old occurrences of an object were to the new one
// of the same kind that overlaps each most, when it overlaps at least half:
// who it is, whether the user said so, which people it is not, and covers.
// What clustering decided moves as provisional: the next pass keeps it only
// if the new occurrence is near its person by the new models too.
func moveDecisions(ctx context.Context, tx *sql.Tx, old, added []storedOccurrence) error {
	taken := make(map[string]bool)
	for _, before := range old {
		best, bestOverlap := "", 0.5
		for _, after := range added {
			if after.kind != before.kind || taken[after.id] {
				continue
			}
			if overlap := boxOverlap(before.box, after.box); overlap >= bestOverlap {
				best, bestOverlap = after.id, overlap
			}
		}
		if best == "" {
			continue
		}
		taken[best] = true
		for _, statement := range []string{
			"UPDATE person_occurrences SET occurrence_id = ?, source = CASE source WHEN 'auto' THEN 'moved' ELSE source END WHERE occurrence_id = ?",
			"UPDATE occurrence_rejections SET occurrence_id = ? WHERE occurrence_id = ?",
			"UPDATE people SET cover_occurrence_id = ? WHERE cover_occurrence_id = ?",
		} {
			if _, err := tx.ExecContext(ctx, statement, best, before.id); err != nil {
				return err
			}
		}
	}
	return nil
}

// boxOverlap is the intersection over union of two boxes.
func boxOverlap(a, b [4]float32) float64 {
	width := math.Min(float64(a[2]), float64(b[2])) - math.Max(float64(a[0]), float64(b[0]))
	height := math.Min(float64(a[3]), float64(b[3])) - math.Max(float64(a[1]), float64(b[1]))
	if width <= 0 || height <= 0 {
		return 0
	}
	intersection := width * height
	union := float64((a[2]-a[0])*(a[3]-a[1])+(b[2]-b[0])*(b[3]-b[1])) - intersection
	if union <= 0 {
		return 0
	}
	return intersection / union
}

func (s *Service) occurrencesOf(ctx context.Context, q queryer, objectID, derivation string) ([]storedOccurrence, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT id, ordinal, kind, model, box, vector FROM occurrences WHERE object_id = ? AND derivation = ? ORDER BY ordinal",
		objectID, derivation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var found []storedOccurrence
	for rows.Next() {
		occurrence := storedOccurrence{objectID: objectID}
		var box, vector []byte
		if err := rows.Scan(&occurrence.id, &occurrence.ordinal, &occurrence.kind, &occurrence.model, &box, &vector); err != nil {
			return nil, err
		}
		corners, err := decodeFractions(box, 4)
		if err != nil {
			return nil, err
		}
		copy(occurrence.box[:], corners)
		occurrence.vector = decodeVector(vector)
		found = append(found, occurrence)
	}
	return found, rows.Err()
}

// avatarSheet crops everything found out of the upright input onto one grid.
func avatarSheet(ctx context.Context, input *os.File, items []found, margin float64) ([]byte, error) {
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	err := withDecodedImage(ctx, input, func(src image.Image) error {
		sheet := imaging.New(avatarColumns*avatarEdge, sheetRows(len(items))*avatarEdge, color.White)
		for i, item := range items {
			tile := imaging.Fill(imaging.Crop(src, avatarSquare(src.Bounds(), item.box, margin)), avatarEdge, avatarEdge,
				imaging.Center, imaging.Lanczos)
			sheet = imaging.Paste(sheet, tile, image.Pt(i%avatarColumns*avatarEdge, i/avatarColumns*avatarEdge))
		}
		return imaging.Encode(&buffer, sheet, imaging.JPEG, imaging.JPEGQuality(avatarQuality))
	})
	return buffer.Bytes(), err
}

// avatarSquare is the square around a box, margin times its longer side,
// kept inside the image.
func avatarSquare(bounds image.Rectangle, box [4]float32, margin float64) image.Rectangle {
	width, height := float64(bounds.Dx()), float64(bounds.Dy())
	centreX, centreY := float64(box[0]+box[2])/2*width, float64(box[1]+box[3])/2*height
	side := margin * math.Max(float64(box[2]-box[0])*width, float64(box[3]-box[1])*height)
	side = math.Max(1, math.Min(side, math.Min(width, height)))
	x0 := math.Min(math.Max(0, centreX-side/2), width-side)
	y0 := math.Min(math.Max(0, centreY-side/2), height-side)
	return image.Rect(int(x0), int(y0), int(x0+side), int(y0+side)).Add(bounds.Min)
}

// avatarTile cuts occurrence ordinal out of its object's avatar sheet of
// derivation.
func (s *Service) avatarTile(ctx context.Context, derivation, objectID string, ordinal int) ([]byte, error) {
	file, err := s.root.Open(derivedPath(derivation, objectID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	var buffer bytes.Buffer
	err = withDecodedImage(ctx, file, func(sheet image.Image) error {
		at := image.Pt(ordinal%avatarColumns*avatarEdge, ordinal/avatarColumns*avatarEdge)
		if !at.In(sheet.Bounds()) {
			return fmt.Errorf("avatar %d is outside its sheet", ordinal)
		}
		tile := imaging.Crop(sheet, image.Rectangle{Min: at, Max: at.Add(image.Pt(avatarEdge, avatarEdge))})
		return imaging.Encode(&buffer, tile, imaging.JPEG, imaging.JPEGQuality(avatarQuality))
	})
	return buffer.Bytes(), err
}
