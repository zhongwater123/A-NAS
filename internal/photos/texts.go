package photos

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/zhongwater123/A-NAS/internal/photos/textread"
)

// Reading the text in photos (docs/architecture/photo-ocr.md): the photos
// routed for it (textrouting.go) are read in a phase of their own, after the
// other AI work, the newest photo first. A reading is derived and kept per
// content object; its scan records the model, so that a new model reads
// again. Each library can turn reading off, as with people: the owner of a
// private library, an administrator for the shared one.

// inTextLibrary is the condition that the object whose id is the SQL
// expression object has a photo, trashed or not, in a library with reading
// on.
func inTextLibrary(object string) string {
	return "EXISTS (SELECT 1 FROM assets ta JOIN libraries tl ON tl.id = ta.library_id WHERE ta.object_id = " +
		object + " AND tl.texts = 1)"
}

func textsOn(ctx context.Context, q queryer, libraryID string) (bool, error) {
	var on bool
	err := q.QueryRowContext(ctx, "SELECT texts FROM libraries WHERE id = ?", libraryID).Scan(&on)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return on, err
}

// claimTextJob leases the due reading of the newest photo: the most recent
// receipts and cards are the ones most often looked for.
func (s *Service) claimTextJob(ctx context.Context) (claimedJob, error) {
	return s.claimJobIn(ctx, textDerivation, "",
		"(SELECT MAX(IFNULL(a.taken_at, a.imported_at)) FROM assets a WHERE a.object_id = j.object_id) DESC, j.object_id")
}

// textReading is what one round read from a photo.
type textReading struct {
	model  string
	kind   textread.Kind
	text   string
	fields map[string]string
	checks map[string]textread.Check
}

// completeText stores a reading and retires its job.
func (s *Service) completeText(ctx context.Context, job claimedJob, reading textReading) error {
	fields, err := json.Marshal(reading.fields)
	if err != nil {
		return err
	}
	checks, err := json.Marshal(reading.checks)
	if err != nil {
		return err
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM jobs WHERE object_id = ? AND derivation = ?)",
			job.objectID, job.derivation).Scan(&exists); err != nil || !exists {
			// Purged, or its library turned reading off, while the Worker read.
			return err
		}
		now := formatTime(s.now())
		if _, err := tx.ExecContext(ctx, `
INSERT INTO texts(object_id, model, kind, text, fields, checks, created_at) VALUES(?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(object_id) DO UPDATE SET model = excluded.model, kind = excluded.kind, text = excluded.text,
  fields = excluded.fields, checks = excluded.checks, created_at = excluded.created_at`,
			job.objectID, reading.model, string(reading.kind), reading.text, string(fields), string(checks), now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO scans(object_id, derivation, model, created_at) VALUES(?, ?, ?, ?)
ON CONFLICT(object_id, derivation) DO UPDATE SET model = excluded.model, created_at = excluded.created_at`,
			job.objectID, textDerivation, reading.model, now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM jobs WHERE object_id = ? AND derivation = ?", job.objectID, job.derivation)
		return err
	})
}

// requeueStaleTexts queues again the photos read by another model than the
// Worker's reader, in libraries that still read text.
func (s *Service) requeueStaleTexts(ctx context.Context, model string) error {
	now := formatTime(s.now())
	_, err := s.db.ExecContext(ctx, `
INSERT INTO jobs(object_id, derivation, state, not_before, created_at)
SELECT sc.object_id, sc.derivation, 'pending', ?, ? FROM scans sc
WHERE sc.derivation = ? AND sc.model <> ? AND `+inTextLibrary("sc.object_id")+`
ON CONFLICT(object_id, derivation) DO NOTHING`, now, now, textDerivation, model)
	return err
}

// SetTexts turns reading the text in photos on or off for a library. Off
// clears the readings of the photos no library that reads still holds and
// keeps what members corrected; on routes the library's photos again.
func (s *Service) SetTexts(ctx context.Context, p Principal, libraryID string, on bool) (Library, error) {
	var lib library
	changed := false
	s.commitMu.Lock()
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var err error
		if lib, err = s.visibleLibrary(ctx, tx, p, libraryID); err != nil {
			return err
		}
		if !canSetPeople(p, lib) {
			return ErrForbidden
		}
		if was, err := textsOn(ctx, tx, lib.id); err != nil || was == on {
			return err
		}
		changed = true
		if _, err := tx.ExecContext(ctx, "UPDATE libraries SET texts = ? WHERE id = ?", on, lib.id); err != nil {
			return err
		}
		action := "library.texts_on"
		if !on {
			action = "library.texts_off"
			// The photos of the library that no library reading text still holds.
			gone := "SELECT a.object_id FROM assets a WHERE a.library_id = ? AND NOT " + inTextLibrary("a.object_id")
			for _, statement := range []string{
				"DELETE FROM texts WHERE object_id IN (" + gone + ")",
				"DELETE FROM scans WHERE derivation = '" + textDerivation + "' AND object_id IN (" + gone + ")",
				"DELETE FROM jobs WHERE derivation = '" + textDerivation + "' AND object_id IN (" + gone + ")",
			} {
				if _, err := tx.ExecContext(ctx, statement, lib.id); err != nil {
					return err
				}
			}
		}
		return s.audit(ctx, tx, p.UserID, action, lib.id, "")
	})
	s.commitMu.Unlock()
	if err != nil {
		return Library{}, err
	}
	if changed && on {
		// The stored vectors are routed again; those of libraries still off
		// stay out.
		s.routeIndex.mu.Lock()
		s.routeIndex.routed = false
		s.routeIndex.mu.Unlock()
		s.ai.wakeUp()
	}
	return s.publicSwitches(ctx, p, lib)
}

// publicSwitches is the library as p sees it, with its AI switches.
func (s *Service) publicSwitches(ctx context.Context, p Principal, lib library) (Library, error) {
	public := publicLibrary(p, lib, s.now())
	var err error
	if public.People, err = peopleOn(ctx, s.db, lib.id); err != nil {
		return Library{}, err
	}
	if public.Texts, err = textsOn(ctx, s.db, lib.id); err != nil {
		return Library{}, err
	}
	public.CanSetPeople = canSetPeople(p, lib)
	return public, nil
}

// textMinConfidence is the token probability every token of a field's value
// must reach to pass (docs/architecture/photo-ocr.md#核对层). Measured with
// Qwen3.5-4B on 50 CC-OCR receipts and cards, the other checks passed 91% of
// the fields with 12% wrong; at 0.99, 45% pass with 4.7% wrong, most of them
// differing only in format, while 0.9 let a serial number one digit off pass
// (docs/research/photo-ocr-model-evaluation.md, 核对层实测).
const textMinConfidence = 0.99

// errReaderChanged: the Worker answered the two reads of one photo with
// different models.
var errReaderChanged = errors.New("the text reader changed between the reads of one photo")

// readText reads the next routed photo, the newest first, once there is
// nothing to detect (docs/architecture/photo-ocr.md#资源策略): its whole
// text, and for a known document type its fields, which are then checked.
// The reader needs memory of its own, so the detection models go first and
// the gate weighs the reader's need. When gate closes meanwhile, the Worker
// is hung up on and the job goes back without spending its attempt.
func (r *aiRunner) readText(ctx context.Context, m models, gate Gate, info EmbedderInfo) (bool, error) {
	job, err := r.lib.claimTextJob(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	r.switchPhase(ctx, m.detector, true)
	if open, reason := gate.Open(ctx, info.OCRMemoryBytes); !open {
		r.progress.set(AIPaused, reason, "")
		r.releaseModels(ctx, m.detector, reason)
		return false, r.lib.returnJob(ctx, job)
	}
	r.progress.set(AIWorking, "", info.Model)
	file, class, err := r.lib.openAIInput(ctx, job)
	switch {
	case err != nil:
		return false, errors.Join(err, r.lib.releaseJob(ctx, job))
	case class == jobWaiting:
		return false, r.lib.releaseJob(ctx, job)
	case class != "":
		return true, r.lib.failJob(ctx, job, class)
	}
	defer func() { _ = file.Close() }()
	r.markReaderLoaded()

	readCtx, cancel := context.WithCancel(ctx)
	stopWatching := watchGate(readCtx, gate, info.OCRMemoryBytes, cancel)
	model, whole, err := m.reader.ReadText(readCtx, file, nil)
	reading := textReading{model: model, kind: textread.Plain, text: whole.Text}
	if err == nil {
		reading.kind = textread.Classify(whole.Text)
		if template := textread.Template(reading.kind); template != nil {
			reading.fields, reading.checks, err = r.readFields(readCtx, m.reader, file, model, reading.kind, template, whole.Text)
		}
	}
	paused := stopWatching()
	cancel()
	switch {
	case ctx.Err() != nil:
		return false, r.lib.returnJob(context.WithoutCancel(ctx), job)
	case paused != "":
		r.progress.set(AIPaused, paused, "")
		err := r.lib.returnJob(ctx, job)
		r.releaseModels(ctx, m.detector, paused)
		return false, err
	case errors.Is(err, ErrAIUnavailable) || errors.Is(err, errReaderChanged) || (err == nil && model != info.OCR):
		// Look again next round: the Worker may have lost or changed its reader.
		r.forgetModels()
		return false, r.lib.releaseJob(ctx, job)
	case errors.Is(err, ErrAIRejected):
		return true, r.lib.failJob(ctx, job, jobErrorUndecodable)
	case err != nil:
		return true, r.lib.failJob(ctx, job, jobErrorIO)
	}
	return true, r.lib.completeText(ctx, job, reading)
}

// readFields reads a typed photo's fields with its type's template and
// checks them against the whole text.
func (r *aiRunner) readFields(ctx context.Context, reader TextReader, file *os.File, model string, kind textread.Kind,
	template json.RawMessage, whole string,
) (map[string]string, map[string]textread.Check, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, nil, err
	}
	fieldsModel, answer, err := reader.ReadText(ctx, file, template)
	if err != nil {
		return nil, nil, err
	}
	if fieldsModel != model {
		return nil, nil, errReaderChanged
	}
	tokens := make([]textread.Token, len(answer.Tokens))
	for i, token := range answer.Tokens {
		tokens[i] = textread.Token{Text: token.Token, LogProb: token.LogProb}
	}
	fields := textread.Fields(kind, answer.Text)
	return fields, textread.Verify(kind, fields, whole, tokens, textread.Options{MinConfidence: textMinConfidence}), nil
}
